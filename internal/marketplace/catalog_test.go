package marketplace

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func signedTestCatalog(t *testing.T, c Catalog) []byte {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKeys["test"] = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { delete(publicKeys, "test") })
	payload, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	env, _ := json.Marshal(Envelope{KeyID: "test", Payload: base64.StdEncoding.EncodeToString(payload), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))})
	return env
}

func TestVerifySignedCatalogAndRejectTampering(t *testing.T) {
	r := Release{Version: "1.2.0", Artifact: "packs/demo/1.2.0/pack.yaml", SHA256: Digest([]byte("yaml")), Size: 4, PackAPI: "worm.io/v1", OCSF: "1.3.0"}
	c := Catalog{Version: CatalogVersion, Packs: []Pack{{Name: "demo", Releases: []Release{r}}}}
	raw := signedTestCatalog(t, c)
	if _, err := Verify(raw); err != nil {
		t.Fatal(err)
	}
	for i := range raw {
		altered := append([]byte(nil), raw...)
		altered[i] ^= 1
		if _, err := Verify(altered); err == nil {
			t.Fatalf("tampering at byte %d was accepted", i)
		}
	}
}

func TestOwnerSigningKeysAreTrusted(t *testing.T) {
	if _, ok := publicKeys[OwnerSigningKeyID]; !ok {
		t.Fatalf("owner signing key %q is not trusted", OwnerSigningKeyID)
	}
	for id, encoded := range publicKeys {
		key, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(key) != ed25519.PublicKeySize {
			t.Fatalf("trusted key %q is invalid", id)
		}
	}
}

func TestCatalogSearchLatestAndArtifactDigest(t *testing.T) {
	p := Pack{Name: "demo", Vendor: "Acme", Category: "network_device", Format: "json", Releases: []Release{{Version: "1.9.0", SHA256: Digest([]byte("a")), Size: 1}, {Version: "1.10.0", SHA256: Digest([]byte("b")), Size: 1}, {Version: "2.0.0-rc.1", SHA256: Digest([]byte("c")), Size: 1}}}
	r, ok := p.Latest()
	if !ok || r.Version != "1.10.0" {
		t.Fatalf("unexpected latest release: %+v %v", r, ok)
	}
	c := Catalog{Packs: []Pack{p}}
	if got := c.Search("acme", "network_device", "json"); len(got) != 1 {
		t.Fatalf("search returned %d packs", len(got))
	}
	if err := r.VerifyArtifact([]byte("x")); err == nil {
		t.Fatal("bad artifact digest accepted")
	}
}

func TestCatalogRejectsUnsafePathsAndDuplicateNames(t *testing.T) {
	c := Catalog{Version: CatalogVersion, Packs: []Pack{{Name: "demo", Releases: []Release{{Version: "1.0.0", Artifact: "packs/../evil", SHA256: Digest(nil), PackAPI: "v1", OCSF: "1.3.0"}}}}}
	if err := c.Validate(); err == nil {
		t.Fatal("unsafe artifact path accepted")
	}
	c.Packs = append(c.Packs, c.Packs[0])
	if err := c.Validate(); err == nil {
		t.Fatal("duplicate pack accepted")
	}
}

func TestOfflineSnapshotExportAndImport(t *testing.T) {
	c, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	installed := filepath.Join("..", "..", "packs")
	for _, p := range c.Packs {
		data, e := os.ReadFile(filepath.Join(installed, p.Name+".yaml"))
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(source, p.Name+".yaml"), data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	exportDir := filepath.Join(t.TempDir(), "bundle")
	if err = ExportSnapshot(c, source, exportDir); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err = ImportSnapshot(exportDir, dest); err != nil {
		t.Fatal(err)
	}
	loaded, err := (LocalStore{Dir: dest}).ReadCatalog()
	if err != nil || len(loaded.Packs) != len(c.Packs) {
		t.Fatalf("imported catalog: %v", err)
	}
	path := filepath.Join(exportDir, filepath.FromSlash(c.Packs[0].Releases[0].Artifact))
	if err = os.WriteFile(path, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = ImportSnapshot(exportDir, t.TempDir()); err == nil {
		t.Fatal("tampered offline artifact imported")
	}
}
