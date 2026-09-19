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

	// For tabular CSV files and JSON documents/arrays, the whole file content
	// preserves structural headers/schemas for decoders to split into child records.
	if ext == ".csv" || ext == ".json" {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("failed to read file %s: %w", filePath, err)
		}

		trimmed := bytes.TrimSpace(data)
		if len(trimmed) == 0 {
			return nil
		}

		rec := model.IngestedRecord{
			Transport:  "file",
			SourceIP:   filepath.Base(filePath),
			SourcePort: 0,
			RawBytes:   trimmed,
			ReceivedAt: time.Now().UTC(),
		}

		select {
		case out <- rec:
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

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := bytes.TrimRight(scanner.Bytes(), "\r\n")
		if len(line) == 0 {
			continue
		}

		payload := make([]byte, len(line))
		copy(payload, line)

		rec := model.IngestedRecord{
			Transport:  "file",
			SourceIP:   filepath.Base(filePath),
			SourcePort: 0,
			RawBytes:   payload,
			ReceivedAt: time.Now().UTC(),
		}

		select {
		case out <- rec:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		return fmt.Errorf("scanner error reading %s: %w", filePath, err)
	}

	return nil
}
