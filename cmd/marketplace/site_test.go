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
