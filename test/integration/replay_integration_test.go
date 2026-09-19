package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"worm/internal/api"
	"worm/internal/ingest"
	"worm/internal/output"
	"worm/internal/packs"
	"worm/internal/pipeline"
	"worm/internal/rawstore"
)

func TestEndToEnd_ReplayAndPacks(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "worm.db")
	outputPath := filepath.Join(tempDir, "normalized.ndjson")
	inboxDir := filepath.Join(tempDir, "inbox")
	_ = os.MkdirAll(inboxDir, 0755)

	store, err := rawstore.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create raw store: %v", err)
	}
	defer store.Close()

	root := findProjectRoot(t)
	snap, err := packs.LoadDir(filepath.Join(root, "packs"))
	if err != nil {
		t.Fatalf("failed to load packs: %v", err)
	}
	packMgr := packs.NewSnapshotManager(snap)

	sink, err := output.NewNDJSONSink(outputPath)
	if err != nil {
		t.Fatalf("failed to create sink: %v", err)
	}
	defer sink.Close()

	pipe := pipeline.New(pipeline.Config{
		Workers:     4,
		BufferSize:  1000,
		PackManager: packMgr,
	}, store, sink)
	pipe.Start()
	defer pipe.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fileWatcher := ingest.NewFileWatcher(inboxDir, 20*time.Millisecond)
	mgr := ingest.NewManager(100)
	mgr.Register(fileWatcher)
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("failed to start ingest manager: %v", err)
	}
	pipe.ConnectIngest(ctx, mgr.Channel())

	// Copy all files from testdata/replay to inbox
	replayDir := filepath.Join(root, "testdata", "replay")
	entries, err := os.ReadDir(replayDir)
	if err != nil {
		t.Fatalf("failed to read testdata/replay: %v", err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(replayDir, e.Name()))
		if err != nil {
			t.Fatalf("failed to read %s: %v", e.Name(), err)
		}
		dest := filepath.Join(inboxDir, e.Name())
		if err := os.WriteFile(dest, data, 0644); err != nil {
			t.Fatalf("failed to write %s: %v", dest, err)
		}
	}

	// Allow file watcher to ingest and workers to process
	time.Sleep(800 * time.Millisecond)

	// Check quarantine count: exactly 3 logs must be quarantined (2 order-service, 1 edge-waf)
	qEntries, qTotal, err := store.ListQuarantinePaged(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListQuarantinePaged failed: %v", err)
	}
	for _, q := range qEntries {
		t.Logf("  * Quarantine ID: %s | Stage: %s | Reason: %s | Preview: %s", q.QuarantineID, q.Stage, q.Reason, q.RawPreview)
	}
	if qTotal != 3 {
		t.Fatalf("expected 3 quarantined entries, got %d", qTotal)
	}
	t.Logf("Successfully verified 3 quarantined entries:")
	for _, q := range qEntries {
		t.Logf("  * Quarantine ID: %s | Stage: %s | Reason: %s", q.QuarantineID, q.Stage, q.Reason)
	}

	// Check normalized count: exactly 7 logs must be normalized
	normEvents, normTotal, err := store.ListNormalized(ctx, "", "", "", 50, 0)
	if err != nil {
		t.Fatalf("ListNormalized failed: %v", err)
	}
	if normTotal != 7 {
		t.Fatalf("expected 7 normalized entries initially, got %d", normTotal)
	}
	t.Logf("Successfully verified 7 normalized entries initially")

	tempPacksDir := filepath.Join(tempDir, "packs")
	_ = os.MkdirAll(tempPacksDir, 0755)
	basePacks, _ := os.ReadDir(filepath.Join(root, "packs"))
	for _, bp := range basePacks {
		bdata, _ := os.ReadFile(filepath.Join(root, "packs", bp.Name()))
		_ = os.WriteFile(filepath.Join(tempPacksDir, bp.Name()), bdata, 0644)
	}

	// Setup API server to test dynamic pack activation and replay-all
	srv := api.NewServer(
		":0",
		store,
		pipe,
		packMgr,
		tempPacksDir,
		api.ConfigInfo{InboxDir: inboxDir},
		nil,
	)
	handler := srv.Handler()

	// 1. Activate Pack 1: app-order-service.yaml
	orderPackYAML, err := os.ReadFile(filepath.Join(replayDir, "packs", "app-order-service.yaml"))
	if err != nil {
		t.Fatalf("failed to read app-order-service.yaml: %v", err)
	}
	actOrderBody, _ := json.Marshal(map[string]string{
		"yaml_content": string(orderPackYAML),
		"filename":     "app-order-service.yaml",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/packs/activate", bytes.NewReader(actOrderBody))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("activate pack 1 failed with %d: %s", rec.Code, rec.Body.String())
	}
	t.Logf("Activated pack 1: app-order-service")

	// 2. Activate Pack 2: edge-waf.yaml
	wafPackYAML, err := os.ReadFile(filepath.Join(replayDir, "packs", "edge-waf.yaml"))
	if err != nil {
		t.Fatalf("failed to read edge-waf.yaml: %v", err)
	}
	actWafBody, _ := json.Marshal(map[string]string{
		"yaml_content": string(wafPackYAML),
		"filename":     "edge-waf.yaml",
	})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/packs/activate", bytes.NewReader(actWafBody))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("activate pack 2 failed with %d: %s", rec.Code, rec.Body.String())
	}
	t.Logf("Activated pack 2: edge-waf")

	// 3. Trigger Global Replay All
	req = httptest.NewRequest(http.MethodPost, "/api/v1/quarantine/replay-all", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("replay-all failed with %d: %s", rec.Code, rec.Body.String())
	}

	var replayResp struct {
		Total    int `json:"total"`
		Replayed int `json:"replayed"`
		Failed   int `json:"failed"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&replayResp); err != nil {
		t.Fatalf("failed to decode replay-all response: %v", err)
	}
	if replayResp.Total != 3 || replayResp.Replayed != 3 || replayResp.Failed != 0 {
		t.Fatalf("expected 3/3 replayed, got %+v", replayResp)
	}
	t.Logf("Replay All executed successfully: %+v", replayResp)

	// 4. Verify that all 10 events are now normalized
	normEvents, normTotal, err = store.ListNormalized(ctx, "", "", "", 50, 0)
	if err != nil {
		t.Fatalf("ListNormalized failed: %v", err)
	}
	if normTotal != 10 {
		t.Fatalf("expected 10 total normalized events after replay, got %d", normTotal)
	}

	// 5. Verify Cryptographic Integrity of every normalized event
	for _, evt := range normEvents {
		rep, err := store.VerifyNormalized(ctx, evt.Worm.EventID)
		if err != nil {
			t.Fatalf("VerifyNormalized error for %s: %v", evt.Worm.EventID, err)
		}
		if !rep.Passed {
			t.Fatalf("integrity check FAILED for %s: %s", evt.Worm.EventID, rep.FailureReason)
		}
	}
	t.Logf("Cryptographic SHA-256 verification PASSED for all 10 normalized events!")
}
