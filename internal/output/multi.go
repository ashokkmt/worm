package output

import (
	"context"
	"fmt"
	"strings"
	"worm/internal/model"
)

// MultiSink broadcasts normalized events to multiple output sinks.
type MultiSink struct {
	sinks []OutputSink
}

// NewMultiSink creates a composite sink dispatching to all provided sinks.
func NewMultiSink(sinks ...OutputSink) *MultiSink {
	return &MultiSink{sinks: sinks}
}

func (m *MultiSink) Emit(ctx context.Context, event *model.NormalizedEvent) error {
	var errs []string
	for _, s := range m.sinks {
		if err := s.Emit(ctx, event); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("multi sink emit errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (m *MultiSink) Flush() error {
	var errs []string
	for _, s := range m.sinks {
		if err := s.Flush(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("multi sink flush errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (m *MultiSink) Close() error {
	var errs []string
	for _, s := range m.sinks {
		if err := s.Close(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("multi sink close errors: %s", strings.Join(errs, "; "))
	}
	return nil
}
