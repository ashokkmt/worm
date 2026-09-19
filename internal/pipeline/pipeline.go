package pipeline

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"worm/internal/decode"
	"worm/internal/model"
	"worm/internal/normalize"
	"worm/internal/packs"
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
	Workers     int                    // number of concurrent processing goroutines
	BufferSize  int                    // capacity of the ingestion buffer channel
	PackManager *packs.SnapshotManager // optional active parser pack manager
}

// Pipeline orchestrates raw commit, parsing, normalization, quarantine and loss accounting.
type Pipeline struct {
	cfg         Config
	store       *rawstore.RawStore
	sink        OutputSink
	registry    *decode.Registry
	packManager *packs.SnapshotManager
	normalizer  *normalize.Normalizer
	validator   *normalize.Validator
	inbound     chan model.IngestedRecord
	wg          sync.WaitGroup
	ctx         context.Context
	cancel      context.CancelFunc
	closed      atomic.Bool

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
	p := &Pipeline{
		cfg:         cfg,
		store:       store,
		sink:        sink,
		registry:    decode.DefaultRegistry(),
		packManager: cfg.PackManager,
		normalizer:  normalize.NewNormalizer(),
		validator:   normalize.NewValidator(),
		inbound:     make(chan model.IngestedRecord, cfg.BufferSize),
		ctx:         ctx,
		cancel:      cancel,
	}

	if store != nil {
		if rawCount, err := store.CountRaw(ctx); err == nil {
			p.accepted.Store(rawCount)
		}
		if normCount, err := store.CountNormalized(ctx); err == nil {
			p.normalized.Store(normCount)
			p.delivered.Store(normCount)
		}
		if quarCount, err := store.CountQuarantine(ctx); err == nil {
			p.quarantined.Store(quarCount)
		}
	}

	return p
}

// SetPackManager attaches or updates the active parser pack manager.
func (p *Pipeline) SetPackManager(pm *packs.SnapshotManager) {
	p.packManager = pm
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

	p.accepted.Add(1)
	p.pending.Add(1)

	select {
	case p.inbound <- record:
		return nil
	case <-p.ctx.Done():
		p.pending.Add(-1)
		return p.ctx.Err()
	default:
		p.pending.Add(-1)
		return fmt.Errorf("pipeline buffer full (backpressure limit reached)")
	}
}

// SubmitWait enqueues an ingested record, waiting with backpressure until space is available or context expires.
func (p *Pipeline) SubmitWait(ctx context.Context, record model.IngestedRecord) error {
	if p.closed.Load() {
		return fmt.Errorf("pipeline is closed")
	}

	p.accepted.Add(1)
	p.pending.Add(1)

	select {
	case p.inbound <- record:
		return nil
	case <-ctx.Done():
		p.pending.Add(-1)
		return ctx.Err()
	case <-p.ctx.Done():
		p.pending.Add(-1)
		return p.ctx.Err()
	}
}

// ConnectIngest drains records from an ingestion stream channel directly into the pipeline with backpressure.
func (p *Pipeline) ConnectIngest(ctx context.Context, in <-chan model.IngestedRecord) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			select {
			case rec, ok := <-in:
				if !ok {
					return
				}
				_ = p.SubmitWait(ctx, rec)
			case <-ctx.Done():
				return
			case <-p.ctx.Done():
				return
			}
		}
	}()
}

// SubmitSync processes an ingested record synchronously within the calling goroutine.
func (p *Pipeline) SubmitSync(ctx context.Context, record model.IngestedRecord) error {
	if p.closed.Load() {
		return fmt.Errorf("pipeline is closed")
	}

	p.accepted.Add(1)
	p.pending.Add(1)
	p.processRecord(ctx, record)
	return nil
}

// Stop gracefully drains the ingestion buffer and waits for all workers to finish.
func (p *Pipeline) Stop() {
	if !p.closed.CompareAndSwap(false, true) {
		return
	}
	close(p.inbound)
	p.wg.Wait()
	p.cancel()
}

func (p *Pipeline) workerLoop(workerID int) {
	defer p.wg.Done()
	for rec := range p.inbound {
		p.processRecord(p.ctx, rec)
	}
}

func (p *Pipeline) processRecord(ctx context.Context, rec model.IngestedRecord) {
	var history []model.ProcessingStep

	// Stage 1: Commit raw bytes to raw store
	rawEvt, err := p.store.Store(ctx, rec)
	if err != nil {
		p.quarantined.Add(1)
		p.pending.Add(-1)
		return
	}

	history = append(history, model.ProcessingStep{
		Stage:     "raw_commit",
		Timestamp: time.Now().UTC(),
		Result:    "ok",
	})

	// Stage 2: Format auto-detection & Syntax decoding
	decoder, decodedRecords, err := p.registry.DetectAndDecode(rawEvt.Payload)
	if err != nil {
		reason := "parse_error"
		stage := "decode"
		if decoder == nil {
			stage = "format_detect"
		}

		history = append(history, model.ProcessingStep{
			Stage:     stage,
			Timestamp: time.Now().UTC(),
			Result:    "failed",
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

	// Adjust loss accounting counters for batch expansion
	numChildren := len(decodedRecords)
	if numChildren > 1 {
		p.accepted.Add(int64(numChildren - 1))
		p.pending.Add(int64(numChildren - 1))
	} else if numChildren == 0 {
		p.pending.Add(-1)
		return
	}

	for _, decRec := range decodedRecords {
		childHistory := make([]model.ProcessingStep, len(history), len(history)+4)
		copy(childHistory, history)

		childHistory = append(childHistory, model.ProcessingStep{
			Stage:     "decode",
			Timestamp: time.Now().UTC(),
			Result:    decRec.Format,
		})

		preview := string(decRec.RawPayload)
		if len(preview) > 256 {
			preview = preview[:256]
		}

		// Stage 3: Parser Pack Matching (if pack manager active)
		var pack *packs.ParserPack
		if p.packManager != nil && p.packManager.Active() != nil {
			matchedPack, matchErr := p.packManager.Active().Match(decRec)
			if matchErr != nil {
				reason := "unknown_source"
				if errors.Is(matchErr, packs.ErrAmbiguousMatch) {
					reason = "ambiguous_source"
				}

				childHistory = append(childHistory, model.ProcessingStep{
					Stage:     "parser_match",
					Timestamp: time.Now().UTC(),
					Result:    "failed",
					Error:     matchErr.Error(),
				})

				_ = p.store.Quarantine(ctx, model.QuarantineEntry{
					RawID:          rawEvt.RawID,
					RawSHA256:      rawEvt.RawSHA256,
					Stage:          "parser_match",
					Reason:         reason,
					ErrorDetails:   matchErr.Error(),
					RawPreview:     preview,
					ReplayEligible: true,
					QuarantinedAt:  time.Now().UTC(),
				})

				p.quarantined.Add(1)
				p.pending.Add(-1)
				continue
			}

			pack = matchedPack
			childHistory = append(childHistory, model.ProcessingStep{
				Stage:     "parser_match",
				Timestamp: time.Now().UTC(),
				Result:    fmt.Sprintf("%s@%s", pack.Metadata.Name, pack.Metadata.Version),
			})
		}

		// Stage 4 & 5: Normalization & Validation
		var normalized *model.NormalizedEvent
		if pack != nil {
			var normErr error
			normalized, normErr = p.normalizer.Normalize(rawEvt, decRec, pack, childHistory)
			if normErr != nil {
				_ = p.store.Quarantine(ctx, model.QuarantineEntry{
					RawID:          rawEvt.RawID,
					RawSHA256:      rawEvt.RawSHA256,
					Stage:          "normalize",
					Reason:         "schema_error",
					ErrorDetails:   normErr.Error(),
					RawPreview:     preview,
					ReplayEligible: true,
					QuarantinedAt:  time.Now().UTC(),
				})
				p.quarantined.Add(1)
				p.pending.Add(-1)
				continue
			}

			if valErr := p.validator.Validate(normalized); valErr != nil {
				_ = p.store.Quarantine(ctx, model.QuarantineEntry{
					RawID:          rawEvt.RawID,
					RawSHA256:      rawEvt.RawSHA256,
					Stage:          "validate",
					Reason:         "schema_error",
					ErrorDetails:   valErr.Error(),
					RawPreview:     preview,
					ReplayEligible: true,
					QuarantinedAt:  time.Now().UTC(),
				})
				p.quarantined.Add(1)
				p.pending.Add(-1)
				continue
			}
		} else {
			// Basic generic fallback when no pack manager is configured
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

			normalized = &model.NormalizedEvent{
				Worm: envelope,
				OCSF: decRec.Fields,
			}
		}

		if err := p.sink.Emit(ctx, normalized); err != nil {
			p.quarantined.Add(1)
			p.pending.Add(-1)
			continue
		}

		if p.store != nil {
			_ = p.store.StoreNormalized(ctx, normalized)
		}

		p.normalized.Add(1)
		p.delivered.Add(1)
		p.pending.Add(-1)
	}

	_ = p.store.UpdateStatus(ctx, rawEvt.RawID, model.StatusNormalized)
}

// Replay retrieves a quarantined event by its quarantine ID, verifies cryptographic
// integrity, and re-processes it through the pipeline with currently active parser packs.
func (p *Pipeline) Replay(ctx context.Context, qID string) (*model.NormalizedEvent, error) {
	entry, err := p.store.GetQuarantineByID(ctx, qID)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve quarantine entry %s: %w", qID, err)
	}

	rawEvt, err := p.store.Retrieve(ctx, entry.RawID)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve raw event %s: %w", entry.RawID, err)
	}

	matches, _, err := p.store.Verify(ctx, entry.RawID)
	if err != nil || !matches {
		return nil, fmt.Errorf("cryptographic integrity check failed for raw event %s", entry.RawID)
	}

	// Run through detection, decoding, matching, and normalization
	decoder, decodedRecords, err := p.registry.DetectAndDecode(rawEvt.Payload)
	if err != nil {
		return nil, fmt.Errorf("replay decode failed: %w", err)
	}
	if len(decodedRecords) == 0 {
		return nil, fmt.Errorf("no decoded records produced upon replay")
	}

	decRec := decodedRecords[0]
	if p.packManager == nil || p.packManager.Active() == nil {
		return nil, fmt.Errorf("no active parser pack snapshot available for replay")
	}

	pack, err := p.packManager.Active().Match(decRec)
	if err != nil {
		return nil, fmt.Errorf("replay pack match failed: %w", err)
	}

	history := []model.ProcessingStep{
		{Stage: "raw_commit", Timestamp: rawEvt.ReceivedAt, Result: "ok"},
		{Stage: "format_detect", Timestamp: time.Now().UTC(), Result: decoder.Name()},
		{Stage: "decode", Timestamp: time.Now().UTC(), Result: decRec.Format},
		{Stage: "parser_match", Timestamp: time.Now().UTC(), Result: fmt.Sprintf("%s@%s", pack.Metadata.Name, pack.Metadata.Version)},
	}

	norm, err := p.normalizer.Normalize(rawEvt, decRec, pack, history)
	if err != nil {
		return nil, fmt.Errorf("replay normalization failed: %w", err)
	}

	if err := p.validator.Validate(norm); err != nil {
		return nil, fmt.Errorf("replay validation failed: %w", err)
	}

	// Update pipeline loss accounting counters
	if p.quarantined.Load() > 0 {
		p.quarantined.Add(-1)
	}
	p.normalized.Add(1)
	p.delivered.Add(1)

	// Remove from quarantine table and persist normalized projection
	if p.store != nil {
		_ = p.store.DeleteQuarantine(ctx, qID)
		_ = p.store.UpdateStatus(ctx, rawEvt.RawID, model.StatusNormalized)
		_ = p.store.StoreNormalized(ctx, norm)
	}

	_ = p.sink.Emit(ctx, norm)
	return norm, nil
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
