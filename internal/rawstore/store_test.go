package rawstore

import (
	"bytes"
	"context"
	"database/sql"
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
	var synchronous int
	if err := store.DB().QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil || synchronous != 2 {
		t.Fatalf("expected SQLite synchronous=FULL (2), got %d (err=%v)", synchronous, err)
	}

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

func TestListRawByStatusDoesNotDeadlockSingleConnection(t *testing.T) {
	store := setupTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	saved, err := store.Store(ctx, model.IngestedRecord{Transport: "test", RawBytes: []byte(`{"event":"restart"}`), ReceivedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.ListRawByStatus(ctx, model.StatusAccepted)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].RawID != saved.RawID {
		t.Fatalf("unexpected accepted recovery set: %+v", events)
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
	// Recovery may reprocess the same accepted parent after a crash. The child
	// lifecycle row must be updated, not duplicated.
	qEntry.ErrorDetails = "same child retried after restart"
	if err := store.Quarantine(ctx, qEntry); err != nil {
		t.Fatalf("idempotent Quarantine failed: %v", err)
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

func TestBatchChildrenAndSourcePort(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	rec := model.IngestedRecord{
		Transport:  "syslog_tcp",
		SourceIP:   "10.0.0.1",
		SourcePort: 5140,
		RawBytes:   []byte("batch payload line 1\nbatch payload line 2"),
		ReceivedAt: time.Now().UTC(),
	}
	rawEvt, err := store.Store(ctx, rec)
	if err != nil {
		t.Fatalf("store failed: %v", err)
	}

	retrieved, err := store.Retrieve(ctx, rawEvt.RawID)
	if err != nil {
		t.Fatalf("retrieve failed: %v", err)
	}
	if retrieved.SourcePort != 5140 {
		t.Errorf("expected SourcePort 5140, got %d", retrieved.SourcePort)
	}

	// Store child 0
	evt0 := &model.NormalizedEvent{
		Worm: model.WormEnvelope{
			EventID:        "evt-child-0",
			RawID:          rawEvt.RawID,
			RecordOrdinal:  0,
			SourceCategory: "server",
			SourceID:       "srv1",
			ReceivedTime:   time.Now().UTC(),
		},
		OCSF: map[string]any{
			"time":    time.Now().UnixMilli(),
			"message": "child zero",
		},
	}
	if err := store.StoreNormalized(ctx, evt0); err != nil {
		t.Fatalf("StoreNormalized child 0 failed: %v", err)
	}

	// Store child 1
	evt1 := &model.NormalizedEvent{
		Worm: model.WormEnvelope{
			EventID:        "evt-child-1",
			RawID:          rawEvt.RawID,
			RecordOrdinal:  1,
			SourceCategory: "server",
			SourceID:       "srv1",
			ReceivedTime:   time.Now().UTC(),
		},
		OCSF: map[string]any{
			"time":    time.Now().UnixMilli(),
			"message": "child one",
		},
	}
	if err := store.StoreNormalized(ctx, evt1); err != nil {
		t.Fatalf("StoreNormalized child 1 failed: %v", err)
	}

	// Both child records must exist
	count, err := store.CountNormalized(ctx)
	if err != nil {
		t.Fatalf("CountNormalized failed: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 normalized child records, got %d", count)
	}

	// Quarantine child 2
	qEntry := model.QuarantineEntry{
		QuarantineID:  "quar-child-2",
		RawID:         rawEvt.RawID,
		RecordOrdinal: 2,
		RawSHA256:     rawEvt.RawSHA256,
		Stage:         "parser_match",
		Reason:        "unknown_source",
		RawPreview:    "child two preview",
	}
	if err := store.Quarantine(ctx, qEntry); err != nil {
		t.Fatalf("Quarantine failed: %v", err)
	}

	gotQ, err := store.GetQuarantineByID(ctx, "quar-child-2")
	if err != nil {
		t.Fatalf("GetQuarantineByID failed: %v", err)
	}
	if gotQ.RecordOrdinal != 2 {
		t.Errorf("expected RecordOrdinal 2, got %d", gotQ.RecordOrdinal)
	}
}

func TestStoreDeduplicatesRecoveredFileSpoolCoordinates(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()
	rec := model.IngestedRecord{Transport: "file", RawBytes: []byte("line one"), Metadata: model.ReceiveMetadata{FileID: "file-fingerprint", FileOffset: 0}}
	first, err := store.Store(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Store(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Duplicate || second.RawID != first.RawID {
		t.Fatalf("expected duplicate existing row, first=%+v second=%+v", first, second)
	}
	count, err := store.CountRaw(ctx)
	if err != nil || count != 1 {
		t.Fatalf("expected one raw row, got %d (err=%v)", count, err)
	}
	rec.RawBytes = []byte("changed bytes")
	if _, err := store.Store(ctx, rec); err == nil {
		t.Fatal("expected changed bytes at an existing file coordinate to be rejected")
	}
}

func TestMigrateLegacySchemaTransactionallyAndIdempotently(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	legacy := `
CREATE TABLE raw_events(raw_id TEXT PRIMARY KEY, raw_sha256 TEXT NOT NULL, byte_count INTEGER NOT NULL, transport TEXT NOT NULL, source_ip TEXT NOT NULL, received_at TEXT NOT NULL, payload BLOB NOT NULL, status TEXT NOT NULL);
CREATE TABLE quarantine(quarantine_id TEXT PRIMARY KEY, raw_id TEXT NOT NULL, raw_sha256 TEXT NOT NULL, stage TEXT NOT NULL, reason TEXT NOT NULL, error_details TEXT, raw_preview TEXT NOT NULL, candidate_packs TEXT, replay_eligible INTEGER NOT NULL, quarantined_at TEXT NOT NULL, replayed_at TEXT);
CREATE TABLE normalized_events(event_id TEXT PRIMARY KEY, raw_id TEXT NOT NULL, source_category TEXT NOT NULL, source_id TEXT NOT NULL, severity_id TEXT, activity_name TEXT, message TEXT, event_time INTEGER NOT NULL, received_time TEXT NOT NULL, event_json TEXT NOT NULL, event_sha256 TEXT NOT NULL DEFAULT '');
PRAGMA user_version=1;`
	if _, err := db.Exec(legacy); err != nil {
		t.Fatal(err)
	}
	if err := initSchema(db); err != nil {
		t.Fatalf("migrate legacy schema: %v", err)
	}
	if err := initSchema(db); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != CurrentSchemaVersion {
		t.Fatalf("user_version=%d err=%v", version, err)
	}
	for _, col := range []string{"source_port", "metadata_json"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('raw_events') WHERE name=?", col).Scan(&count); err != nil || count != 1 {
			t.Fatalf("expected raw_events.%s after migration, count=%d err=%v", col, count, err)
		}
	}
}

func TestMigrationFailureDoesNotAdvanceOrPartiallyApply(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "broken-legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	legacy := `
CREATE TABLE raw_events(raw_id TEXT PRIMARY KEY, raw_sha256 TEXT NOT NULL, byte_count INTEGER NOT NULL, transport TEXT NOT NULL, source_ip TEXT NOT NULL, source_port INTEGER NOT NULL DEFAULT 0, received_at TEXT NOT NULL, payload BLOB NOT NULL, status TEXT NOT NULL);
CREATE TABLE quarantine(quarantine_id TEXT PRIMARY KEY, raw_id TEXT NOT NULL, record_ordinal INTEGER NOT NULL DEFAULT 0, raw_sha256 TEXT NOT NULL, stage TEXT NOT NULL, reason TEXT NOT NULL, error_details TEXT, raw_preview TEXT NOT NULL, candidate_packs TEXT, replay_eligible INTEGER NOT NULL, quarantined_at TEXT NOT NULL, replayed_at TEXT);
CREATE TABLE normalized_events(event_id TEXT PRIMARY KEY, raw_id TEXT NOT NULL, source_category TEXT NOT NULL, source_id TEXT NOT NULL, severity_id TEXT, activity_name TEXT, message TEXT, event_time INTEGER NOT NULL, received_time TEXT NOT NULL, event_json TEXT NOT NULL, event_sha256 TEXT NOT NULL DEFAULT '');
PRAGMA user_version=1;`
	if _, err := db.Exec(legacy); err != nil {
		t.Fatal(err)
	}
	if err := initSchema(db); err == nil {
		t.Fatal("expected migration to fail on inconsistent v1 schema")
	}
	var version, sourcePortCount int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('raw_events') WHERE name='source_port'").Scan(&sourcePortCount); err != nil {
		t.Fatal(err)
	}
	if version != 1 || sourcePortCount != 1 {
		t.Fatalf("failed migration changed schema/version unexpectedly: version=%d source_port_count=%d", version, sourcePortCount)
	}
}

func TestReplayUpdatesNormalizedProjectionColumns(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()
	raw, err := store.Store(ctx, model.IngestedRecord{Transport: "test", RawBytes: []byte("raw")})
	if err != nil {
		t.Fatal(err)
	}
	event := &model.NormalizedEvent{Worm: model.WormEnvelope{EventID: "event-old", RawID: raw.RawID, RecordOrdinal: 0, SourceCategory: "old-category", SourceID: "old-source", ReceivedTime: time.Unix(1, 0).UTC()}, OCSF: map[string]any{"time": int64(1000), "severity_id": 1, "activity_name": "old-activity", "message": "old-message"}}
	if err := store.StoreNormalized(ctx, event); err != nil {
		t.Fatal(err)
	}
	event.Worm.SourceCategory = "new-category"
	event.Worm.SourceID = "new-source"
	event.Worm.ReceivedTime = time.Unix(2, 0).UTC()
	event.OCSF = map[string]any{"time": int64(2000), "severity_id": 5, "activity_name": "new-activity", "message": "new-message"}
	if err := store.StoreNormalized(ctx, event); err != nil {
		t.Fatal(err)
	}
	var category, sourceID, severity, activity, message, received string
	var eventTime int64
	err = store.db.QueryRow(`SELECT source_category,source_id,severity_id,activity_name,message,event_time,received_time
		FROM normalized_events WHERE raw_id=? AND record_ordinal=0`, raw.RawID).Scan(&category, &sourceID, &severity, &activity, &message, &eventTime, &received)
	if err != nil {
		t.Fatal(err)
	}
	if category != "new-category" || sourceID != "new-source" || severity != "5" || activity != "new-activity" || message != "new-message" || eventTime != 2000 || received != event.Worm.ReceivedTime.Format(time.RFC3339Nano) {
		t.Fatalf("replay projection columns are stale: category=%q source=%q severity=%q activity=%q message=%q time=%d received=%q", category, sourceID, severity, activity, message, eventTime, received)
	}
}
