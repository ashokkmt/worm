package outbox

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestOutbox_Lifecycle(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory sqlite: %v", err)
	}
	defer db.Close()

	ob, err := NewOutbox(db)
	if err != nil {
		t.Fatalf("NewOutbox failed: %v", err)
	}

	ctx := context.Background()

	// 1. Enqueue
	id, err := ob.Enqueue(ctx, "evt-001", "http-siem", []byte(`{"hello":"world"}`))
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
	}

	// 2. Fetch pending
	pending, err := ob.FetchPending(ctx, 10)
	if err != nil {
		t.Fatalf("FetchPending failed: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending record, got %d", len(pending))
	}
	if pending[0].EventID != "evt-001" || pending[0].Destination != "http-siem" {
		t.Errorf("unexpected record contents: %+v", pending[0])
	}

	// 3. Mark failed with backoff
	err = ob.MarkFailed(ctx, id, "connection refused", 5*time.Second, 3)
	if err != nil {
		t.Fatalf("MarkFailed failed: %v", err)
	}

	// Should not be fetched immediately because next_retry_at is 5s in the future
	pendingAfterFail, err := ob.FetchPending(ctx, 10)
	if err != nil {
		t.Fatalf("FetchPending failed: %v", err)
	}
	if len(pendingAfterFail) != 0 {
		t.Fatalf("expected 0 pending due to future retry time, got %d", len(pendingAfterFail))
	}

	// 4. Mark delivered
	err = ob.MarkDelivered(ctx, id)
	if err != nil {
		t.Fatalf("MarkDelivered failed: %v", err)
	}

	delCount, _ := ob.CountByStatus(ctx, "delivered")
	if delCount != 1 {
		t.Errorf("expected 1 delivered, got %d", delCount)
	}

	penCount, _ := ob.CountByStatus(ctx, "pending")
	if penCount != 0 {
		t.Errorf("expected 0 pending, got %d", penCount)
	}
}

func TestOutbox_DistinctAccountingMultiDestination(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	ob, err := NewOutbox(db)
	if err != nil {
		t.Fatalf("NewOutbox: %v", err)
	}
	ctx := context.Background()

	// Enqueue 1 event to 2 sinks: stdout and ndjson
	id1, err := ob.Enqueue(ctx, "evt-multi-01", "cli-stdout", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	id2, err := ob.Enqueue(ctx, "evt-multi-01", "cli-ndjson", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}

	// 1. Both destinations pending
	if pending, _ := ob.CountDistinctPending(ctx); pending != 1 {
		t.Errorf("expected 1 distinct pending event, got %d", pending)
	}
	if delivered, _ := ob.CountDistinctDelivered(ctx); delivered != 0 {
		t.Errorf("expected 0 distinct delivered events, got %d", delivered)
	}

	// 2. Deliver first destination only
	if err := ob.MarkDelivered(ctx, id1); err != nil {
		t.Fatal(err)
	}
	// Still 1 pending because second destination is pending
	if pending, _ := ob.CountDistinctPending(ctx); pending != 1 {
		t.Errorf("expected 1 distinct pending event, got %d", pending)
	}
	if delivered, _ := ob.CountDistinctDelivered(ctx); delivered != 0 {
		t.Errorf("expected 0 distinct delivered events, got %d", delivered)
	}

	// 3. Deliver second destination
	if err := ob.MarkDelivered(ctx, id2); err != nil {
		t.Fatal(err)
	}
	// Row count in outbox is 2, but distinct delivered events is 1
	if rowCount, _ := ob.CountByStatus(ctx, "delivered"); rowCount != 2 {
		t.Errorf("expected 2 delivered rows, got %d", rowCount)
	}
	if delivered, _ := ob.CountDistinctDelivered(ctx); delivered != 1 {
		t.Errorf("expected 1 distinct delivered event, got %d", delivered)
	}
	if pending, _ := ob.CountDistinctPending(ctx); pending != 0 {
		t.Errorf("expected 0 distinct pending events, got %d", pending)
	}
}

