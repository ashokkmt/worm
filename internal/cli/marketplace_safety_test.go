package cli

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"worm/internal/marketplace"
	"worm/internal/packs"
)

func TestLocalMarketplaceInstallRejectsEditedPack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pack.yaml")
	if err := os.WriteFile(path, []byte("edited"), 0600); err != nil {
		t.Fatal(err)
	}
	entry := packs.PackInstall{Name: "sample", Filename: "pack.yaml", Digest: marketplace.Digest([]byte("published"))}
	if _, err := readUnmodifiedMarketplacePack(dir, entry); err == nil || !strings.Contains(err.Error(), "modified locally") {
		t.Fatalf("edited pack check error = %v", err)
	}
	if err := os.WriteFile(path, []byte("published"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readUnmodifiedMarketplacePack(dir, entry); err != nil {
		t.Fatalf("unchanged published pack rejected: %v", err)
	}
}

func TestLocalMarketplaceUpgradeKeepsUnpinnedPackUnpinned(t *testing.T) {
	dir := t.TempDir()
	const name = "network-device-asa"
	oldBytes, err := os.ReadFile(filepath.Join("..", "..", "marketplace", "releases", name, "1.0.3.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	newBytes, err := os.ReadFile(filepath.Join("..", "..", "packs", name+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".yaml"), oldBytes, 0600); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(dir, ".marketplace", "artifacts", name, "1.0.4", "pack.yaml")
	if err := os.MkdirAll(filepath.Dir(artifact), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, newBytes, 0600); err != nil {
		t.Fatal(err)
	}
	manifest := packs.EmptyManifest()
	manifest.Entries[name] = packs.PackInstall{Name: name, Filename: name + ".yaml", Version: "1.0.3", Digest: marketplace.Digest(oldBytes), Origin: "marketplace", Pinned: false}
	if err := packs.WriteManifest(dir, manifest); err != nil {
		t.Fatal(err)
	}
	UpgradeMarketplacePackLocalCLI(name, dir)
	updated, err := packs.ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	entry := updated.Entries[name]
	if entry.Version != "1.0.4" || entry.Pinned {
		t.Fatalf("upgraded install state = %+v, want 1.0.4 and unpinned", entry)
	}
}

func TestReplayRequestUsesAdminTokenAndRejectsBadResponses(t *testing.T) {
	SetMarketplaceAdminToken("test-replay-token")
	t.Cleanup(func() { SetMarketplaceAdminToken("") })
	req, err := newReplayRequest("http://localhost/replay")
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer test-replay-token" {
		t.Fatalf("Authorization = %q", got)
	}
	for _, resp := range []*http.Response{
		{StatusCode: http.StatusUnauthorized, Status: "401 Unauthorized", Body: io.NopCloser(strings.NewReader("unauthorized"))},
		{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader("not-json"))},
	} {
		var result map[string]any
		if err := decodeReplayResponse(resp, &result); err == nil {
			t.Fatalf("accepted replay error response: %+v", resp)
		}
	}
}
