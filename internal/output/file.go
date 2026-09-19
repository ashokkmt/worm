package output

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"worm/internal/model"
)

// NDJSONSink appends normalized events to a line-delimited JSON file.
type NDJSONSink struct {
	filePath string
	file     *os.File
	writer   *bufio.Writer
	mu       sync.Mutex
	closed   bool
}

// NewNDJSONSink creates a new NDJSON file sink at the specified path.
func NewNDJSONSink(filePath string) (*NDJSONSink, error) {
	if filePath == "" {
		filePath = "data/output/normalized.ndjson"
	}

	dir := filepath.Dir(filePath)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory for %s: %w", filePath, err)
		}
	}

	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open output file %s: %w", filePath, err)
	}

	return &NDJSONSink{
		filePath: filePath,
		file:     f,
		writer:   bufio.NewWriterSize(f, 64*1024),
	}, nil
}

func (s *NDJSONSink) Emit(ctx context.Context, event *model.NormalizedEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal normalized event: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return fmt.Errorf("sink is closed")
	}

	if _, err := s.writer.Write(data); err != nil {
		return fmt.Errorf("failed to write event: %w", err)
	}
	if err := s.writer.WriteByte('\n'); err != nil {
		return fmt.Errorf("failed to write newline: %w", err)
	}

	return nil
}

func (s *NDJSONSink) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}

	if err := s.writer.Flush(); err != nil {
		return fmt.Errorf("flush failed: %w", err)
	}
	return s.file.Sync()
}

func (s *NDJSONSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true

	flushErr := s.writer.Flush()
	syncErr := s.file.Sync()
	closeErr := s.file.Close()

	if flushErr != nil {
		return flushErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
