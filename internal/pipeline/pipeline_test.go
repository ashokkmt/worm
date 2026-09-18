package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"worm/internal/model"
	"worm/internal/rawstore"
)

func setupTestPipeline(t *testing.T, workers int) (*Pipeline, *rawstore.RawStore, *MemorySink) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_pipeline.db")
	store, err := rawstore.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create rawstore: %v", err)
	}

	sink := NewMemorySink()
	cfg := Config{
		Workers:    workers,
		BufferSize: 200,
	}
	p := New(cfg, store, sink)
	p.Start()

	t.Cleanup(func() {
		p.Stop()
		_ = store.Close()
	})

	return p, store, sink
}

func TestPipelineNormalFlowAndLossAccounting(t *testing.T) {
	p, store, sink := setupTestPipeline(t, 4)
	ctx := context.Background()

	const eventCount = 50
	for i := 0; i < eventCount; i++ {
		rec := model.IngestedRecord{
			Transport:  "syslog_udp",
			SourceIP:   fmt.Sprintf("10.0.1.%d", i+1),
			SourcePort: 514,
			RawBytes:   []byte(fmt.Sprintf("<14>1 2026-09-20T10:00:%02dZ host-%d app - - test message %d", i, i, i)),
			ReceivedAt: time.Now().UTC(),
		}
		if err := p.SubmitSync(ctx, rec); err != nil {
			t.Fatalf("SubmitSync failed at %d: %v", i, err)
		}
	}

	// Stop pipeline to flush all items and wait for workers
	p.Stop()

	stats := p.Stats()
	valid, reason := stats.VerifyInvariant()
	if !valid {
		t.Fatalf("loss accounting invariant failed: %s", reason)
	}

	if stats.Accepted != eventCount {
		t.Errorf("expected %d accepted, got %d", eventCount, stats.Accepted)
	}
	if stats.Normalized != eventCount {
		t.Errorf("expected %d normalized, got %d", eventCount, stats.Normalized)
	}
	if stats.Quarantined != 0 {
		t.Errorf("expected 0 quarantined, got %d", stats.Quarantined)
	}
	if stats.Pending != 0 {
		t.Errorf("expected 0 pending, got %d", stats.Pending)
	}

	// Check sink events
	emitted := sink.Events()
	if len(emitted) != eventCount {
		t.Fatalf("expected %d emitted events, got %d", eventCount, len(emitted))
	}

	// Verify cryptographic lineage on the first event
	first := emitted[0]
	if first.Worm.EventID == "" {
		t.Errorf("expected non-empty EventID")
	}
	if first.Worm.RawID == "" {
		t.Errorf("expected non-empty RawID")
	}

	// Verify exact raw bytes retrieval from store
	rawRecord, err := store.Retrieve(ctx, first.Worm.RawID)
	if err != nil {
		t.Fatalf("failed to retrieve raw record %s: %v", first.Worm.RawID, err)
	}
	if rawRecord.RawSHA256 != first.Worm.RawSHA256 {
		t.Errorf("SHA-256 mismatch between raw store and WORM envelope")
	}

	// Cryptographic verification
	match, _, err := store.Verify(ctx, first.Worm.RawID)
	if err != nil || !match {
		t.Fatalf("cryptographic verification failed for event %s", first.Worm.EventID)
	}
}

func TestPipelineQuarantineAndMixedWorkload(t *testing.T) {
	p, store, sink := setupTestPipeline(t, 2)
	ctx := context.Background()

	const validCount = 15
	const invalidCount = 10

	for i := 0; i < validCount; i++ {
		rec := model.IngestedRecord{
			Transport:  "http_post",
			SourceIP:   "192.168.1.50",
			RawBytes:   []byte(fmt.Sprintf(`{"valid": true, "index": %d}`, i)),
			ReceivedAt: time.Now().UTC(),
		}
		_ = p.SubmitSync(ctx, rec)
	}

	for i := 0; i < invalidCount; i++ {
		rec := model.IngestedRecord{
			Transport:  "syslog_udp",
			SourceIP:   "192.168.1.99",
			RawBytes:   []byte("INVALID_GARBAGE"),
			ReceivedAt: time.Now().UTC(),
		}
		_ = p.SubmitSync(ctx, rec)
	}

	p.Stop()

	stats := p.Stats()
	valid, reason := stats.VerifyInvariant()
	if !valid {
		t.Fatalf("invariant violated: %s", reason)
	}

	if stats.Accepted != validCount+invalidCount {
		t.Errorf("expected %d accepted, got %d", validCount+invalidCount, stats.Accepted)
	}
	if stats.Normalized != validCount {
		t.Errorf("expected %d normalized, got %d", validCount, stats.Normalized)
	}
	if stats.Quarantined != invalidCount {
		t.Errorf("expected %d quarantined, got %d", invalidCount, stats.Quarantined)
	}
	if stats.Pending != 0 {
		t.Errorf("expected 0 pending, got %d", stats.Pending)
	}

	// Verify sink received only valid events
	if len(sink.Events()) != validCount {
		t.Errorf("expected %d events in sink, got %d", validCount, len(sink.Events()))
	}

	// Verify quarantined records in SQLite
	quarantined, err := store.GetQuarantined(ctx, 50, 0)
	if err != nil {
		t.Fatalf("failed to query quarantine entries: %v", err)
	}
	if len(quarantined) != invalidCount {
		t.Errorf("expected %d quarantine records in DB, got %d", invalidCount, len(quarantined))
	}
}

func TestPipelineBatchExpansion_JSON_and_CSV(t *testing.T) {
	p, store, sink := setupTestPipeline(t, 2)
	ctx := context.Background()

	// 1. JSON Array with 3 elements
	jsonArray := []byte(`[
		{"id": "rec-1", "user": "alice"},
		{"id": "rec-2", "user": "bob"},
		{"id": "rec-3", "user": "charlie"}
	]`)
	_ = p.SubmitSync(ctx, model.IngestedRecord{
		Transport:  "http_post",
		SourceIP:   "10.0.1.10",
		RawBytes:   jsonArray,
		ReceivedAt: time.Now().UTC(),
	})

	// 2. CSV with 1 header + 3 rows
	csvData := []byte("col_a,col_b\nval1,val2\nval3,val4\nval5,val6\n")
	_ = p.SubmitSync(ctx, model.IngestedRecord{
		Transport:  "file_spool",
		SourceIP:   "local",
		RawBytes:   csvData,
		ReceivedAt: time.Now().UTC(),
	})

	// 3. Single RFC 5424 Syslog
	syslogMsg := []byte("<14>1 2026-09-20T10:05:23+05:30 pa-fw-01 paloalto - TRAFFIC - vendor_product=PAN-OS action=allow")
	_ = p.SubmitSync(ctx, model.IngestedRecord{
		Transport:  "syslog_udp",
		SourceIP:   "10.0.1.50",
		RawBytes:   syslogMsg,
		ReceivedAt: time.Now().UTC(),
	})

	p.Stop()

	stats := p.Stats()
	valid, reason := stats.VerifyInvariant()
	if !valid {
		t.Fatalf("loss accounting invariant failed: %s", reason)
	}

	// 3 (JSON array items) + 3 (CSV rows) + 1 (Syslog) = 7 logical events
	const expectedEvents = 7
	if stats.Accepted != expectedEvents {
		t.Errorf("expected %d accepted events, got %d", expectedEvents, stats.Accepted)
	}
	if stats.Normalized != expectedEvents {
		t.Errorf("expected %d normalized events, got %d", expectedEvents, stats.Normalized)
	}
	if stats.Pending != 0 {
		t.Errorf("expected 0 pending, got %d", stats.Pending)
	}

	emitted := sink.Events()
	if len(emitted) != expectedEvents {
		t.Fatalf("expected %d emitted events, got %d", expectedEvents, len(emitted))
	}

	// Verify child record ordinals
	// JSON array records should have ordinals 0, 1, 2 sharing the same RawID
	jsonChild0 := emitted[0]
	jsonChild1 := emitted[1]
	jsonChild2 := emitted[2]

	if jsonChild0.Worm.RawID != jsonChild1.Worm.RawID || jsonChild1.Worm.RawID != jsonChild2.Worm.RawID {
		t.Errorf("expected JSON array children to share the same parent RawID")
	}
	if jsonChild0.Worm.RecordOrdinal != 0 || jsonChild1.Worm.RecordOrdinal != 1 || jsonChild2.Worm.RecordOrdinal != 2 {
		t.Errorf("unexpected JSON child ordinals: %d, %d, %d",
			jsonChild0.Worm.RecordOrdinal, jsonChild1.Worm.RecordOrdinal, jsonChild2.Worm.RecordOrdinal)
	}

	// Verify parent raw byte store integrity for the JSON array
	match, _, err := store.Verify(ctx, jsonChild0.Worm.RawID)
	if err != nil || !match {
		t.Fatalf("cryptographic verification failed for batch parent raw record: %v", err)
	}
}

