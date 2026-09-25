package ingest

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"worm/internal/model"
)

// IngestAdapter is a lifecycle-managed ingestion transport.
type IngestAdapter interface {
	Name() string
	Start(context.Context, chan<- model.IngestedRecord) error
	Stop() error
}

// Manager keeps one stable output channel while atomically replacing live adapters.
type Manager struct {
	mu          sync.Mutex
	adapters    []IngestAdapter
	adapterErrs map[string]error
	out         chan model.IngestedRecord
	parent      context.Context
	runCtx      context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	running     bool
	stopped     bool
}

func NewManager(bufferSize int) *Manager {
	if bufferSize <= 0 {
		bufferSize = 1000
	}
	return &Manager{adapterErrs: map[string]error{}, out: make(chan model.IngestedRecord, bufferSize)}
}
func (m *Manager) Register(a IngestAdapter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		panic("register after start; use Reconcile")
	}
	m.adapters = append(m.adapters, a)
}
func (m *Manager) Channel() <-chan model.IngestedRecord { return m.out }
func (m *Manager) Inbound() chan<- model.IngestedRecord { return m.out }
func (m *Manager) AdapterErrors() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := map[string]string{}
	for k, v := range m.adapterErrs {
		if v != nil {
			r[k] = v.Error()
		}
	}
	return r
}
func (m *Manager) AdapterNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := make([]string, 0, len(m.adapters))
	for _, a := range m.adapters {
		n = append(n, a.Name())
	}
	sort.Strings(n)
	return n
}

func validateAdapters(as []IngestAdapter) error {
	seen := map[string]bool{}
	for _, a := range as {
		if a == nil || a.Name() == "" {
			return fmt.Errorf("adapter name is required")
		}
		if seen[a.Name()] {
			return fmt.Errorf("duplicate adapter %q", a.Name())
		}
		seen[a.Name()] = true
	}
	return nil
}
func (m *Manager) launchLocked() {
	for _, a := range m.adapters {
		ad := a
		ctx := m.runCtx
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			if err := ad.Start(ctx, m.out); err != nil && ctx.Err() == nil {
				m.mu.Lock()
				m.adapterErrs[ad.Name()] = err
				m.mu.Unlock()
			}
		}()
	}
}
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return fmt.Errorf("ingest manager is already running")
	}
	if m.stopped {
		m.mu.Unlock()
		return fmt.Errorf("ingest manager is stopped")
	}
	if err := validateAdapters(m.adapters); err != nil {
		m.mu.Unlock()
		return err
	}
	m.parent = ctx
	m.runCtx, m.cancel = context.WithCancel(ctx)
	m.running = true
	m.launchLocked()
	m.mu.Unlock()
	return m.probeStartup()
}

func (m *Manager) probeStartup() error {
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-m.runCtx.Done():
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, err := range m.adapterErrs {
		if err != nil {
			return fmt.Errorf("adapter %s failed startup: %w", name, err)
		}
	}
	return nil
}

// Reconcile validates the complete replacement before stopping the old snapshot.
func (m *Manager) Reconcile(adapters []IngestAdapter) error {
	if err := validateAdapters(adapters); err != nil {
		return err
	}
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return fmt.Errorf("ingest manager is stopped")
	}
	running := m.running
	cancel := m.cancel
	old := append([]IngestAdapter(nil), m.adapters...)
	parent := m.parent
	m.running = false
	m.mu.Unlock()
	if running {
		cancel()
		for _, a := range old {
			_ = a.Stop()
		}
		m.wg.Wait()
	}
	m.mu.Lock()
	m.adapters = append([]IngestAdapter(nil), adapters...)
	m.adapterErrs = map[string]error{}
	if running {
		m.runCtx, m.cancel = context.WithCancel(parent)
		m.running = true
		m.launchLocked()
	}
	m.mu.Unlock()
	if running {
		return m.probeStartup()
	}
	return nil
}
func (m *Manager) Stop() error {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return nil
	}
	m.stopped = true
	running := m.running
	cancel := m.cancel
	old := append([]IngestAdapter(nil), m.adapters...)
	m.running = false
	m.mu.Unlock()
	if running {
		cancel()
	}
	var first error
	for _, a := range old {
		if err := a.Stop(); err != nil && first == nil {
			first = err
		}
	}
	m.wg.Wait()
	close(m.out)
	return first
}
