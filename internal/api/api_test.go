package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"worm/internal/api"
	"worm/internal/model"
	"worm/internal/packs"
	"worm/internal/pipeline"
	"worm/internal/rawstore"
)

func setupTestServer(t *testing.T) (*api.Server, *rawstore.RawStore, *pipeline.Pipeline, string) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "worm_api_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tempDir, "worm.db")
	store, err := rawstore.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create raw store: %v", err)
	}

	// Create test parser pack
	packsDir := filepath.Join(tempDir, "packs")
	_ = os.MkdirAll(packsDir, 0755)

	samplePackYAML := `
apiVersion: worm.io/v1
kind: LogSource
metadata:
  name: test-firewall
  version: 1.0.0
  description: Test Firewall Pack
  author: SecurityOps
spec:
  sourceCategory: network_device
  format: syslog
  match:
    contains: "%ASA-6-302013"
  fields:
    src_ip:
      from: src_ip
      type: ip
`
	_ = os.WriteFile(filepath.Join(packsDir, "test-firewall.yaml"), []byte(samplePackYAML), 0644)
	snap, err := packs.LoadDir(packsDir)
	if err != nil {
		t.Fatalf("failed to load packs: %v", err)
	}
	packMgr := packs.NewSnapshotManager(snap)

	pipe := pipeline.New(pipeline.Config{
		Workers:     2,
		BufferSize:  100,
		PackManager: packMgr,
	}, store, nil)
	pipe.Start()

	mockFS := fstest.MapFS{
		"index.html": {
			Data: []byte("<!DOCTYPE html><html><body>WORM Test UI</body></html>"),
		},
		"assets/test.js": {
			Data: []byte("console.log('test');"),
		},
	}

	cfg := api.ConfigInfo{
		SyslogUDP:  ":1514",
		SyslogTCP:  ":1514",
		HTTPIngest: ":8080",
		InboxDir:   "data/inbox",
		DBPath:     dbPath,
		Workers:    2,
	}

	srv := api.NewServer("127.0.0.1:0", store, pipe, packMgr, packsDir, cfg, mockFS)
	return srv, store, pipe, tempDir
}

func TestAPI_HealthAndStats(t *testing.T) {
	srv, store, pipe, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)
	defer store.Close()
	defer pipe.Stop()

	handler := srv.Handler()

	// 1. Health
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var health map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&health); err != nil {
		t.Fatalf("failed to decode health: %v", err)
	}
	if health["status"] != "ok" || health["air_gapped"] != true {
		t.Fatalf("invalid health response: %+v", health)
	}

	// 2. Stats
	req = httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var stats map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&stats); err != nil {
		t.Fatalf("failed to decode stats: %v", err)
	}
	if _, ok := stats["loss_audit"]; !ok {
		t.Fatalf("missing loss_audit in stats: %+v", stats)
	}
}

func TestAPI_EventsAndTrace(t *testing.T) {
	srv, store, pipe, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)
	defer store.Close()
	defer pipe.Stop()

	// Insert an event
	ctx := context.Background()
	raw, err := store.Store(ctx, model.IngestedRecord{
		Transport:  "udp",
		SourceIP:   "192.168.1.10",
		SourcePort: 514,
		RawBytes:   []byte("<134>Sep 18 10:00:00 asa %ASA-6-302013: Built outbound TCP connection"),
		ReceivedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("failed to store raw: %v", err)
	}

	evt := &model.NormalizedEvent{
		Worm: model.WormEnvelope{
			EventID:        "evt-test-123",
			RawID:          raw.RawID,
			RawSHA256:      raw.RawSHA256,
			SourceCategory: "network_device",
			SourceID:       "asa-fw-01",
			ReceivedTime:   time.Now().UTC(),
			Status:         "normalized",
			ProcessingHistory: []model.ProcessingStep{
				{Stage: "raw_commit", Result: "ok"},
				{Stage: "format_detect", Result: "syslog"},
			},
		},
		OCSF: map[string]any{
			"activity_name": "Logon",
			"severity_id":   3,
			"unmapped": map[string]any{
				"custom_flag": "val",
			},
		},
	}
	if err := store.StoreNormalized(ctx, evt); err != nil {
		t.Fatalf("failed to store normalized: %v", err)
	}

	handler := srv.Handler()

	// 1. List Events
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var listResp map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&listResp)
	if total, ok := listResp["total"].(float64); !ok || int(total) != 1 {
		t.Fatalf("expected total 1, got %+v", listResp["total"])
	}

	// 2. Get Single Event
	req = httptest.NewRequest(http.MethodGet, "/api/v1/events/evt-test-123", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// 3. Event Trace
	req = httptest.NewRequest(http.MethodGet, "/api/v1/events/evt-test-123/trace", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var traceResp map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&traceResp)
	if traceResp["raw_id"] != raw.RawID {
		t.Fatalf("trace raw_id mismatch: %+v", traceResp)
	}
	if traceResp["raw_hex"] == "" {
		t.Fatalf("trace raw_hex empty")
	}

	// 4. Raw Verify
	req = httptest.NewRequest(http.MethodPost, "/api/v1/raw/"+raw.RawID+"/verify", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var verifyResp map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&verifyResp)
	if verifyResp["matches"] != true || verifyResp["status"] != "PASSED" {
		t.Fatalf("expected verify PASSED, got %+v", verifyResp)
	}
}

func TestAPI_PacksValidationAndManagement(t *testing.T) {
	srv, store, pipe, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)
	defer store.Close()
	defer pipe.Stop()

	handler := srv.Handler()

	// 1. List packs
	req := httptest.NewRequest(http.MethodGet, "/api/v1/packs", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var packsResp map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&packsResp)
	packsList := packsResp["packs"].([]any)
	if len(packsList) != 1 {
		t.Fatalf("expected 1 pack, got %d", len(packsList))
	}

	// 2. Validate pack
	validYAML := `
apiVersion: worm.io/v1
kind: LogSource
metadata:
  name: test-ssh
  version: 1.0.0
spec:
  sourceCategory: os
  format: syslog
  match:
    contains: "sshd"
  fields:
    user:
      from: user
      type: string
`
	valPayload := map[string]string{
		"yaml_content": validYAML,
		"sample_log":   "<34>Sep 18 10:00:00 host sshd[1234]: Accepted publickey for ubuntu",
	}
	body, _ := json.Marshal(valPayload)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/packs/validate", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var valResp map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&valResp)
	if valResp["valid"] != true || valResp["match"] != true {
		t.Fatalf("expected validation success and match, got %+v", valResp)
	}

	// 3. Static SPA fallback
	req = httptest.NewRequest(http.MethodGet, "/events/123", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("WORM Test UI")) {
		t.Fatalf("expected index.html fallback for client routing, got: %s", rec.Body.String())
	}
}

func TestAPI_QuarantineReplayAll(t *testing.T) {
	srv, store, pipe, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)
	defer store.Close()
	defer pipe.Stop()

	handler := srv.Handler()

	// 1. Initially empty quarantine replay-all should succeed with total: 0
	req := httptest.NewRequest(http.MethodPost, "/api/v1/quarantine/replay-all", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp["total"].(float64) != 0 || resp["replayed"].(float64) != 0 {
		t.Fatalf("expected total 0 replayed 0, got %+v", resp)
	}

	// 2. Test GET /api/v1/packs/{name}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/packs/test-firewall", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for pack test-firewall, got %d: %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("test-firewall")) {
		t.Fatalf("expected YAML to contain test-firewall, got: %s", rec.Body.String())
	}
}

func TestAPI_PathTraversalBlocked(t *testing.T) {
	srv, store, pipe, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)
	defer store.Close()
	defer pipe.Stop()

	handler := srv.Handler()

	// 1. Path traversal in GET /api/v1/packs/{name}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/packs/..%2f..%2fetc%2fpasswd", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for path traversal attempt, got %d", rec.Code)
	}

	// 2. Path traversal in POST /api/v1/packs/activate with malicious filename
	maliciousPayload := map[string]string{
		"yaml_content": `apiVersion: worm.io/v1
kind: LogSource
metadata:
  name: malicious-pack
  version: 1.0.0
spec:
  sourceCategory: network_device
  format: syslog
  match:
    contains: "TEST"
`,
		"filename": "../../evil.yaml",
	}
	body, _ := json.Marshal(maliciousPayload)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/packs/activate", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for malicious filename, got %d", rec.Code)
	}
}

func TestAPI_ReplayIneligibleBlocked(t *testing.T) {
	srv, store, pipe, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)
	defer store.Close()
	defer pipe.Stop()

	ctx := context.Background()
	// Create a raw record and quarantine it as ineligible (replay_eligible = false)
	raw, err := store.Store(ctx, model.IngestedRecord{
		Transport:  "syslog",
		SourceIP:   "127.0.0.1",
		RawBytes:   []byte("<14>1 unparseable"),
		ReceivedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("store failed: %v", err)
	}

	err = store.Quarantine(ctx, model.QuarantineEntry{
		QuarantineID:   "quar-ineligible-01",
		RawID:          raw.RawID,
		RawSHA256:      raw.RawSHA256,
		Stage:          "decode",
		Reason:         "corrupt_binary",
		RawPreview:     "preview",
		ReplayEligible: false, // Ineligible!
		QuarantinedAt:  time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("quarantine failed: %v", err)
	}

	handler := srv.Handler()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/quarantine/quar-ineligible-01/replay", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request when attempting to replay ineligible entry, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAPI_StaticAndSPARouting(t *testing.T) {
	srv, store, pipe, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)
	defer store.Close()
	defer pipe.Stop()

	handler := srv.Handler()

	// 1. Root index.html
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for /, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "WORM Test UI") {
		t.Fatalf("expected root to serve index.html, got: %s", rec.Body.String())
	}

	// 2. Static asset file
	req = httptest.NewRequest(http.MethodGet, "/assets/test.js", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for /assets/test.js, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "console.log('test')") {
		t.Fatalf("expected static JS content, got: %s", rec.Body.String())
	}

	// 3. SPA client-side route fallback
	req = httptest.NewRequest(http.MethodGet, "/events/worm-evt-001", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for SPA route, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "WORM Test UI") {
		t.Fatalf("expected SPA fallback to index.html, got: %s", rec.Body.String())
	}
}

func TestAPI_AdminTokenAuth_BA023(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "worm_auth_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "worm.db")
	store, err := rawstore.New(dbPath)
	if err != nil {
		t.Fatalf("failed to open raw store: %v", err)
	}
	defer store.Close()

	cfg := api.ConfigInfo{
		Version:    "1.0.0",
		AdminToken: "super-secret-admin-token",
	}
	srv := api.NewServer("127.0.0.1:0", store, nil, nil, "", cfg, nil)
	handler := srv.Handler()

	// 1. Without token on raw endpoint -> 401 Unauthorized
	req := httptest.NewRequest(http.MethodGet, "/api/v1/raw/worm-raw-nonexistent", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized without token, got %d", rec.Code)
	}

	// 2. With wrong token -> 401 Unauthorized
	req = httptest.NewRequest(http.MethodGet, "/api/v1/raw/worm-raw-nonexistent", nil)
	req.Header.Set("X-WORM-Admin-Key", "wrong-token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized with wrong token, got %d", rec.Code)
	}

	// 3. With correct token via header -> Allowed past auth (404 since record doesn't exist)
	req = httptest.NewRequest(http.MethodGet, "/api/v1/raw/worm-raw-nonexistent", nil)
	req.Header.Set("X-WORM-Admin-Key", "super-secret-admin-token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found past auth, got %d", rec.Code)
	}

	// 4. With correct token via Bearer -> Allowed past auth
	req = httptest.NewRequest(http.MethodGet, "/api/v1/raw/worm-raw-nonexistent", nil)
	req.Header.Set("Authorization", "Bearer super-secret-admin-token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found with Bearer token, got %d", rec.Code)
	}
}
