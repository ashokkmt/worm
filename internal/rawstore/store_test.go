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

func TestVerifyNormalized_TamperDetection(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	// 1. Commit raw event
	raw, err := store.Store(ctx, model.IngestedRecord{
		Transport:  "syslog_udp",
		SourceIP:   "10.0.1.50",
		RawBytes:   []byte("<14>Sep 19 10:00:00 fw01: connection allowed"),
		ReceivedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Store raw failed: %v", err)
	}

	// 2. Commit normalized event
	evt := &model.NormalizedEvent{
		Worm: model.WormEnvelope{
			EventID:        "worm-evt-test-999",
			RawID:          raw.RawID,
			RawSHA256:      raw.RawSHA256,
			SourceCategory: "network_device",
			SourceID:       "fw01",
			ReceivedTime:   time.Now().UTC(),
			Status:         "normalized",
		},
		OCSF: map[string]any{
			"activity_name": "Traffic",
			"severity_id":   1,
			"message":       "connection allowed",
		},
	}

	if err := store.StoreNormalized(ctx, evt); err != nil {
		t.Fatalf("StoreNormalized failed: %v", err)
	}

	// 3. Verify clean event -> MUST PASS
	rep, err := store.VerifyNormalized(ctx, "worm-evt-test-999")
	if err != nil {
		t.Fatalf("VerifyNormalized error: %v", err)
	}
	if !rep.Passed || rep.Status != "PASSED" {
		t.Fatalf("expected clean event to PASS, got %+v", rep)
	}

	// 4. Tamper 1: Modify message column (e.g. user adding 'hello' in SQLite Web GUI)
	_, err = store.db.Exec("UPDATE normalized_events SET message = 'connection allowed hello' WHERE event_id = 'worm-evt-test-999';")
	if err != nil {
		t.Fatalf("failed to tamper message column: %v", err)
	}

	repTamperedMsg, err := store.VerifyNormalized(ctx, "worm-evt-test-999")
	if err != nil {
		t.Fatalf("VerifyNormalized after tamper error: %v", err)
	}
	if repTamperedMsg.Passed || repTamperedMsg.Status != "TAMPER DETECTED" {
		t.Fatalf("expected message tamper to be DETECTED, but got Passed=true!")
	}
	t.Logf("Message tamper detected as expected: %s", repTamperedMsg.FailureReason)

	// Revert message column
	_, _ = store.db.Exec("UPDATE normalized_events SET message = 'connection allowed' WHERE event_id = 'worm-evt-test-999';")

	// 5. Tamper 2: Modify event_json column
	_, err = store.db.Exec("UPDATE normalized_events SET event_json = replace(event_json, 'connection allowed', 'connection denied') WHERE event_id = 'worm-evt-test-999';")
	if err != nil {
		t.Fatalf("failed to tamper event_json column: %v", err)
	}

	repTamperedJSON, err := store.VerifyNormalized(ctx, "worm-evt-test-999")
	if err != nil {
		t.Fatalf("VerifyNormalized after JSON tamper error: %v", err)
	}
	if repTamperedJSON.Passed || repTamperedJSON.Status != "TAMPER DETECTED" {
		t.Fatalf("expected JSON tamper to be DETECTED, but got Passed=true!")
	}
	t.Logf("JSON tamper detected as expected: %s", repTamperedJSON.FailureReason)
}

func TestSchemaUserVersion(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "version_test.db")

	// First initialization: should execute schema v1 and set user_version = 1
	store1, err := New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	var version1 int
	if err := store1.db.QueryRow("PRAGMA user_version;").Scan(&version1); err != nil {
		t.Fatalf("failed to query user_version: %v", err)
	}
	if version1 != CurrentSchemaVersion {
		t.Fatalf("expected user_version=%d, got %d", CurrentSchemaVersion, version1)
	}
	_ = store1.Close()

	// Second initialization: should read user_version = 1 and skip DDL execution
	store2, err := New(dbPath)
	if err != nil {
		t.Fatalf("failed to reopen store: %v", err)
	}
	defer store2.Close()

	var version2 int
	if err := store2.db.QueryRow("PRAGMA user_version;").Scan(&version2); err != nil {
		t.Fatalf("failed to query user_version on reopened store: %v", err)
	}
	if version2 != CurrentSchemaVersion {
		t.Fatalf("expected user_version=%d on reopen, got %d", CurrentSchemaVersion, version2)
	}
}

func TestDeleteQuarantineAndCounts(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	// Initial counts should be 0
	rawCount, _ := store.CountRaw(ctx)
	normCount, _ := store.CountNormalized(ctx)
	quarCount, _ := store.CountQuarantine(ctx)
	if rawCount != 0 || normCount != 0 || quarCount != 0 {
		t.Fatalf("expected all counts to be 0, got raw=%d norm=%d quar=%d", rawCount, normCount, quarCount)
	}

	// 1. Store a raw event
	rec := model.IngestedRecord{
		Transport: "syslog_udp",
		SourceIP:  "127.0.0.1",
		RawBytes:  []byte("<14>test"),
	}
	savedRaw, err := store.Store(ctx, rec)
	if err != nil {
		t.Fatalf("store failed: %v", err)
	}

	rawCount, _ = store.CountRaw(ctx)
	if rawCount != 1 {
		t.Fatalf("expected rawCount 1, got %d", rawCount)
	}

	// 2. Quarantine it
	qEntry := model.QuarantineEntry{
		QuarantineID:   "quar-del-test",
		RawID:          savedRaw.RawID,
		RawSHA256:      savedRaw.RawSHA256,
		Stage:          "parser_match",
		Reason:         "unknown_source",
		RawPreview:     "<14>test",
		ReplayEligible: true,
		QuarantinedAt:  time.Now().UTC(),
	}
	if err := store.Quarantine(ctx, qEntry); err != nil {
		t.Fatalf("quarantine failed: %v", err)
	}

	quarCount, _ = store.CountQuarantine(ctx)
	if quarCount != 1 {
		t.Fatalf("expected quarCount 1, got %d", quarCount)
	}

	// 3. Delete from quarantine
	if err := store.DeleteQuarantine(ctx, "quar-del-test"); err != nil {
		t.Fatalf("delete quarantine failed: %v", err)
	}

	quarCount, _ = store.CountQuarantine(ctx)
	if quarCount != 0 {
		t.Fatalf("expected quarCount 0 after deletion, got %d", quarCount)
	}
}

