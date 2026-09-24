package packs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"worm/internal/decode"
	"worm/internal/model"
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

func TestLoadPack_AllPacks(t *testing.T) {
	root := findProjectRoot(t)
	packsDir := filepath.Join(root, "packs")

	snap, err := LoadDir(packsDir)
	if err != nil {
		t.Fatalf("failed to load packs from %s: %v", packsDir, err)
	}

	expectedPacks := []string{
		"network-device-firewall",
		"network-device-asa",
		"server-webaccess",
		"os-linux-auth",
		"endpoint-ids-alert",
		"app-payment-gateway",
		"database-audit",
		"cloud-audit",
		"container-runtime",
		"iam-auth-leef",
		"iot-gateway-xml",
		"appliance-proprietary-text",
		"network-perimeter-xml",
		"network-perimeter-leef",
	}

	if len(snap.ListPacks()) != len(expectedPacks) {
		t.Errorf("expected %d packs, found %d", len(expectedPacks), len(snap.ListPacks()))
	}

	for _, name := range expectedPacks {
		pack, ok := snap.GetPack(name)
		if !ok || pack == nil {
			t.Errorf("expected pack %q to be loaded", name)
		}
	}
}

func TestLoadPack_StrictYAML(t *testing.T) {
	invalidYAML := `
apiVersion: worm.io/v1
kind: LogSource
unknownFieldHere: shouldFail
metadata:
  name: test-pack
  version: 1.0.0
spec:
  sourceCategory: network_device
  format: syslog
  match:
    contains: "test"
  fields:
    src:
      from: src
      type: ip
  map:
    src: event.src_endpoint.ip
`
	_, err := LoadPack(strings.NewReader(invalidYAML))
	if err == nil {
		t.Errorf("expected error loading YAML with unknown fields, got nil")
	}
}

func TestValidate_InvalidPacks(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "Unsupported APIVersion",
			yaml: `
apiVersion: worm.io/v2
kind: LogSource
metadata:
  name: test-pack
  version: 1.0.0
spec:
  sourceCategory: network_device
  format: syslog
  match:
    contains: "test"
  fields: {}
  map: {}
`,
		},
		{
			name: "Unknown SourceCategory",
			yaml: `
apiVersion: worm.io/v1
kind: LogSource
metadata:
  name: test-pack
  version: 1.0.0
spec:
  sourceCategory: nonexistent_category
  format: syslog
  match:
    contains: "test"
  fields: {}
  map: {}
`,
		},
		{
			name: "Missing Match Rules",
			yaml: `
apiVersion: worm.io/v1
kind: LogSource
metadata:
  name: test-pack
  version: 1.0.0
spec:
  sourceCategory: network_device
  format: syslog
  match: {}
  fields: {}
  map: {}
`,
		},
		{
			name: "Invalid Regex",
			yaml: `
apiVersion: worm.io/v1
kind: LogSource
metadata:
  name: test-pack
  version: 1.0.0
spec:
  sourceCategory: network_device
  format: syslog
  match:
    regex: "(unclosed_parenthesis"
  fields: {}
  map: {}
`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadPack(strings.NewReader(tc.yaml))
			if err == nil {
				t.Errorf("expected validation failure for %s, got nil", tc.name)
			}
		})
	}
}

func TestSnapshot_Matching(t *testing.T) {
	root := findProjectRoot(t)
	packsDir := filepath.Join(root, "packs")
	snap, err := LoadDir(packsDir)
	if err != nil {
		t.Fatalf("LoadDir failed: %v", err)
	}

	registry := decode.DefaultRegistry()

	tests := []struct {
		name         string
		fixturePath  string
		expectedPack string
	}{
		{
			name:         "Palo Alto Firewall",
			fixturePath:  "testdata/sources/network-device-firewall/valid_session.log",
			expectedPack: "network-device-firewall",
		},
		{
			name:         "Cisco ASA",
			fixturePath:  "testdata/sources/network-device-asa/valid_permit.log",
			expectedPack: "network-device-asa",
		},
		{
			name:         "Server WebAccess",
			fixturePath:  "testdata/sources/server-webaccess/valid_access.log",
			expectedPack: "server-webaccess",
		},
		{
			name:         "Linux SSHD Auth",
			fixturePath:  "testdata/sources/os-linux-auth/valid_sshd.log",
			expectedPack: "os-linux-auth",
		},
		{
			name:         "Suricata IDS CEF",
			fixturePath:  "testdata/sources/endpoint-ids-alert/valid_alert.cef",
			expectedPack: "endpoint-ids-alert",
		},
		{
			name:         "Payment Gateway JSON",
			fixturePath:  "testdata/sources/app-custom-json/valid_after.json",
			expectedPack: "app-payment-gateway",
		},
		{
			name:         "Database Audit CSV",
			fixturePath:  "testdata/sources/database-audit/valid_audit.csv",
			expectedPack: "database-audit",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := loadFixture(t, root, tc.fixturePath)
			_, records, err := registry.DetectAndDecode(raw)
			if err != nil {
				t.Fatalf("DetectAndDecode failed: %v", err)
			}
			if len(records) == 0 {
				t.Fatalf("expected records, got 0")
			}

			matchedPack, err := snap.Match(records[0])
			if err != nil {
				t.Fatalf("Match failed: %v", err)
			}

			if matchedPack.Metadata.Name != tc.expectedPack {
				t.Errorf("expected pack %s, got %s", tc.expectedPack, matchedPack.Metadata.Name)
			}
		})
	}
}

func TestSnapshot_UnknownSource(t *testing.T) {
	root := findProjectRoot(t)
	snap, err := LoadDir(filepath.Join(root, "packs"))
	if err != nil {
		t.Fatalf("LoadDir failed: %v", err)
	}

	unknownRec := &model.DecodedRecord{
		Format:     "json",
		RawPayload: []byte(`{"event":"alien_format","data":123}`),
		Fields:     map[string]any{"event": "alien_format", "data": 123},
	}

	_, err = snap.Match(unknownRec)
	if !errors.Is(err, ErrNoMatch) {
		t.Errorf("expected ErrNoMatch, got %v", err)
	}
}

func TestSnapshot_AmbiguousSource(t *testing.T) {
	pack1 := &ParserPack{
		APIVersion: "worm.io/v1",
		Kind:       "LogSource",
		Metadata:   PackMetadata{Name: "pack-alpha", Version: "1.0.0"},
		Spec: PackSpec{
			SourceCategory: "network_device",
			Format:         "syslog",
			Match:          MatchRule{Contains: "ambiguous-token"},
			Fields:         map[string]FieldRule{},
			Map:            map[string]string{},
		},
	}
	pack2 := &ParserPack{
		APIVersion: "worm.io/v1",
		Kind:       "LogSource",
		Metadata:   PackMetadata{Name: "pack-beta", Version: "1.0.0"},
		Spec: PackSpec{
			SourceCategory: "network_device",
			Format:         "syslog",
			Match:          MatchRule{Contains: "ambiguous-token"},
			Fields:         map[string]FieldRule{},
			Map:            map[string]string{},
		},
	}

	_ = pack1.Validate()
	_ = pack2.Validate()

	snap := MustNewSnapshot("test-snap", []*ParserPack{pack1, pack2})

	rec := &model.DecodedRecord{
		Format:     "syslog",
		RawPayload: []byte("syslog message with ambiguous-token"),
	}

	_, err := snap.Match(rec)
	if !errors.Is(err, ErrAmbiguousMatch) {
		t.Errorf("expected ErrAmbiguousMatch, got %v", err)
	}
}

func TestSnapshotManager_ActivationAndRollback(t *testing.T) {
	packA := &ParserPack{
		APIVersion: "worm.io/v1",
		Kind:       "LogSource",
		Metadata:   PackMetadata{Name: "pack-a", Version: "1.0.0"},
		Spec: PackSpec{
			SourceCategory: "application",
			Format:         "json",
			Match:          MatchRule{Contains: "service-a"},
			Fields:         map[string]FieldRule{},
			Map:            map[string]string{},
		},
	}
	packB := &ParserPack{
		APIVersion: "worm.io/v1",
		Kind:       "LogSource",
		Metadata:   PackMetadata{Name: "pack-b", Version: "1.0.0"},
		Spec: PackSpec{
			SourceCategory: "application",
			Format:         "json",
			Match:          MatchRule{Contains: "service-b"},
			Fields:         map[string]FieldRule{},
			Map:            map[string]string{},
		},
	}

	snap1 := MustNewSnapshot("v1", []*ParserPack{packA})
	snap2 := MustNewSnapshot("v2", []*ParserPack{packA, packB})

	mgr := NewSnapshotManager(snap1)
	if mgr.Active().Version() != "v1" {
		t.Errorf("expected active v1, got %s", mgr.Active().Version())
	}

	// Activate snap2
	if err := mgr.Activate(snap2); err != nil {
		t.Fatalf("Activate failed: %v", err)
	}
	if mgr.Active().Version() != "v2" {
		t.Errorf("expected active v2, got %s", mgr.Active().Version())
	}
	if len(mgr.Active().ListPacks()) != 2 {
		t.Errorf("expected 2 packs in snap2, got %d", len(mgr.Active().ListPacks()))
	}

	// Rollback
	if err := mgr.Rollback(); err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}
	if mgr.Active().Version() != "v1" {
		t.Errorf("expected rolled back version v1, got %s", mgr.Active().Version())
	}
	if len(mgr.Active().ListPacks()) != 1 {
		t.Errorf("expected 1 pack after rollback, got %d", len(mgr.Active().ListPacks()))
	}

	// Second rollback should fail (only 1 level preserved)
	if err := mgr.Rollback(); err == nil {
		t.Errorf("expected second rollback to fail, got nil")
	}
}

func TestExtractAndConvert(t *testing.T) {
	pack := &ParserPack{
		APIVersion: "worm.io/v1",
		Kind:       "LogSource",
		Metadata:   PackMetadata{Name: "extractor-test", Version: "1.0.0"},
		Spec: PackSpec{
			SourceCategory:   "network_device",
			Format:           "syslog",
			Match:            MatchRule{Contains: "PAN-OS"},
			PreserveUnmapped: true,
			Fields: map[string]FieldRule{
				"src": {
					From:     "src",
					Type:     "ip",
					Required: true,
				},
				"sport": {
					From: "sport",
					Type: "integer",
				},
				"time": {
					From:   "time",
					Type:   "timestamp",
					Layout: "2006-01-02T15:04:05-07:00",
				},
				"bytes": {
					From: "bytes",
					Type: "float",
				},
				"active": {
					From: "active",
					Type: "boolean",
				},
				"action": {
					From: "action",
					Type: "string",
				},
			},
			Map: map[string]string{
				"src": "event.src_endpoint.ip",
			},
		},
	}

	rec := &model.DecodedRecord{
		Format: "syslog",
		Fields: map[string]any{
			"src":        "192.168.1.100",
			"sport":      "8080",
			"time":       "2026-09-20T10:05:23+05:30",
			"bytes":      "15234.5",
			"active":     "true",
			"action":     "allow",
			"unmapped_1": "custom_vendor_tag",
			"unmapped_2": 999,
		},
	}

	extracted, unmapped, warnings, err := ExtractAndConvert(pack, rec)
	if err != nil {
		t.Fatalf("ExtractAndConvert failed: %v", err)
	}
	_ = warnings

	// Verify types
	if ip, ok := extracted["src"].(string); !ok || ip != "192.168.1.100" {
		t.Errorf("expected IP 192.168.1.100, got %v", extracted["src"])
	}
	if port, ok := extracted["sport"].(int64); !ok || port != 8080 {
		t.Errorf("expected int64 8080, got %v (%T)", extracted["sport"], extracted["sport"])
	}
	if ts, ok := extracted["time"].(int64); !ok || ts <= 0 {
		t.Errorf("expected valid epoch ms timestamp, got %v", extracted["time"])
	}
	if b, ok := extracted["bytes"].(float64); !ok || b != 15234.5 {
		t.Errorf("expected float64 15234.5, got %v", extracted["bytes"])
	}
	if act, ok := extracted["active"].(bool); !ok || !act {
		t.Errorf("expected boolean true, got %v", extracted["active"])
	}
	if action, ok := extracted["action"].(string); !ok || action != "allow" {
		t.Errorf("expected action allow, got %v", extracted["action"])
	}

	// Verify unmapped fields
	if unmapped["unmapped_1"] != "custom_vendor_tag" {
		t.Errorf("expected unmapped_1 to be preserved, got %v", unmapped["unmapped_1"])
	}
	if unmapped["unmapped_2"] != 999 {
		t.Errorf("expected unmapped_2 to be preserved, got %v", unmapped["unmapped_2"])
	}
	// Mapped fields should not be in unmapped
	if _, exists := unmapped["src"]; exists {
		t.Errorf("mapped field 'src' should not appear in unmapped")
	}
}

func TestExtractAndConvert_RequiredMissing(t *testing.T) {
	pack := &ParserPack{
		APIVersion: "worm.io/v1",
		Kind:       "LogSource",
		Metadata:   PackMetadata{Name: "req-test", Version: "1.0.0"},
		Spec: PackSpec{
			SourceCategory: "network_device",
			Format:         "syslog",
			Match:          MatchRule{Contains: "PAN-OS"},
			Fields: map[string]FieldRule{
				"mandatory_field": {
					From:     "mandatory_field",
					Type:     "string",
					Required: true,
				},
			},
			Map: map[string]string{},
		},
	}

	rec := &model.DecodedRecord{
		Format: "syslog",
		Fields: map[string]any{"other_field": "val"},
	}

	_, _, _, err := ExtractAndConvert(pack, rec)
	if err == nil {
		t.Errorf("expected error when required field is missing, got nil")
	}
}

func TestSnapshot_DuplicatePackRejection_BA014(t *testing.T) {
	pack1 := &ParserPack{
		APIVersion: "worm.io/v1",
		Kind:       "LogSource",
		Metadata:   PackMetadata{Name: "duplicate-pack", Version: "1.0.0"},
		Spec: PackSpec{
			SourceCategory: "application",
			Format:         "json",
			Match:          MatchRule{Contains: "app1"},
			Fields:         map[string]FieldRule{},
		},
	}
	pack2 := &ParserPack{
		APIVersion: "worm.io/v1",
		Kind:       "LogSource",
		Metadata:   PackMetadata{Name: "duplicate-pack", Version: "1.0.1"},
		Spec: PackSpec{
			SourceCategory: "application",
			Format:         "json",
			Match:          MatchRule{Contains: "app2"},
			Fields:         map[string]FieldRule{},
		},
	}

	_, err := NewSnapshot("v-dup", []*ParserPack{pack1, pack2})
	if err == nil {
		t.Fatalf("expected error when duplicate pack name is provided to NewSnapshot, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate parser pack name") {
		t.Errorf("unexpected error message: %v", err)
	}

	snap := MustNewSnapshot("v-single", []*ParserPack{pack1})
	if snap.Digest() == "" {
		t.Errorf("expected non-empty digest for snapshot")
	}
}

func TestPackValidation_BA013(t *testing.T) {
	// 1. Invalid pack name
	packInvalidName := &ParserPack{
		APIVersion: "worm.io/v1",
		Kind:       "LogSource",
		Metadata:   PackMetadata{Name: "Invalid_Pack_Name!", Version: "1.0.0"},
		Spec: PackSpec{
			SourceCategory: "application",
			Format:         "json",
			Match:          MatchRule{Contains: "foo"},
			Fields:         map[string]FieldRule{},
		},
	}
	if err := packInvalidName.Validate(); err == nil {
		t.Errorf("expected error for invalid pack name regex, got nil")
	}

	// 2. Invalid semver
	packInvalidSemver := &ParserPack{
		APIVersion: "worm.io/v1",
		Kind:       "LogSource",
		Metadata:   PackMetadata{Name: "valid-pack", Version: "1-not-semver"},
		Spec: PackSpec{
			SourceCategory: "application",
			Format:         "json",
			Match:          MatchRule{Contains: "foo"},
			Fields:         map[string]FieldRule{},
		},
	}
	if err := packInvalidSemver.Validate(); err == nil {
		t.Errorf("expected error for invalid semver, got nil")
	}

	// 3. Map target not starting with event.
	packInvalidMapTarget := &ParserPack{
		APIVersion: "worm.io/v1",
		Kind:       "LogSource",
		Metadata:   PackMetadata{Name: "valid-pack", Version: "1.0.0"},
		Spec: PackSpec{
			SourceCategory: "application",
			Format:         "json",
			Match:          MatchRule{Contains: "foo"},
			Fields: map[string]FieldRule{
				"raw_f": {From: "raw_f", Type: "string"},
			},
			Map: map[string]string{
				"raw_f": "invalid.target.path",
			},
		},
	}
	if err := packInvalidMapTarget.Validate(); err == nil {
		t.Errorf("expected error for map target not starting with 'event.', got nil")
	}
}

func TestNestedFieldLookup_BA015(t *testing.T) {
	rec := &model.DecodedRecord{
		Format: "json",
		Fields: map[string]any{
			"user": map[string]any{
				"profile": map[string]any{
					"email": "user@example.com",
					"id":    12345,
				},
			},
			"status": "active",
		},
	}

	// Test nested lookup
	val, ok := LookupField(rec, "user.profile.email")
	if !ok || val != "user@example.com" {
		t.Errorf("expected 'user@example.com', got %v (found=%v)", val, ok)
	}

	idVal, ok := LookupField(rec, "user.profile.id")
	if !ok || idVal != 12345 {
		t.Errorf("expected 12345, got %v (found=%v)", idVal, ok)
	}

	// Test flat lookup still works
	statusVal, ok := LookupField(rec, "status")
	if !ok || statusVal != "active" {
		t.Errorf("expected 'active', got %v (found=%v)", statusVal, ok)
	}

	// Non-existent path
	_, ok = LookupField(rec, "user.profile.nonexistent")
	if ok {
		t.Errorf("expected nonexistent path to return false")
	}
}
