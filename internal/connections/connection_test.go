package connections

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConnection_Validate(t *testing.T) {
	validYAML := `
apiVersion: worm.io/v1
kind: Sink
metadata:
  name: test-siem
  version: 1.0.0
spec:
  type: http_output
  enabled: true
  endpoint:
    url: http://127.0.0.1:8080/events
`
	conn, err := LoadConnection(strings.NewReader(validYAML))
	if err != nil {
		t.Fatalf("LoadConnection failed on valid YAML: %v", err)
	}
	if conn.Metadata.Name != "test-siem" || conn.Spec.Type != "http_output" {
		t.Errorf("unexpected parsed connection: %+v", conn)
	}

	// Legacy kind: Connection rejected
	legacyYAML := `
apiVersion: worm.io/v1
kind: Connection
metadata:
  name: legacy-conn
  version: 1.0.0
spec:
  type: http_output
  enabled: true
  endpoint:
    url: http://127.0.0.1:8080/events
`
	_, err = LoadConnection(strings.NewReader(legacyYAML))
	if err == nil {
		t.Fatalf("expected error for legacy kind: Connection, got nil")
	}

	// Invalid type
	invalidTypeYAML := `
apiVersion: worm.io/v1
kind: Sink
metadata:
  name: invalid-type
  version: 1.0.0
spec:
  type: unknown_sink
  enabled: true
`
	_, err = LoadConnection(strings.NewReader(invalidTypeYAML))
	if err == nil {
		t.Fatalf("expected error for unknown connection type, got nil")
	}

	// Missing endpoint URL for http_output
	missingURL := `
apiVersion: worm.io/v1
kind: Sink
metadata:
  name: missing-url
  version: 1.0.0
spec:
  type: http_output
  enabled: true
`
	_, err = LoadConnection(strings.NewReader(missingURL))
	if err == nil {
		t.Fatalf("expected error for missing endpoint url in http_output, got nil")
	}
}

func TestConnectionManager_ApplyAndRollback(t *testing.T) {
	tempDir := t.TempDir()
	mgr := NewManager(tempDir)

	manifestYAML := `apiVersion: worm.io/v1
kind: Sink
metadata:
  name: test-parquet
  version: 1.0.0
spec:
  type: parquet_output
  enabled: true
  path: /tmp/parquet
`
	// 1. Apply file
	applied, err := mgr.ApplyFile("test-parquet.yaml", []byte(manifestYAML))
	if err != nil {
		t.Fatalf("ApplyFile failed: %v", err)
	}
	if applied.Metadata.Name != "test-parquet" {
		t.Errorf("expected name test-parquet, got %s", applied.Metadata.Name)
	}

	// Verify on disk
	diskPath := filepath.Join(tempDir, "test-parquet.yaml")
	if _, err := os.Stat(diskPath); err != nil {
		t.Fatalf("applied file not found on disk: %v", err)
	}

	// Verify in manager list
	list := mgr.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 connection in manager, got %d", len(list))
	}

	// 2. Rollback
	if err := mgr.Rollback(); err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}

	// Verify removed from disk
	if _, err := os.Stat(diskPath); !os.IsNotExist(err) {
		t.Errorf("expected file to be removed after rollback")
	}

	// Verify removed from manager
	if len(mgr.List()) != 0 {
		t.Errorf("expected 0 connections in manager after rollback, got %d", len(mgr.List()))
	}
}

func TestConnectionManager_ReconcilesApplyRollbackAndFailure(t *testing.T) {
	tempDir := t.TempDir()
	mgr := NewManager(tempDir)
	var snapshots [][]string
	mgr.SetReconciler(func(conns []*Connection) error {
		names := make([]string, len(conns))
		for i, c := range conns {
			names[i] = c.Metadata.Name
		}
		snapshots = append(snapshots, names)
		if len(names) == 1 && names[0] == "reject-me" {
			return context.Canceled
		}
		return nil
	})

	manifest := func(name string) []byte {
		return []byte("apiVersion: worm.io/v1\nkind: Sink\nmetadata:\n  name: " + name + "\n  version: 1.0.0\nspec:\n  type: ndjson_output\n  enabled: true\n  path: " + filepath.Join(tempDir, name+".ndjson") + "\n")
	}
	if _, err := mgr.ApplyFile("live.yaml", manifest("live")); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(snapshots) != 1 || len(snapshots[0]) != 1 || snapshots[0][0] != "live" {
		t.Fatalf("apply did not reconcile live snapshot: %#v", snapshots)
	}
	if err := mgr.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if len(snapshots) != 2 || len(snapshots[1]) != 0 {
		t.Fatalf("rollback did not reconcile empty snapshot: %#v", snapshots)
	}
	if _, err := mgr.ApplyFile("rejected.yaml", manifest("reject-me")); err == nil {
		t.Fatal("expected failed live reconciliation")
	}
	if len(mgr.List()) != 0 {
		t.Fatal("failed reconciliation changed manager state")
	}
	if _, err := os.Stat(filepath.Join(tempDir, "rejected.yaml")); !os.IsNotExist(err) {
		t.Fatalf("failed reconciliation left staged manifest: %v", err)
	}
}

func TestConnectionManager_RejectsDuplicateIdentity(t *testing.T) {
	dir := t.TempDir()
	body := func(kind string) []byte {
		return []byte("apiVersion: worm.io/v1\nkind: " + kind + "\nmetadata:\n  name: duplicate\n  version: 1.0.0\nspec:\n  type: ndjson_output\n  enabled: true\n  path: out.ndjson\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "one.yaml"), body("Sink"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "two.yaml"), body("Sink"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := NewManager(dir).LoadDir(dir); err == nil || !strings.Contains(err.Error(), "duplicate connection identity") {
		t.Fatalf("expected duplicate identity error, got %v", err)
	}
}

func TestConnectionManager_ApplyRejectsDuplicateIdentityAndCanRenameFile(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	manifest := func(name string) []byte {
		return []byte("apiVersion: worm.io/v1\nkind: Sink\nmetadata:\n  name: " + name + "\n  version: 1.0.0\nspec:\n  type: ndjson_output\n  enabled: true\n  path: " + filepath.Join(dir, name+".ndjson") + "\n")
	}
	if _, err := mgr.ApplyFile("one.yaml", manifest("one")); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.ApplyFile("two.yaml", manifest("one")); err == nil {
		t.Fatal("expected duplicate name in a different file to fail")
	}
	if _, err := mgr.ApplyFile("one.yaml", manifest("renamed")); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Get("one"); err == nil {
		t.Fatal("old identity remained after same-file rename")
	}
	if _, err := mgr.Get("renamed"); err != nil {
		t.Fatal(err)
	}
}

func TestConnection_TestConnectivity(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// Parquet local path connectivity check
	conn := &Connection{
		APIVersion: "worm.io/v1",
		Kind:       "Sink",
		Metadata:   ConnectionMetadata{Name: "parquet-check", Version: "1.0.0"},
		Spec: ConnectionSpec{
			Type:    "parquet_output",
			Enabled: true,
			Path:    filepath.Join(dir, "lake"),
		},
	}
	if err := conn.TestConnectivity(ctx); err != nil {
		t.Errorf("parquet connectivity test failed: %v", err)
	}
	ndjsonPath := filepath.Join(dir, "events.ndjson")
	conn.Spec.Type = "ndjson_output"
	conn.Spec.Path = ndjsonPath
	if err := conn.TestConnectivity(ctx); err != nil {
		t.Errorf("ndjson connectivity test failed: %v", err)
	}
	if info, err := os.Stat(ndjsonPath); err == nil && info.IsDir() {
		t.Fatal("NDJSON connectivity check created the output file path as a directory")
	}
}

func TestSourceAndSinkValidation(t *testing.T) {
	// 1. Valid Source
	validSource := `
apiVersion: worm.io/v1
kind: Source
metadata:
  name: cloudtrail-source
  version: 1.0.0
spec:
  type: cloud_pull
  enabled: true
  endpoint:
    url: https://audit.us-east-1.cloud/logs
`
	src, err := LoadSource(strings.NewReader(validSource))
	if err != nil {
		t.Fatalf("LoadSource failed on valid Source: %v", err)
	}
	if src.Kind != "Source" || src.Spec.Type != "cloud_pull" {
		t.Errorf("unexpected parsed source: %+v", src)
	}

	// 2. Source with Sink type rejected
	invalidSource := `
apiVersion: worm.io/v1
kind: Source
metadata:
  name: bad-source
  version: 1.0.0
spec:
  type: http_output
  enabled: true
  endpoint:
    url: https://siem.corp/events
`
	_, err = LoadSource(strings.NewReader(invalidSource))
	if err == nil {
		t.Fatal("expected error loading Source with http_output type, got nil")
	}

	// 3. Valid Sink
	validSink := `
apiVersion: worm.io/v1
kind: Sink
metadata:
  name: splunk-sink
  version: 1.0.0
spec:
  type: http_output
  enabled: true
  endpoint:
    url: https://splunk:8088/services/collector/event
`
	snk, err := LoadSink(strings.NewReader(validSink))
	if err != nil {
		t.Fatalf("LoadSink failed on valid Sink: %v", err)
	}
	if snk.Kind != "Sink" || snk.Spec.Type != "http_output" {
		t.Errorf("unexpected parsed sink: %+v", snk)
	}

	// 4. Sink with Source type rejected
	invalidSink := `
apiVersion: worm.io/v1
kind: Sink
metadata:
  name: bad-sink
  version: 1.0.0
spec:
  type: cloud_pull
  enabled: true
  endpoint:
    url: https://audit.cloud/logs
`
	_, err = LoadSink(strings.NewReader(invalidSink))
	if err == nil {
		t.Fatal("expected error loading Sink with cloud_pull type, got nil")
	}
}
