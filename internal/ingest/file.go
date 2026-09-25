package ingest

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"worm/internal/model"
)

// FileWatcher monitors an inbox directory for batch files (CSV, JSON, CLF, Syslog dumps)
// and ingests them into the WORM pipeline using atomic file processing.
type FileWatcher struct {
	inboxDir string
	interval time.Duration
	mu       sync.Mutex
	closed   bool
}

// NewFileWatcher creates a file spool watcher for the given directory.
func NewFileWatcher(inboxDir string, interval time.Duration) *FileWatcher {
	if inboxDir == "" {
		inboxDir = "data/inbox"
	}
	if interval <= 0 {
		interval = 1 * time.Second
	}
	return &FileWatcher{
		inboxDir: inboxDir,
		interval: interval,
	}
}

func (w *FileWatcher) Name() string {
	return "file-spool"
}

func (w *FileWatcher) Start(ctx context.Context, out chan<- model.IngestedRecord) error {
	// Ensure directory structure exists
	processingDir := filepath.Join(w.inboxDir, "processing")
	processedDir := filepath.Join(w.inboxDir, "processed")
	failedDir := filepath.Join(w.inboxDir, "failed")

	for _, d := range []string{w.inboxDir, processingDir, processedDir, failedDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", d, err)
		}
	}

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			w.scanInbox(ctx, processingDir, processedDir, failedDir, out)
		}
	}
}

func (w *FileWatcher) scanInbox(ctx context.Context, processingDir, processedDir, failedDir string, out chan<- model.IngestedRecord) {
	entries, err := os.ReadDir(w.inboxDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".tmp") {
			continue
		}

		srcPath := filepath.Join(w.inboxDir, name)
		procPath := filepath.Join(processingDir, name)

		// Atomically move to processing directory
		if err := os.Rename(srcPath, procPath); err != nil {
			continue
		}

		// Ingest file records
		ingestErr := IngestFile(ctx, procPath, out)
		if ingestErr != nil {
			_ = os.Rename(procPath, filepath.Join(failedDir, name))
		} else {
			_ = os.Rename(procPath, filepath.Join(processedDir, name))
		}
	}
}

func (w *FileWatcher) Stop() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return nil
}

// IngestFile reads a file from disk and emits its records to the ingestion channel.
func IngestFile(ctx context.Context, filePath string, out chan<- model.IngestedRecord) error {
	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("stat failed for %s: %w", filePath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("target is a directory, not a file: %s", filePath)
	}

	ext := strings.ToLower(filepath.Ext(filePath))

	// For tabular CSV files, JSON documents/arrays, and XML documents, the whole file content
	// preserves structural headers/schemas for decoders to split into child records.
	if ext == ".csv" || ext == ".json" || ext == ".xml" {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("failed to read file %s: %w", filePath, err)
		}

		if len(data) == 0 {
			return nil
		}

		ackChan := make(chan error, 1)
		rec := model.IngestedRecord{
			Transport:  "file",
			SourceIP:   filepath.Base(filePath),
			SourcePort: 0,
			RawBytes:   data,
			ReceivedAt: time.Now().UTC(),
			Ack:        ackChan,
			Metadata:   model.ReceiveMetadata{FilePath: filePath, FileOffset: 0},
		}

		select {
		case out <- rec:
		case <-ctx.Done():
			return ctx.Err()
		}

		select {
		case ackErr := <-ackChan:
			if ackErr != nil {
				return fmt.Errorf("downstream raw commit failed for %s: %w", filePath, ackErr)
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// For line-delimited log formats (.log, .txt, .ndjson, etc.), scan line by line
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file %s: %w", filePath, err)
	}
	defer file.Close()

	reader := bufio.NewReaderSize(file, 1024*1024+1)
	var offset int64
	for {
		rawLine, readErr := reader.ReadSlice('\n')
		if readErr == bufio.ErrBufferFull {
			return fmt.Errorf("line exceeds 1 MiB limit in %s at offset %d", filePath, offset)
		}
		lineOffset := offset
		offset += int64(len(rawLine))
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := bytes.TrimRight(rawLine, "\r\n")
		if len(line) == 0 {
			if readErr == io.EOF {
				return nil
			}
			if readErr != nil {
				return fmt.Errorf("read %s at offset %d: %w", filePath, lineOffset, readErr)
			}
			continue
		}

		payload := make([]byte, len(line))
		copy(payload, line)

		ackChan := make(chan error, 1)
		rec := model.IngestedRecord{
			Transport:  "file",
			SourceIP:   filepath.Base(filePath),
			SourcePort: 0,
			RawBytes:   payload,
			ReceivedAt: time.Now().UTC(),
			Ack:        ackChan,
			Metadata:   model.ReceiveMetadata{FilePath: filePath, FileOffset: lineOffset},
		}

		select {
		case out <- rec:
		case <-ctx.Done():
			return ctx.Err()
		}

		select {
		case ackErr := <-ackChan:
			if ackErr != nil {
				return fmt.Errorf("downstream raw commit failed for line in %s: %w", filePath, ackErr)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("read %s at offset %d: %w", filePath, lineOffset, readErr)
		}
	}
}
