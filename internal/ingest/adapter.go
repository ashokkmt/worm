package ingest

import (
	"context"
	"fmt"
	"sync"
	"worm/internal/model"
)

// IngestAdapter represents an ingestion transport listener (Syslog UDP/TCP, HTTP, File).
type IngestAdapter interface {
	Name() string
	Start(ctx context.Context, out chan<- model.IngestedRecord) error
	Stop() error
}

// Manager coordinates multiple ingestion adapters, routing records to a single channel.
type Manager struct {
	mu       sync.Mutex
	adapters    []IngestAdapter
	adapterErrs map[string]error
	out         chan model.IngestedRecord
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	running     bool
}

// NewManager creates a new adapter manager with the given output channel capacity.
func NewManager(bufferSize int) *Manager {
	if bufferSize <= 0 {
		bufferSize = 1000
	}
	return &Manager{
		adapters:    make([]IngestAdapter, 0),
		adapterErrs: make(map[string]error),
		out:         make(chan model.IngestedRecord, bufferSize),
	}
}

// Register adds an adapter to the manager. Must be called before Start.
func (m *Manager) Register(adapter IngestAdapter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.adapters = append(m.adapters, adapter)
}

// Channel returns the channel receiving all ingested records.
func (m *Manager) Channel() <-chan model.IngestedRecord {
	return m.out
}

// Inbound returns the send channel for injecting records directly into the manager.
func (m *Manager) Inbound() chan<- model.IngestedRecord {
	return m.out
}

// AdapterErrors returns a snapshot of errors recorded by adapters.
func (m *Manager) AdapterErrors() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := make(map[string]string, len(m.adapterErrs))
	for k, v := range m.adapterErrs {
		if v != nil {
			res[k] = v.Error()
		}
	}
	return res
}

// Start launches all registered adapters.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		return fmt.Errorf("ingest manager is already running")
	}

	m.ctx, m.cancel = context.WithCancel(ctx)
	m.running = true

	for _, adapter := range m.adapters {
		ad := adapter
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			if err := ad.Start(m.ctx, m.out); err != nil && m.ctx.Err() == nil {
				m.mu.Lock()
				m.adapterErrs[ad.Name()] = err
				m.mu.Unlock()
			}
		}()
	}

	return nil
}

// Stop gracefully stops all registered adapters and closes the output channel.
func (m *Manager) Stop() error {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return nil
	}
	m.running = false
	m.cancel()
	m.mu.Unlock()

	var firstErr error
	for _, adapter := range m.adapters {
		if err := adapter.Stop(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	m.wg.Wait()
	close(m.out)
	return firstErr
}
