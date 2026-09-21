package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetBaseURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "http://localhost:9090"},
		{"none", "http://localhost:9090"},
		{":8080", "http://localhost:8080"},
		{"127.0.0.1:9090", "http://127.0.0.1:9090"},
		{"http://localhost:9090", "http://localhost:9090"},
		{"https://example.com", "https://example.com"},
	}

	for _, tc := range tests {
		got := GetBaseURL(tc.input)
		if got != tc.want {
			t.Errorf("GetBaseURL(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestReadPID(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "test.pid")

	// Missing file
	_, err := ReadPID(pidFile)
	if err == nil {
		t.Errorf("expected error for missing PID file")
	}

	// Invalid content
	if err := os.WriteFile(pidFile, []byte("not_a_number"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err = ReadPID(pidFile)
	if err == nil {
		t.Errorf("expected error for invalid PID content")
	}

	// Valid content
	if err := os.WriteFile(pidFile, []byte(" 12345 \n"), 0644); err != nil {
		t.Fatal(err)
	}
	pid, err := ReadPID(pidFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pid != 12345 {
		t.Errorf("ReadPID() = %d, want 12345", pid)
	}
}

func TestIsPIDAlive(t *testing.T) {
	// Current process must be alive
	currentPID := os.Getpid()
	if !IsPIDAlive(currentPID) {
		t.Errorf("expected current PID %d to be alive", currentPID)
	}

	// Non-positive PID
	if IsPIDAlive(0) {
		t.Errorf("expected PID 0 to be dead")
	}
	if IsPIDAlive(-1) {
		t.Errorf("expected negative PID to be dead")
	}

	// Likely non-existent PID
	if IsPIDAlive(99999999) {
		t.Errorf("expected PID 99999999 to be dead")
	}
}
