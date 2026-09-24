package output

import (
	"context"
	"fmt"
	"time"
	"worm/internal/model"
)

// RetrySink wraps an underlying OutputSink with configurable retry attempts and backoff.
type RetrySink struct {
	sink        OutputSink
	maxAttempts int
	backoff     time.Duration
}

// NewRetrySink creates an OutputSink that retries failed Emits up to maxAttempts times.
func NewRetrySink(sink OutputSink, maxAttempts int, backoff time.Duration) *RetrySink {
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	if backoff <= 0 {
		backoff = 10 * time.Millisecond
	}
	return &RetrySink{
		sink:        sink,
		maxAttempts: maxAttempts,
		backoff:     backoff,
	}
}

// Emit attempts to deliver the event, retrying with exponential backoff on failure.
func (r *RetrySink) Emit(ctx context.Context, event *model.NormalizedEvent) error {
	var lastErr error
	delay := r.backoff

	for attempt := 1; attempt <= r.maxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := r.sink.Emit(ctx, event)
		if err == nil {
			return nil
		}
		lastErr = err

		if attempt < r.maxAttempts {
			time.Sleep(delay)
			delay *= 2
		}
	}

	return fmt.Errorf("sink delivery failed after %d attempts: %w", r.maxAttempts, lastErr)
}

func (r *RetrySink) Flush() error {
	return r.sink.Flush()
}

func (r *RetrySink) Close() error {
	return r.sink.Close()
}
