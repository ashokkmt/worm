package output

import (
	"context"
	"worm/internal/model"
)

// OutputSink represents a destination for normalized WORM events.
type OutputSink interface {
	Emit(ctx context.Context, event *model.NormalizedEvent) error
	Flush() error
	Close() error
}
