package normalize_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"worm/internal/decode"
	"worm/internal/model"
	"worm/internal/normalize"
	"worm/internal/packs"
	"worm/internal/pipeline"
	"worm/internal/rawstore"
)

func findProjectRoot(t *testing.T) string {
	t.Helper()
	candidates := []string{
		"../../",
		"./",
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, "packs")); err == nil {
			return c
		}
	}
	t.Fatalf("could not locate project root with packs/ directory")
	return ""
}

func loadFixture(t *testing.T, root, relPath string) []byte {
	t.Helper()
	p := filepath.Join(root, relPath)
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("failed to load fixture %s: %v", p, err)
	}
	return data
}

func TestNormalize_AllSixSourcesGolden(t *testing.T) {
	root := findProjectRoot(t)
	snap, err := packs.LoadDir(filepath.Join(root, "packs"))
	if err != nil {
		t.Fatalf("failed to load packs: %v", err)
	}

	registry := decode.DefaultRegistry()
	normalizer := normalize.NewNormalizer()
	validator := normalize.NewValidator()

	sources := []struct {
		name         string
		fixturePath  string
		expectedPath string
		packName     string
		transport    string
		sourceIP     string
	}{
		{
			name:         "Palo Alto Firewall",
			fixturePath:  "testdata/sources/network-device-firewall/valid_session.log",
			expectedPath: "testdata/sources/network-device-firewall/expected.json",
			packName:     "network-device-firewall",
			transport:    "syslog_udp",
			sourceIP:     "10.0.1.50",
		},
		{
			name:         "Cisco ASA Firewall",
			fixturePath:  "testdata/sources/network-device-asa/valid_permit.log",
			expectedPath: "testdata/sources/network-device-asa/expected.json",
			packName:     "network-device-asa",
			transport:    "syslog_udp",
			sourceIP:     "10.0.1.100",
		},
		{
			name:         "Nginx/Apache Web Server",
			fixturePath:  "testdata/sources/server-webaccess/valid_access.log",
			expectedPath: "testdata/sources/server-webaccess/expected.json",
			packName:     "server-webaccess",
			transport:    "file_spool",
			sourceIP:     "10.0.1.50",
		},
		{
			name:         "Linux SSHD Auth",
			fixturePath:  "testdata/sources/os-linux-auth/valid_sshd.log",
			expectedPath: "testdata/sources/os-linux-auth/expected.json",
			packName:     "os-linux-auth",
			transport:    "syslog_udp",
			sourceIP:     "10.0.1.100",
		},
		{
			name:         "Suricata IDS Alert CEF",
			fixturePath:  "testdata/sources/endpoint-ids-alert/valid_alert.cef",
			expectedPath: "testdata/sources/endpoint-ids-alert/expected.json",
			packName:     "endpoint-ids-alert",
			transport:    "syslog_udp",
			sourceIP:     "45.33.32.156",
		},
		{
			name:         "Payment Gateway JSON",
			fixturePath:  "testdata/sources/app-custom-json/valid_after.json",
			expectedPath: "testdata/sources/app-custom-json/expected.json",
			packName:     "app-payment-gateway",
			transport:    "http_post",
			sourceIP:     "10.0.1.50",
		},
		{
			name:         "Database Audit CSV",
			fixturePath:  "testdata/sources/database-audit/valid_audit.csv",
			expectedPath: "testdata/sources/database-audit/expected.json",
			packName:     "database-audit",
			transport:    "file_spool",
			sourceIP:     "10.0.1.50",
		},
	}

	for _, s := range sources {
		t.Run(s.name, func(t *testing.T) {
			rawBytes := loadFixture(t, root, s.fixturePath)
			expectedBytes := loadFixture(t, root, s.expectedPath)

			var expected model.NormalizedEvent
			if err := json.Unmarshal(expectedBytes, &expected); err != nil {
				t.Fatalf("failed to unmarshal expected.json: %v", err)
			}

			// 1. Detect & Decode
			_, decodedRecords, err := registry.DetectAndDecode(rawBytes)
			if err != nil {
				t.Fatalf("DetectAndDecode failed: %v", err)
			}
			if len(decodedRecords) == 0 {
				t.Fatalf("no decoded records produced")
			}

			decRec := decodedRecords[0]

			// 2. Match parser pack
			pack, err := snap.Match(decRec)
			if err != nil {
				t.Fatalf("pack match failed: %v", err)
			}
			if pack.Metadata.Name != s.packName {
				t.Errorf("expected pack %s, got %s", s.packName, pack.Metadata.Name)
			}

			// Mock raw event
			rawEvt := &model.RawEvent{
				RawID:      "worm-raw-20260920-000001-test",
				RawSHA256:  "559f4db86f4a3fde93c500e063c1b6e2e69b15f4d8bb168d5e213b6fa54d6022",
				ByteCount:  len(rawBytes),
				Transport:  s.transport,
				SourceIP:   s.sourceIP,
				ReceivedAt: time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC),
				Payload:    rawBytes,
				Status:     model.StatusAccepted,
			}

			// 3. Normalize
			norm, err := normalizer.Normalize(rawEvt, decRec, pack, []model.ProcessingStep{
				{Stage: "raw_commit", Timestamp: time.Now().UTC(), Result: "ok"},
				{Stage: "format_detect", Timestamp: time.Now().UTC(), Result: decRec.Format},
			})
			if err != nil {
				t.Fatalf("Normalize failed: %v", err)
			}

			// 4. Validate
			if err := validator.Validate(norm); err != nil {
				t.Fatalf("Validate failed: %v", err)
			}

			// 5. Compare with expected
			if norm.Worm.SourceCategory != expected.Worm.SourceCategory {
				t.Errorf("category mismatch: got %s, want %s", norm.Worm.SourceCategory, expected.Worm.SourceCategory)
			}
			if norm.Worm.ParserPack != expected.Worm.ParserPack {
				t.Errorf("pack mismatch: got %s, want %s", norm.Worm.ParserPack, expected.Worm.ParserPack)
			}

			// Compare all expected fields deeply
			matchExpected(t, s.name, norm.OCSF, expected.OCSF)
		})
	}
}

func matchExpected(t *testing.T, prefix string, actual, expected any) {
	t.Helper()
	switch exp := expected.(type) {
	case map[string]any:
		actMap, ok := actual.(map[string]any)
		if !ok {
			t.Errorf("%s: expected map, got %T (%v)", prefix, actual, actual)
			return
		}
		for k, v := range exp {
			if k == "unmapped" {
				continue
			}
			actVal, exists := actMap[k]
			if !exists {
				t.Errorf("%s: missing expected key %q", prefix, k)
				continue
			}
			matchExpected(t, prefix+"."+k, actVal, v)
		}
	default:
		expStr := fmt.Sprintf("%v", expected)
		actStr := fmt.Sprintf("%v", actual)
		if expStr != actStr {
			t.Errorf("%s mismatch: got %v, want %v", prefix, actStr, expStr)
		}
	}
}

func TestValidator_SchemaValidation(t *testing.T) {
	v := normalize.NewValidator()

	baseNorm := func() *model.NormalizedEvent {
		return &model.NormalizedEvent{
			Worm: model.WormEnvelope{
				EventID:   "worm-evt-0001",
				RawID:     "worm-raw-0001",
				RawSHA256: "abc123hash",
			},
			OCSF: map[string]any{
				"time": int64(1789559400000),
				"src_endpoint": map[string]any{
					"ip":   "10.0.1.50",
					"port": int64(443),
				},
				"dst_endpoint": map[string]any{
					"ip":   "192.168.1.1",
					"port": int64(80),
				},
				"severity_id": int64(1),
			},
		}
	}

	// 1. Valid event passes
	if err := v.Validate(baseNorm()); err != nil {
		t.Fatalf("expected valid event to pass, got: %v", err)
	}

	// 2. Missing event.time fails
	missingTime := baseNorm()
	delete(missingTime.OCSF, "time")
	if err := v.Validate(missingTime); err == nil {
		t.Errorf("expected missing time to fail, got nil")
	}

	// 3. Negative event.time fails
	negTime := baseNorm()
	negTime.OCSF["time"] = int64(-5)
	if err := v.Validate(negTime); err == nil {
		t.Errorf("expected negative time to fail, got nil")
	}

	// 4. Invalid IP fails
	badIP := baseNorm()
	badIP.OCSF["src_endpoint"] = map[string]any{"ip": "999.999.999.999"}
	if err := v.Validate(badIP); err == nil {
		t.Errorf("expected invalid IP to fail, got nil")
	}

	// 5. Port out of range fails
	badPort := baseNorm()
	badPort.OCSF["src_endpoint"] = map[string]any{"ip": "10.0.1.50", "port": int64(70000)}
	if err := v.Validate(badPort); err == nil {
		t.Errorf("expected port 70000 to fail, got nil")
	}

	// 6. Severity out of range fails
	badSev := baseNorm()
	badSev.OCSF["severity_id"] = int64(99)
	if err := v.Validate(badSev); err == nil {
		t.Errorf("expected severity 99 to fail, got nil")
	}
}

func TestUnmappedFieldsPreservation(t *testing.T) {
	pack := &packs.ParserPack{
		APIVersion: "worm.io/v1",
		Kind:       "LogSource",
		Metadata:   packs.PackMetadata{Name: "unmapped-test", Version: "1.0.0"},
		Spec: packs.PackSpec{
			SourceCategory:   "network_device",
			Format:           "syslog",
			Match:            packs.MatchRule{Contains: "test"},
			PreserveUnmapped: true,
			Fields: map[string]packs.FieldRule{
				"src":  {From: "src", Type: "ip"},
				"time": {From: "time", Type: "timestamp"},
			},
			Map: map[string]string{
				"src":  "event.src_endpoint.ip",
				"time": "event.time",
			},
		},
	}

	raw := &model.RawEvent{
		RawID:      "worm-raw-unmapped-test",
		RawSHA256:  "testhash",
		ByteCount:  100,
		ReceivedAt: time.Now().UTC(),
	}

	dec := &model.DecodedRecord{
		Format: "syslog",
		Fields: map[string]any{
			"src":            "10.0.1.50",
			"time":           "2026-09-20T10:05:23Z",
			"vendor_tag_1":   "custom_security_id_xyz",
			"vendor_debug_2": 4242,
		},
	}

	norm, err := normalize.NewNormalizer().Normalize(raw, dec, pack, nil)
	if err != nil {
		t.Fatalf("Normalize failed: %v", err)
	}

	unmapped, ok := norm.OCSF["unmapped"].(map[string]any)
	if !ok {
		t.Fatalf("expected unmapped map in OCSF event, got %T", norm.OCSF["unmapped"])
	}

	if unmapped["vendor_tag_1"] != "custom_security_id_xyz" {
		t.Errorf("expected vendor_tag_1 to be preserved, got %v", unmapped["vendor_tag_1"])
	}
	if unmapped["vendor_debug_2"] != 4242 {
		t.Errorf("expected vendor_debug_2 to be preserved, got %v", unmapped["vendor_debug_2"])
	}
	if _, exists := unmapped["src"]; exists {
		t.Errorf("mapped field 'src' should not be in unmapped")
	}
}

func TestPipeline_QuarantineAndReplay(t *testing.T) {
	root := findProjectRoot(t)
	dbPath := filepath.Join(t.TempDir(), "pipeline_replay.db")
	store, err := rawstore.New(dbPath)
	if err != nil {
		t.Fatalf("failed to open raw store: %v", err)
	}
	defer store.Close()

	sink := pipeline.NewMemorySink()

	// 1. Initially start with an EMPTY snapshot (no packs loaded)
	emptySnap := packs.NewSnapshot("v0-empty", []*packs.ParserPack{})
	packManager := packs.NewSnapshotManager(emptySnap)

	p := pipeline.New(pipeline.Config{
		Workers:     1,
		BufferSize:  10,
		PackManager: packManager,
	}, store, sink)
	p.Start()

	ctx := context.Background()

	// 2. Submit payment JSON fixture (which has NO matching pack currently)
	paymentFixture := loadFixture(t, root, "testdata/sources/app-custom-json/quarantined_before.json")
	rec := model.IngestedRecord{
		Transport:  "http_post",
		SourceIP:   "10.0.1.50",
		RawBytes:   paymentFixture,
		ReceivedAt: time.Now().UTC(),
	}

	if err := p.SubmitSync(ctx, rec); err != nil {
		t.Fatalf("SubmitSync failed: %v", err)
	}

	// Verify it was quarantined as unknown_source
	stats := p.Stats()
	if stats.Quarantined != 1 {
		t.Fatalf("expected 1 quarantined event, got %d", stats.Quarantined)
	}
	if len(sink.Events()) != 0 {
		t.Fatalf("expected 0 normalized events in sink, got %d", len(sink.Events()))
	}

	quarantinedList, err := store.GetQuarantined(ctx, 10, 0)
	if err != nil || len(quarantinedList) != 1 {
		t.Fatalf("expected 1 quarantine entry in DB, got %d (err: %v)", len(quarantinedList), err)
	}

	qEntry := quarantinedList[0]
	if qEntry.Reason != "unknown_source" {
		t.Errorf("expected reason unknown_source, got %s", qEntry.Reason)
	}

	// 3. Now dynamically load all packs (including app-payment-gateway.yaml)
	fullSnap, err := packs.LoadDir(filepath.Join(root, "packs"))
	if err != nil {
		t.Fatalf("failed to load packs: %v", err)
	}

	// Hot-swap snapshot
	if err := packManager.Activate(fullSnap); err != nil {
		t.Fatalf("Activate failed: %v", err)
	}

	// 4. Replay the quarantined event
	replayedNorm, err := p.Replay(ctx, qEntry.QuarantineID)
	if err != nil {
		t.Fatalf("Replay failed: %v", err)
	}

	// Verify replayed output
	if replayedNorm.Worm.RawID != qEntry.RawID {
		t.Errorf("expected replayed event raw_id %s, got %s", qEntry.RawID, replayedNorm.Worm.RawID)
	}
	if replayedNorm.Worm.ParserPack != "app-payment-gateway" {
		t.Errorf("expected parser pack app-payment-gateway, got %s", replayedNorm.Worm.ParserPack)
	}
	if replayedNorm.OCSF["activity_name"] != "payment_process" {
		t.Errorf("expected activity_name payment_process, got %v", replayedNorm.OCSF["activity_name"])
	}

	// Verify sink received the replayed event
	if len(sink.Events()) != 1 {
		t.Errorf("expected 1 event in sink after replay, got %d", len(sink.Events()))
	}

	p.Stop()
}
