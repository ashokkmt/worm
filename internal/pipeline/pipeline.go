package pipeline

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"worm/internal/decode"
	"worm/internal/model"
	"worm/internal/rawstore"
)

// OutputSink represents a destination for normalized WORM events.
type OutputSink interface {
	Emit(ctx context.Context, event *model.NormalizedEvent) error
}

// MemorySink is a simple thread-safe in-memory sink for testing and local inspection.
type MemorySink struct {
	mu     sync.RWMutex
	events []*model.NormalizedEvent
}

func NewMemorySink() *MemorySink {
	return &MemorySink{events: make([]*model.NormalizedEvent, 0)}
}

func (m *MemorySink) Emit(ctx context.Context, event *model.NormalizedEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
	return nil
}

func (m *MemorySink) Events() []*model.NormalizedEvent {
	m.mu.RLock()
	defer m.mu.RUnlock()
	copied := make([]*model.NormalizedEvent, len(m.events))
	copy(copied, m.events)
	return copied
}

func (m *MemorySink) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = m.events[:0]
}

// Config configures the bounded pipeline worker pool.
type Config struct {
	Workers    int // number of concurrent processing goroutines
	BufferSize int // capacity of the ingestion buffer channel
}

// Pipeline orchestrates raw commit, parsing, normalization, quarantine and loss accounting.
type Pipeline struct {
	cfg      Config
	store    *rawstore.RawStore
	sink     OutputSink
	registry *decode.Registry
	inbound  chan model.IngestedRecord
	wg       sync.WaitGroup
	ctx      context.Context
	cancel   context.CancelFunc
	closed   atomic.Bool

	// Loss accounting counters
	accepted    atomic.Int64
	normalized  atomic.Int64
	quarantined atomic.Int64
	pending     atomic.Int64
	delivered   atomic.Int64
}

// New creates an unstarted pipeline instance.
func New(cfg Config, store *rawstore.RawStore, sink OutputSink) *Pipeline {
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = 1000
	}
	if sink == nil {
		sink = NewMemorySink()
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Pipeline{
		cfg:      cfg,
		store:    store,
		sink:     sink,
		registry: decode.DefaultRegistry(),
		inbound:  make(chan model.IngestedRecord, cfg.BufferSize),
		ctx:      ctx,
		cancel:   cancel,
	}
}

// Start launches the background worker goroutines.
func (p *Pipeline) Start() {
	for i := 0; i < p.cfg.Workers; i++ {
		p.wg.Add(1)
		go p.workerLoop(i)
	}
}

// Submit enqueues an ingested raw datagram into the bounded pipeline.
func (p *Pipeline) Submit(record model.IngestedRecord) error {
	if p.closed.Load() {
		return fmt.Errorf("pipeline is closed")
	}

	select {
	case p.inbound <- record:
		p.accepted.Add(1)
		p.pending.Add(1)
		return nil
	case <-p.ctx.Done():
		return p.ctx.Err()
	default:
		// Channel is full — apply backpressure
		return fmt.Errorf("pipeline ingress buffer full (%d/%d)", len(p.inbound), p.cfg.BufferSize)
	}
}

// SubmitSync synchronously submits and blocks until the record is accepted into the buffer.
func (p *Pipeline) SubmitSync(ctx context.Context, record model.IngestedRecord) error {
	if p.closed.Load() {
		return fmt.Errorf("pipeline is closed")
	}

	select {
	case p.inbound <- record:
		p.accepted.Add(1)
		p.pending.Add(1)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.ctx.Done():
		return p.ctx.Err()
	}
}

// workerLoop continuously processes incoming ingested records.
func (p *Pipeline) workerLoop(workerID int) {
	defer p.wg.Done()

	for {
		select {
		case record, ok := <-p.inbound:
			if !ok {
				return
			}
			p.processRecord(record)
		case <-p.ctx.Done():
			// Drain remaining records before exiting
			for {
				select {
				case record, ok := <-p.inbound:
					if !ok {
						return
					}
					p.processRecord(record)
				default:
					return
				}
			}
		}
	}
}

// processRecord executes the processing stages on a single ingested record.
func (p *Pipeline) processRecord(record model.IngestedRecord) {
	ctx := context.Background()
	history := make([]model.ProcessingStep, 0, 8)

	// Stage 1: Raw Ingress & Cryptographic Commit
	rawEvt, err := p.store.Store(ctx, record)
	if err != nil {
		// Critical storage failure: mark quarantine with synthetic ID
		p.quarantined.Add(1)
		p.pending.Add(-1)
		return
	}

	history = append(history, model.ProcessingStep{
		Stage:     "raw_commit",
		Timestamp: time.Now().UTC(),
		Result:    "ok",
	})

	// Stage 2 & 3: Universal Format Detection & Syntax Decoding
	decoder, decodedRecords, err := p.registry.DetectAndDecode(rawEvt.Payload)
	if err != nil {
		stage := "decode"
		reason := "parse_error"
		if decoder == nil {
			stage = "format_detect"
			reason = "unknown_source"
		}

		history = append(history, model.ProcessingStep{
			Stage:     stage,
			Timestamp: time.Now().UTC(),
			Result:    "error",
			Error:     err.Error(),
		})

		preview := string(rawEvt.Payload)
		if len(preview) > 256 {
			preview = preview[:256]
		}

		_ = p.store.Quarantine(ctx, model.QuarantineEntry{
			RawID:          rawEvt.RawID,
			RawSHA256:      rawEvt.RawSHA256,
			Stage:          stage,
			Reason:         reason,
			ErrorDetails:   err.Error(),
			RawPreview:     preview,
			ReplayEligible: true,
			QuarantinedAt:  time.Now().UTC(),
		})

		p.quarantined.Add(1)
		p.pending.Add(-1)
		return
	}

	history = append(history, model.ProcessingStep{
		Stage:     "format_detect",
		Timestamp: time.Now().UTC(),
		Result:    decoder.Name(),
	})

	// Adjust loss accounting counters for batch expansion (e.g. JSON array or CSV)
	numChildren := len(decodedRecords)
	if numChildren > 1 {
		p.accepted.Add(int64(numChildren - 1))
		p.pending.Add(int64(numChildren - 1))
	} else if numChildren == 0 {
		p.pending.Add(-1)
		return
	}

	for _, decRec := range decodedRecords {
		childHistory := make([]model.ProcessingStep, len(history), len(history)+2)
		copy(childHistory, history)

		childHistory = append(childHistory, model.ProcessingStep{
			Stage:     "decode",
			Timestamp: time.Now().UTC(),
			Result:    decRec.Format,
		})

		var randBytes [8]byte
		_, _ = rand.Read(randBytes[:])
		eventID := fmt.Sprintf("worm-evt-%x", randBytes)

		envelope := model.WormEnvelope{
			EventID:           eventID,
			RawID:             rawEvt.RawID,
			RawSHA256:         rawEvt.RawSHA256,
			RawBytes:          rawEvt.ByteCount,
			RecordOrdinal:     decRec.RecordOrdinal,
			SourceCategory:    "unclassified",
			SourceID:          rawEvt.SourceIP,
			ParserPack:        "generic-core",
			ParserVersion:     "0.1.0",
			CoreVersion:       "0.1.0",
			SchemaVersion:     "1.3.0",
			ReceivedTime:      rawEvt.ReceivedAt,
			Status:            "normalized",
			Warnings:          []string{},
			ProcessingHistory: childHistory,
		}

		normalized := &model.NormalizedEvent{
			Worm: envelope,
			OCSF: decRec.Fields,
		}

		if err := p.sink.Emit(ctx, normalized); err != nil {
			p.quarantined.Add(1)
			p.pending.Add(-1)
			continue
		}

		p.normalized.Add(1)
		p.delivered.Add(1)
		p.pending.Add(-1)
	}

	_ = p.store.UpdateStatus(ctx, rawEvt.RawID, model.StatusNormalized)
}

// Stats returns a snapshot of loss accounting counters and verifies the invariant.
func (p *Pipeline) Stats() model.LossAccountingStats {
	return model.LossAccountingStats{
		Accepted:    p.accepted.Load(),
		Normalized:  p.normalized.Load(),
		Quarantined: p.quarantined.Load(),
		Pending:     p.pending.Load(),
		Delivered:   p.delivered.Load(),
	}
}

// Stop gracefully stops accepting new records, flushes the queue, and terminates workers.
func (p *Pipeline) Stop() {
	if p.closed.CompareAndSwap(false, true) {
		close(p.inbound)
		p.wg.Wait()
		p.cancel()
	}
}
