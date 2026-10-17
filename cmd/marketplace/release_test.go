package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"worm/internal/marketplace"
)

func TestNextPatchVersion(t *testing.T) {
	for input, want := range map[string]string{"1.0.0": "1.0.1", "v2.4.9": "v2.4.10"} {
		got, err := nextPatchVersion(input)
		if err != nil || got != want {
			t.Fatalf("nextPatchVersion(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := nextPatchVersion("1.0"); err == nil {
		t.Fatal("nextPatchVersion accepted an incomplete version")
	}
}

func TestSetMetadataVersionPreservesOtherYAML(t *testing.T) {
	input := []byte("apiVersion: worm.io/v1\nmetadata:\n  name: sample\n  version: '1.0.0' # previous 1.0.0\nspec:\n  version: unchanged\n")
	got, err := setMetadataVersion(input, "1.0.1")
	if err != nil {
		t.Fatal(err)
	}
	want := "apiVersion: worm.io/v1\nmetadata:\n  name: sample\n  version: '1.0.1' # previous 1.0.0\nspec:\n  version: unchanged\n"
	if string(got) != want {
		t.Fatalf("updated YAML differs\n got: %s\nwant: %s", got, want)
	}
}

func TestReadOrFetchArchiveVerifiesCatalogDigest(t *testing.T) {
	data := []byte("prior signed release bytes")
	sum := sha256.Sum256(data)
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/packs/sample/1.0.0/pack.yaml" {
			return nil, io.ErrUnexpectedEOF
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(string(data))), Header: make(http.Header)}, nil
	})}
	release := marketplace.Release{
		Version: "1.0.0", Artifact: "packs/sample/1.0.0/pack.yaml",
		SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data)),
	}
	got, err := readOrFetchArchive(client, "https://registry.example", t.TempDir()+"/archive.yaml", release)
	if err != nil || string(got) != string(data) {
		t.Fatalf("readOrFetchArchive() = %q, %v", got, err)
	}
	release.SHA256 = strings.Repeat("0", 64)
	if _, err := readOrFetchArchive(client, "https://registry.example", t.TempDir()+"/archive.yaml", release); err == nil {
		t.Fatal("readOrFetchArchive accepted an artifact with the wrong digest")
	}
}

func TestPrepareReleasesArchivesBumpsAndUpdatesMetadata(t *testing.T) {
	root := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)
	packDir := filepath.Join(root, "packs")
	archivePath := filepath.Join("marketplace", "releases", "release-fixture", "1.0.0.yaml")
	metadataPath := filepath.Join(root, "marketplace", "metadata.json")
	if err := os.MkdirAll(packDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(metadataPath), 0755); err != nil {
		t.Fatal(err)
	}
	oldPack := []byte("apiVersion: worm.io/v1\nkind: LogSource\nmetadata:\n  name: release-fixture\n  version: 1.0.0\n  description: test\n  author: test\nspec:\n  sourceCategory: cloud\n  format: text\n  match:\n    regex: '^old$'\n  fields:\n    message:\n      from: message\n      type: string\n  map:\n    message: event.message\n")
	packPath := filepath.Join(packDir, "release-fixture.yaml")
	if err := os.WriteFile(packPath, oldPack, 0644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(oldPack)
	catalog := &marketplace.Catalog{Packs: []marketplace.Pack{{
		Name:     "release-fixture",
		Releases: []marketplace.Release{{Version: "1.0.0", Artifact: "packs/release-fixture/1.0.0/pack.yaml", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(oldPack))}},
	}}}
	metadata := ownerMetadata{Packs: map[string]packCoverage{"release-fixture": {Product: "fixture"}}, Releases: map[string]releaseMetadata{"release-fixture@1.0.0": {Changelog: "initial", RequiredDecoders: []string{"text"}}}}
	metadataBytes, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, metadataBytes, 0644); err != nil {
		t.Fatal(err)
	}
	if err := prepareReleaseFiles(packDir, metadataPath, "https://unused.example", catalog, []string{"release-fixture.yaml"}, "parser fix"); err != nil {
		t.Fatal(err)
	}
	packBytes, err := os.ReadFile(packPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(packBytes), "version: 1.0.1") {
		t.Fatalf("pack version was not bumped: %s", packBytes)
	}
	archivedBytes, err := os.ReadFile(archivePath)
	if err != nil || string(archivedBytes) != string(oldPack) {
		t.Fatalf("prior release was not preserved byte-for-byte: %v", err)
	}
	userEditedPack := []byte(strings.Replace(string(packBytes), "^old$", "^new$", 1))
	if err := os.WriteFile(packPath, userEditedPack, 0644); err != nil {
		t.Fatal(err)
	}
	if err := prepareReleaseFiles(packDir, metadataPath, "https://unused.example", catalog, []string{"release-fixture.yaml"}, "parser fix"); err != nil {
		t.Fatalf("repeat preparation after editing should be safe: %v", err)
	}
	packBytes, err = os.ReadFile(packPath)
	if err != nil || !strings.Contains(string(packBytes), "regex: '^new$'") {
		t.Fatalf("repeat preparation overwrote the user's edit: %v", err)
	}
	updatedMetadata, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	var got ownerMetadata
	if err := json.Unmarshal(updatedMetadata, &got); err != nil {
		t.Fatal(err)
	}
	if got.Releases["release-fixture@1.0.1"].Changelog != "parser fix" {
		t.Fatalf("new release metadata missing changelog: %+v", got.Releases)
	}
}

func TestPrepareReleaseRecoversPriorArtifactFromGeneratedSite(t *testing.T) {
	root := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)
	packDir := filepath.Join(root, "packs")
	metadataPath := filepath.Join(root, "marketplace", "metadata.json")
	if err := os.MkdirAll(packDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(metadataPath), 0755); err != nil {
		t.Fatal(err)
	}
	oldPack := []byte("apiVersion: worm.io/v1\nkind: LogSource\nmetadata:\n  name: release-fixture\n  version: 1.0.0\n  description: test\n  author: test\nspec:\n  sourceCategory: cloud\n  format: text\n  match:\n    regex: '^old$'\n  fields:\n    message:\n      from: message\n      type: string\n  map:\n    message: event.message\n")
	currentPack := []byte(strings.Replace(string(oldPack), "^old$", "^edited$", 1))
	packPath := filepath.Join(packDir, "release-fixture.yaml")
	if err := os.WriteFile(packPath, currentPack, 0644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(oldPack)
	release := marketplace.Release{Version: "1.0.0", Artifact: "packs/release-fixture/1.0.0/pack.yaml", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(oldPack))}
	catalog := &marketplace.Catalog{Packs: []marketplace.Pack{{Name: "release-fixture", Releases: []marketplace.Release{release}}}}
	metadata := ownerMetadata{Releases: map[string]releaseMetadata{"release-fixture@1.0.0": {Changelog: "initial"}}}
	metadataBytes, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, metadataBytes, 0644); err != nil {
		t.Fatal(err)
	}
	sitePath := filepath.Join("marketplace-site", filepath.FromSlash(release.Artifact))
	if err := os.MkdirAll(filepath.Dir(sitePath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sitePath, oldPack, 0644); err != nil {
		t.Fatal(err)
	}
	if err := prepareReleaseFiles(packDir, metadataPath, "https://unavailable.example", catalog, []string{"release-fixture.yaml"}, "parser fix"); err != nil {
		t.Fatal(err)
	}
	archived, err := os.ReadFile(filepath.Join("marketplace", "releases", "release-fixture", "1.0.0.yaml"))
	if err != nil || string(archived) != string(oldPack) {
		t.Fatalf("generated-site artifact was not archived exactly: %v", err)
	}
	updated, err := os.ReadFile(packPath)
	if err != nil || !strings.Contains(string(updated), "version: 1.0.1") || !strings.Contains(string(updated), "regex: '^edited$'") {
		t.Fatalf("preparation did not preserve edited YAML and bump its version: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
