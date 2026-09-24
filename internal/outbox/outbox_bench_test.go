package outbox

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func setupBenchDB(b *testing.B) (*sql.DB, *Outbox) {
	dir := b.TempDir()
	dbPath := filepath.Join(dir, "bench_outbox.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		b.Fatalf("failed to open sqlite: %v", err)
	}

	ob, err := NewOutbox(db)
	if err != nil {
		b.Fatalf("failed to create outbox: %v", err)
	}
	return db, ob
}

func BenchmarkOutbox_Enqueue(b *testing.B) {
	db, ob := setupBenchDB(b)
	defer db.Close()

	ctx := context.Background()
	payload := []byte(`{"event_id":"evt-bench-1","timestamp":"2026-09-24T12:00:00Z","src_ip":"192.168.1.1","dst_ip":"10.0.0.1","action":"allow"}`)

	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		evtID := fmt.Sprintf("evt-bench-%d", i)
		_, err := ob.Enqueue(ctx, evtID, "siem-events", payload)
		if err != nil {
			b.Fatalf("enqueue failed: %v", err)
		}
	}
}

func BenchmarkOutbox_FetchPending(b *testing.B) {
	db, ob := setupBenchDB(b)
	defer db.Close()

	ctx := context.Background()
	payload := []byte(`{"event_id":"evt-bench-seed","action":"allow"}`)

	// Seed 1000 records
	for i := 0; i < 1000; i++ {
		_, _ = ob.Enqueue(ctx, fmt.Sprintf("evt-seed-%d", i), "siem-events", payload)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		entries, err := ob.FetchPending(ctx, 100)
		if err != nil {
			b.Fatalf("fetch failed: %v", err)
		}
		if len(entries) == 0 {
			b.Fatal("expected pending entries")
		}
	}
}
