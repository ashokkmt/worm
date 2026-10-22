package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"worm/internal/marketplace"
)

func TestWritePackIndexesListsPacksAndVersions(t *testing.T) {
	site := t.TempDir()
	catalog := &marketplace.Catalog{Packs: []marketplace.Pack{
		{
			Name: "sample", Description: "Sample parser", Vendor: "Acme", Product: "Logs", Category: "system", Format: "json",
			Releases: []marketplace.Release{
				{Version: "1.0.0", Artifact: "packs/sample/1.0.0/pack.yaml"},
				{Version: "1.1.0", Artifact: "packs/sample/1.1.0/pack.yaml", Changelog: "Improved matching"},
			},
		},
		{Name: "withdrawn"},
	}}
	if err := writePackIndexes(site, catalog); err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(filepath.Join(site, "packs", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), `href="sample/index.html"`) || strings.Contains(string(index), "withdrawn") {
		t.Fatalf("pack index has unexpected contents: %s", index)
	}
	versions, err := os.ReadFile(filepath.Join(site, "packs", "sample", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`href="../index.html"`, `href="1.0.0/pack.yaml"`, `href="1.1.0/pack.yaml"`, "Improved matching"} {
		if !strings.Contains(string(versions), want) {
			t.Errorf("pack versions page missing %q", want)
		}
	}
}

func TestWriteRepositoryIndexIncludesPackCoverageAndReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "INDEX.md")
	catalog := &marketplace.Catalog{Packs: []marketplace.Pack{{
		Name: "sample", Description: "Sample parser", Vendor: "Acme", Product: "Audit", Category: "identity", Format: "json", Models: []string{"v2"}, Tags: []string{"login"},
		Releases: []marketplace.Release{{Version: "1.0.0", Artifact: "packs/sample/1.0.0/pack.yaml", Changelog: "Initial release", MinWORM: "1.0.0", PackAPI: "worm.io/v1", OCSF: "1.3.0"}},
	}}}
	if err := writeRepositoryIndex(path, catalog); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1 unique pack identities", "1 published releases", "Sample parser", "1.0.0", "Acme / Audit", "identity", "json", "v2", "login", "Initial release"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("index missing %q", want)
		}
	}
}
