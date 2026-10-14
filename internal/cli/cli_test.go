package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDaemonStartupBanner(t *testing.T) {
	for _, tc := range []struct {
		name              string
		uiAddr            string
		marketplaceOnline bool
		wantUI            string
		wantMarketplace   string
	}{
		{name: "online", uiAddr: "127.0.0.1:19090", marketplaceOnline: true, wantUI: "http://127.0.0.1:19090", wantMarketplace: "online (network access only for explicit marketplace commands)"},
		{name: "offline and UI disabled", uiAddr: "none", wantUI: "disabled", wantMarketplace: "offline (bundled catalog and verified cache only)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := daemonStartupBanner(123, "./worm", tc.uiAddr, "data/worm.log", "packs", tc.marketplaceOnline, "https://registry.example.test")
			for _, want := range []string{tc.wantUI, tc.wantMarketplace, "./worm packs apply -f <pack.yaml>", "./worm marketplace refresh", "./worm packs install <name>[@version]", "https://registry.example.test"} {
				if !strings.Contains(got, want) {
					t.Errorf("daemon startup banner missing %q:\n%s", want, got)
				}
			}
		})
	}
}

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
