package output

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"worm/internal/model"
)

// DynamicSink is an atomically replaceable named destination snapshot.
type DynamicSink struct {
	mu    sync.RWMutex
	sinks map[string]OutputSink
}

func NewDynamicSink() *DynamicSink                { return &DynamicSink{sinks: map[string]OutputSink{}} }
func (d *DynamicSink) DestinationNames() []string { return d.Names() }
func (d *DynamicSink) EmitTo(ctx context.Context, name string, e *model.NormalizedEvent) error {
	d.mu.RLock()
	s, ok := d.sinks[name]
	d.mu.RUnlock()
	if !ok {
		return fmt.Errorf("destination %q is not active", name)
	}
	return s.Emit(ctx, e)
}
func (d *DynamicSink) Names() []string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	n := make([]string, 0, len(d.sinks))
	for k := range d.sinks {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}
func (d *DynamicSink) Emit(ctx context.Context, e *model.NormalizedEvent) error {
	d.mu.RLock()
	sinks := make(map[string]OutputSink, len(d.sinks))
	for k, v := range d.sinks {
		sinks[k] = v
	}
	d.mu.RUnlock()
	var errs []string
	for n, s := range sinks {
		if err := s.Emit(ctx, e); err != nil {
			errs = append(errs, n+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("sink delivery: %s", strings.Join(errs, "; "))
	}
	return nil
}
func (d *DynamicSink) Replace(next map[string]OutputSink) error {
	if next == nil {
		next = map[string]OutputSink{}
	}
	d.mu.Lock()
	old := d.sinks
	d.sinks = next
	d.mu.Unlock()
	for n, s := range old {
		if next[n] == s {
			continue
		}
		// The new snapshot is already active. A cleanup error from an old sink
		// cannot safely roll routing back to that now-closed instance.
		_ = s.Close()
	}
	return nil
}
func (d *DynamicSink) Flush() error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var errs []string
	for n, s := range d.sinks {
		if err := s.Flush(); err != nil {
			errs = append(errs, n+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("flush sinks: %s", strings.Join(errs, "; "))
	}
	return nil
}
func (d *DynamicSink) Close() error { return d.Replace(nil) }
