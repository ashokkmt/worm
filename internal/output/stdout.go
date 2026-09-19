package output

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"worm/internal/model"
)

// StdoutSink writes normalized events as NDJSON to stdout with thread safety.
type StdoutSink struct {
	mu sync.Mutex
}

func NewStdoutSink() *StdoutSink {
	return &StdoutSink{}
}

func (s *StdoutSink) Emit(ctx context.Context, event *model.NormalizedEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal normalized event: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, err = fmt.Fprintln(os.Stdout, string(data))
	return err
}

func (s *StdoutSink) Flush() error {
	return nil
}

func (s *StdoutSink) Close() error {
	return nil
}
