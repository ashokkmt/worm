package pipeline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

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
		normCount, _ := store.CountNormalized(ctx)
		quarCount, _ := store.CountQuarantine(ctx)
		p.normalized.Store(normCount)
		p.delivered.Store(normCount)
		p.quarantined.Store(quarCount)
		// Rebuild accepted logical records = normalized + quarantined (pending = 0)
		p.accepted.Store(normCount + quarCount)
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

	select {
	case p.inbound <- record:
		p.accepted.Add(1)
		p.pending.Add(1)
		return nil
	case <-p.ctx.Done():
		return p.ctx.Err()
	default:
		return fmt.Errorf("pipeline buffer full (backpressure limit reached)")
	}
}

// SubmitWait enqueues an ingested record, waiting with backpressure until space is available or context expires.
func (p *Pipeline) SubmitWait(ctx context.Context, record model.IngestedRecord) error {
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
				if err := p.SubmitWait(ctx, rec); err != nil {
					if ctx.Err() != nil || p.ctx.Err() != nil {
						return
					}
				}
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

func safePreview(b []byte, maxLen int) string {
	if maxLen <= 0 {
		maxLen = 256
	}
	if len(b) == 0 {
		return ""
	}
	if utf8.Valid(b) {
		runes := []rune(string(b))
		if len(runes) > maxLen {
			runes = runes[:maxLen]
		}
		return string(runes)
	}
	limit := maxLen / 2
	if len(b) < limit {
		limit = len(b)
	}
	return "hex:" + hex.EncodeToString(b[:limit])
}

func (p *Pipeline) processRecord(ctx context.Context, rec model.IngestedRecord) {
	var history []model.ProcessingStep

	// Stage 1: Commit raw bytes to raw store
	rawEvt, err := p.store.Store(ctx, rec)
	if rec.Ack != nil {
		rec.Ack <- err
	}
	if err != nil {
		// BA-002: Raw commit failed: record was never durably committed.
		// Decrement accepted and pending. It is an operational/storage refusal, not a recoverable quarantine.
		p.accepted.Add(-1)
		p.pending.Add(-1)
		return
	}

	history = append(history, model.ProcessingStep{
		Stage:     "raw_commit",
		Timestamp: time.Now().UTC(),
		Result:    "ok",
	})

	preview := safePreview(rawEvt.Payload, 256)

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

		_ = p.store.Quarantine(ctx, model.QuarantineEntry{
			RawID:          rawEvt.RawID,
			RecordOrdinal:  0,
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
		// BA-004: Zero-record batch: quarantine as empty payload so accounting balances
		_ = p.store.Quarantine(ctx, model.QuarantineEntry{
			RawID:          rawEvt.RawID,
			RecordOrdinal:  0,
			RawSHA256:      rawEvt.RawSHA256,
			Stage:          "decode",
			Reason:         "empty_payload",
			ErrorDetails:   "payload produced zero child records",
			RawPreview:     preview,
			ReplayEligible: false,
			QuarantinedAt:  time.Now().UTC(),
		})
		p.quarantined.Add(1)
		p.pending.Add(-1)
		return
	}

	// BA-009: Capture parser pack snapshot ONCE per event
	var snap *packs.Snapshot
	if p.packManager != nil {
		snap = p.packManager.Active()
	}

	childNormalizedCount := 0

	for _, decRec := range decodedRecords {
		childHistory := make([]model.ProcessingStep, len(history), len(history)+4)
		copy(childHistory, history)

		childHistory = append(childHistory, model.ProcessingStep{
			Stage:     "decode",
			Timestamp: time.Now().UTC(),
			Result:    decRec.Format,
		})

		childPreview := safePreview(decRec.RawPayload, 256)

		// Stage 3: Parser Pack Matching
		var pack *packs.ParserPack
		if snap != nil {
			matchedPack, matchErr := snap.Match(decRec)
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
					RecordOrdinal:  decRec.RecordOrdinal,
					RawSHA256:      rawEvt.RawSHA256,
					Stage:          "parser_match",
					Reason:         reason,
					ErrorDetails:   matchErr.Error(),
					RawPreview:     childPreview,
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
					RecordOrdinal:  decRec.RecordOrdinal,
					RawSHA256:      rawEvt.RawSHA256,
					Stage:          "normalize",
					Reason:         "schema_error",
					ErrorDetails:   normErr.Error(),
					RawPreview:     childPreview,
					ReplayEligible: true,
					QuarantinedAt:  time.Now().UTC(),
				})
				p.quarantined.Add(1)
				p.pending.Add(-1)
				continue
			}
		} else {
			// BA-018: Generic fallback when no pack manager is active: validated pass-through
			var randBytes [4]byte
			_, _ = rand.Read(randBytes[:])
			eventID := fmt.Sprintf("worm-evt-%s-%x", time.Now().Format("20060102"), randBytes)

			envelope := model.WormEnvelope{
				EventID:           eventID,
				RawID:             rawEvt.RawID,
				RawSHA256:         rawEvt.RawSHA256,
				RawBytes:          rawEvt.ByteCount,
				RecordOrdinal:     decRec.RecordOrdinal,
				SourceCategory:    "other",
				SourceID:          rawEvt.SourceIP,
				ParserPack:        "generic-core",
				ParserVersion:     "0.1.0",
				CoreVersion:       "0.1.0",
				SchemaVersion:     "1.3.0",
				ReceivedTime:      rawEvt.ReceivedAt,
				Status:            "normalized",
				Warnings:          []string{"unclassified generic pass-through"},
				ProcessingHistory: childHistory,
			}

			ocsfPayload := make(map[string]any)
			for k, v := range decRec.Fields {
				ocsfPayload[k] = v
			}
			if _, ok := ocsfPayload["time"]; !ok {
				ocsfPayload["time"] = rawEvt.ReceivedAt.UnixMilli()
			}
			if _, ok := ocsfPayload["category_name"]; !ok {
				ocsfPayload["category_name"] = "Other"
			}
			if _, ok := ocsfPayload["class_name"]; !ok {
				ocsfPayload["class_name"] = "Base Event"
			}

			normalized = &model.NormalizedEvent{
				Worm: envelope,
				OCSF: ocsfPayload,
			}
		}

		if valErr := p.validator.Validate(normalized); valErr != nil {
			_ = p.store.Quarantine(ctx, model.QuarantineEntry{
				RawID:          rawEvt.RawID,
				RecordOrdinal:  decRec.RecordOrdinal,
				RawSHA256:      rawEvt.RawSHA256,
				Stage:          "validate",
				Reason:         "schema_error",
				ErrorDetails:   valErr.Error(),
				RawPreview:     childPreview,
				ReplayEligible: true,
				QuarantinedAt:  time.Now().UTC(),
			})
			p.quarantined.Add(1)
			p.pending.Add(-1)
			continue
		}

		// BA-003: Store normalized record and check error
		if p.store != nil {
			if storeErr := p.store.StoreNormalized(ctx, normalized); storeErr != nil {
				_ = p.store.Quarantine(ctx, model.QuarantineEntry{
					RawID:          rawEvt.RawID,
					RecordOrdinal:  decRec.RecordOrdinal,
					RawSHA256:      rawEvt.RawSHA256,
					Stage:          "storage",
					Reason:         "store_error",
					ErrorDetails:   storeErr.Error(),
					RawPreview:     childPreview,
					ReplayEligible: true,
					QuarantinedAt:  time.Now().UTC(),
				})
				p.quarantined.Add(1)
				p.pending.Add(-1)
				continue
			}
		}

		// Emit to sink and check error
		if emitErr := p.sink.Emit(ctx, normalized); emitErr != nil {
			_ = p.store.Quarantine(ctx, model.QuarantineEntry{
				RawID:          rawEvt.RawID,
				RecordOrdinal:  decRec.RecordOrdinal,
				RawSHA256:      rawEvt.RawSHA256,
				Stage:          "output",
				Reason:         "sink_error",
				ErrorDetails:   emitErr.Error(),
				RawPreview:     childPreview,
				ReplayEligible: true,
				QuarantinedAt:  time.Now().UTC(),
			})
			p.quarantined.Add(1)
			p.pending.Add(-1)
			continue
		}

		childNormalizedCount++
		p.normalized.Add(1)
		p.delivered.Add(1)
		p.pending.Add(-1)
	}

	// BA-004: Derive parent status based on child outcomes
	finalStatus := model.StatusNormalized
	if childNormalizedCount == 0 {
		finalStatus = model.StatusQuarantined
	}
	if p.store != nil {
		_ = p.store.UpdateStatus(ctx, rawEvt.RawID, finalStatus)
	}
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

	// BA-005: Select the specific child record matching entry.RecordOrdinal
	var decRec *model.DecodedRecord
	for _, dr := range decodedRecords {
		if dr.RecordOrdinal == entry.RecordOrdinal {
			decRec = dr
			break
		}
	}
	if decRec == nil {
		decRec = decodedRecords[0]
	}

	// BA-009: Capture snapshot once
	if p.packManager == nil {
		return nil, fmt.Errorf("no active parser pack snapshot available for replay")
	}
	snap := p.packManager.Active()
	if snap == nil {
		return nil, fmt.Errorf("no active parser pack snapshot available for replay")
	}

	pack, err := snap.Match(decRec)
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

	// BA-005: Emit to sink first. If delivery fails, do NOT close quarantine or increment delivered!
	if err := p.sink.Emit(ctx, norm); err != nil {
		return nil, fmt.Errorf("replay delivery to sink failed: %w", err)
	}

	// Persist normalized projection and remove from quarantine
	if p.store != nil {
		if err := p.store.StoreNormalized(ctx, norm); err != nil {
			return nil, fmt.Errorf("failed to store normalized replayed event: %w", err)
		}
		if err := p.store.DeleteQuarantine(ctx, qID); err != nil {
			return nil, fmt.Errorf("failed to delete resolved quarantine entry: %w", err)
		}
		_ = p.store.UpdateStatus(ctx, rawEvt.RawID, model.StatusNormalized)
	}

	// Update pipeline loss accounting counters
	if p.quarantined.Load() > 0 {
		p.quarantined.Add(-1)
	}
	p.normalized.Add(1)
	p.delivered.Add(1)

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
