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

func TestConnection_TestConnectivity(t *testing.T) {
	ctx := context.Background()

	// Parquet local path connectivity check
	conn := &Connection{
		APIVersion: "worm.io/v1",
		Kind:       "Sink",
		Metadata:   ConnectionMetadata{Name: "parquet-check", Version: "1.0.0"},
		Spec: ConnectionSpec{
			Type:    "parquet_output",
			Enabled: true,
			Path:    "/tmp/test-lake",
		},
	}
	if err := conn.TestConnectivity(ctx); err != nil {
		t.Errorf("parquet connectivity test failed: %v", err)
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
