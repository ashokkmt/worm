package rawstore

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"worm/internal/model"
)

func setupTestStore(t *testing.T) *RawStore {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_rawstore.db")
	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("failed to create test rawstore: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	return store
}

func TestStoreAndRetrieveLossless(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	originalBytes := []byte("<14>1 2026-09-20T10:05:23+05:30 pa-fw-01 paloalto - TRAFFIC - vendor_product=PAN-OS action=allow")
	rec := model.IngestedRecord{
		Transport:  "syslog_udp",
		SourceIP:   "10.0.1.50",
		SourcePort: 514,
		RawBytes:   originalBytes,
		ReceivedAt: time.Now().UTC(),
	}

	saved, err := store.Store(ctx, rec)
	if err != nil {
		t.Fatalf("store.Store failed: %v", err)
	}

	if saved.RawID == "" {
		t.Errorf("expected non-empty RawID")
	}
	if saved.ByteCount != len(originalBytes) {
		t.Errorf("expected ByteCount %d, got %d", len(originalBytes), saved.ByteCount)
	}

	retrieved, err := store.Retrieve(ctx, saved.RawID)
	if err != nil {
		t.Fatalf("store.Retrieve failed: %v", err)
	}

	if !bytes.Equal(retrieved.Payload, originalBytes) {
		t.Fatalf("byte mismatch! retrieved: %q, expected: %q", retrieved.Payload, originalBytes)
	}
	if retrieved.RawSHA256 != saved.RawSHA256 {
		t.Errorf("SHA-256 mismatch: got %s, expected %s", retrieved.RawSHA256, saved.RawSHA256)
	}
	if retrieved.Status != model.StatusAccepted {
		t.Errorf("expected status %s, got %s", model.StatusAccepted, retrieved.Status)
	}
}

func TestVerifyIntegrityAndTamperDetection(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	payload := []byte(`{"event": "payment_processed", "amount": 100.50, "currency": "INR"}`)
	rec := model.IngestedRecord{
		Transport:  "http_post",
		SourceIP:   "192.168.1.100",
		RawBytes:   payload,
		ReceivedAt: time.Now().UTC(),
	}

	saved, err := store.Store(ctx, rec)
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// 1. Verify clean record
	valid, computedHash, err := store.Verify(ctx, saved.RawID)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if !valid {
		t.Errorf("expected valid=true for untampered record")
	}
	if computedHash != saved.RawSHA256 {
		t.Errorf("hash mismatch: %s != %s", computedHash, saved.RawSHA256)
	}

	// 2. Tamper record directly in database
	tamperedPayload := []byte(`{"event": "payment_processed", "amount": 999999.00, "currency": "INR"}`)
	_, err = store.db.Exec("UPDATE raw_events SET payload = ? WHERE raw_id = ?", tamperedPayload, saved.RawID)
	if err != nil {
		t.Fatalf("direct DB tamper failed: %v", err)
	}

	// 3. Verify tampered record should fail!
	valid, tamperedHash, err := store.Verify(ctx, saved.RawID)
	if err != nil {
		t.Fatalf("Verify after tamper failed: %v", err)
	}
	if valid {
		t.Errorf("expected valid=false after tampering, but got true!")
	}
	if tamperedHash == saved.RawSHA256 {
		t.Errorf("expected tampered hash %s to differ from original %s", tamperedHash, saved.RawSHA256)
	}
}

func TestQuarantineAndReplayStatus(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	garbage := []byte("INVALID_GARBAGE_LINE_THAT_IS_NOT_A_LOG")
	rec := model.IngestedRecord{
		Transport:  "file_spool",
		SourceIP:   "local",
		RawBytes:   garbage,
		ReceivedAt: time.Now().UTC(),
	}

	saved, err := store.Store(ctx, rec)
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	qEntry := model.QuarantineEntry{
		RawID:          saved.RawID,
		RawSHA256:      saved.RawSHA256,
		Stage:          "format_detect",
		Reason:         "parse_error",
		ErrorDetails:   "unrecognized wire format",
		RawPreview:     string(garbage),
		CandidatePacks: []string{"server-webaccess", "network-device-firewall"},
		ReplayEligible: true,
	}

	if err := store.Quarantine(ctx, qEntry); err != nil {
		t.Fatalf("Quarantine failed: %v", err)
	}

	// Check raw event status updated
	raw, err := store.Retrieve(ctx, saved.RawID)
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}
	if raw.Status != model.StatusQuarantined {
		t.Errorf("expected raw status %s, got %s", model.StatusQuarantined, raw.Status)
	}

	// Check quarantine query
	entries, err := store.GetQuarantined(ctx, 10, 0)
	if err != nil {
		t.Fatalf("GetQuarantined failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 quarantine entry, got %d", len(entries))
	}
	if entries[0].RawID != saved.RawID {
		t.Errorf("expected quarantine RawID %s, got %s", saved.RawID, entries[0].RawID)
	}
	if len(entries[0].CandidatePacks) != 2 {
		t.Errorf("expected 2 candidate packs, got %d", len(entries[0].CandidatePacks))
	}
}
