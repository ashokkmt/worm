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
kind: Connection
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

	// Invalid type
	invalidTypeYAML := `
apiVersion: worm.io/v1
kind: Connection
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
kind: Connection
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
kind: Connection
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
		Kind:       "Connection",
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
