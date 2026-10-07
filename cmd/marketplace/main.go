// Command marketplace builds and signs the static parser pack catalog.
package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"worm/internal/marketplace"
	"worm/internal/ocsf"
	"worm/internal/packs"
)

type packCoverage struct {
	Vendor  string   `json:"vendor"`
	Product string   `json:"product"`
	Models  []string `json:"models"`
	Tags    []string `json:"tags"`
}
type releaseMetadata struct {
	Changelog        string   `json:"changelog"`
	Withdrawn        bool     `json:"withdrawn"`
	MinWORM          string   `json:"min_worm"`
	RequiredDecoders []string `json:"required_decoders"`
	PublishedAt      string   `json:"published_at"`
}
type ownerMetadata struct {
	Packs    map[string]packCoverage    `json:"packs"`
	Releases map[string]releaseMetadata `json:"releases"`
}

func main() {
	packDir := flag.String("packs", "packs", "directory of parser pack YAML")
	metadataFile := flag.String("metadata", "marketplace/metadata.json", "owner-maintained vendor and model metadata")
	out := flag.String("out", "internal/marketplace/catalog.json", "signed catalog output file")
	site := flag.String("site", "marketplace-site", "static site output directory")
	keyFile := flag.String("key-file", "", "base64 Ed25519 private key file (or WORM_MARKETPLACE_SIGNING_KEY)")
	checkPacks := flag.Bool("check-packs", false, "verify shipped pack files match the embedded signed catalog")
	flag.Parse()
	if *checkPacks {
		checkSeedPacks(*packDir)
		return
	}
	keyText := os.Getenv("WORM_MARKETPLACE_SIGNING_KEY")
	if *keyFile != "" {
		b, err := os.ReadFile(*keyFile)
		fatal(err)
		keyText = strings.TrimSpace(string(b))
	}
	key, err := base64.StdEncoding.DecodeString(keyText)
	if err != nil || len(key) != ed25519.PrivateKeySize {
		fatal(fmt.Errorf("provide a valid base64 WORM_MARKETPLACE_SIGNING_KEY"))
	}
	if !marketplace.SigningKeyTrusted(marketplace.OwnerSigningKeyID, ed25519.PrivateKey(key)) {
		fatal(fmt.Errorf("signing key does not match trusted key %s", marketplace.OwnerSigningKeyID))
	}
	previous, prevErr := os.ReadFile(*out)
	var oldCatalog *marketplace.Catalog
	if prevErr == nil {
		oldCatalog, err = marketplace.Verify(previous)
		fatal(err)
	} else if !os.IsNotExist(prevErr) {
		fatal(prevErr)
	}

	entries, err := os.ReadDir(*packDir)
	fatal(err)
	cat := marketplace.Catalog{Version: marketplace.CatalogVersion, Generated: time.Now().UTC().Format(time.RFC3339)}
	metadata := ownerMetadata{Packs: map[string]packCoverage{}, Releases: map[string]releaseMetadata{}}
	if b, e := os.ReadFile(*metadataFile); e == nil {
		fatal(json.Unmarshal(b, &metadata))
		if metadata.Packs == nil {
			fatal(json.Unmarshal(b, &metadata.Packs))
		}
	} else if !os.IsNotExist(e) {
		fatal(e)
	}
	packIndex := map[string]int{}
	releaseSource := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(*packDir, entry.Name()))
		fatal(err)
		p, err := packs.LoadPack(strings.NewReader(string(data)))
		fatal(err)
		sum := sha256.Sum256(data)
		name := p.Metadata.Name
		cover := metadata.Packs[name]
		releaseMeta := metadata.Releases[name+"@"+p.Metadata.Version]
		cat.Packs = append(cat.Packs, marketplace.Pack{
			Name: name, Description: p.Metadata.Description, Publisher: p.Metadata.Author,
			Category: p.Spec.SourceCategory, Format: p.Spec.Format,
			Vendor: cover.Vendor, Product: cover.Product, Models: cover.Models,
			Tags:     append([]string{p.Spec.SourceCategory, p.Spec.Format}, cover.Tags...),
			Releases: []marketplace.Release{{Version: p.Metadata.Version, Artifact: fmt.Sprintf("packs/%s/%s/pack.yaml", name, p.Metadata.Version), SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data)), PackAPI: p.APIVersion, OCSF: ocsf.Version, Changelog: releaseMeta.Changelog, Withdrawn: releaseMeta.Withdrawn, MinWORM: releaseMeta.MinWORM, RequiredDecoders: releaseMeta.RequiredDecoders, PublishedAt: releaseMeta.PublishedAt}},
		})
		packIndex[name] = len(cat.Packs) - 1
		releaseSource[fmt.Sprintf("packs/%s/%s/pack.yaml", name, p.Metadata.Version)] = filepath.Join(*packDir, entry.Name())
	}
	if oldCatalog != nil {
		for _, oldPack := range oldCatalog.Packs {
			if _, exists := packIndex[oldPack.Name]; !exists {
				oldPack.Releases = nil
				packIndex[oldPack.Name] = len(cat.Packs)
				cat.Packs = append(cat.Packs, oldPack)
			}
		}
	}
	for name := range metadata.Packs {
		if _, ok := packIndex[name]; !ok {
			fatal(fmt.Errorf("marketplace metadata references unknown pack %q", name))
		}
	}
	archiveDir := "marketplace/releases"
	if _, err := os.Stat(archiveDir); err == nil {
		err = filepath.WalkDir(archiveDir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() || !strings.HasSuffix(strings.ToLower(path), ".yaml") {
				return nil
			}
			data, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			p, e := packs.LoadPack(strings.NewReader(string(data)))
			if e != nil {
				return e
			}
			i, ok := packIndex[p.Metadata.Name]
			if !ok {
				return fmt.Errorf("archived release %s has no current pack", p.Metadata.Name)
			}
			sum := sha256.Sum256(data)
			artifact := fmt.Sprintf("packs/%s/%s/pack.yaml", p.Metadata.Name, p.Metadata.Version)
			for _, existing := range cat.Packs[i].Releases {
				if strings.TrimPrefix(existing.Version, "v") == strings.TrimPrefix(p.Metadata.Version, "v") {
					if existing.SHA256 != hex.EncodeToString(sum[:]) {
						return fmt.Errorf("published release %s@%s has different bytes", p.Metadata.Name, p.Metadata.Version)
					}
					return nil
				}
			}
			releaseMeta := metadata.Releases[p.Metadata.Name+"@"+p.Metadata.Version]
			cat.Packs[i].Releases = append(cat.Packs[i].Releases, marketplace.Release{Version: p.Metadata.Version, Artifact: artifact, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data)), PackAPI: p.APIVersion, OCSF: ocsf.Version, Changelog: releaseMeta.Changelog, Withdrawn: releaseMeta.Withdrawn, MinWORM: releaseMeta.MinWORM, RequiredDecoders: releaseMeta.RequiredDecoders, PublishedAt: releaseMeta.PublishedAt})
			releaseSource[artifact] = path
			return nil
		})
		fatal(err)
	}
	for releaseID := range metadata.Releases {
		parts := strings.SplitN(releaseID, "@", 2)
		if len(parts) != 2 || releaseSource[fmt.Sprintf("packs/%s/%s/pack.yaml", parts[0], parts[1])] == "" {
			fatal(fmt.Errorf("release metadata references unpublished release %q", releaseID))
		}
	}
	sort.Slice(cat.Packs, func(i, j int) bool { return cat.Packs[i].Name < cat.Packs[j].Name })
	if err := cat.Validate(); err != nil {
		fatal(err)
	}
	if oldCatalog != nil {
		for _, oldPack := range oldCatalog.Packs {
			var current *marketplace.Pack
			for i := range cat.Packs {
				if cat.Packs[i].Name == oldPack.Name {
					current = &cat.Packs[i]
					break
				}
			}
			if current == nil {
				fatal(fmt.Errorf("published pack %s cannot be removed from catalog", oldPack.Name))
			}
			for _, oldRelease := range oldPack.Releases {
				found := false
				for _, newRelease := range current.Releases {
					if strings.TrimPrefix(newRelease.Version, "v") == strings.TrimPrefix(oldRelease.Version, "v") {
						found = true
						if newRelease.SHA256 != oldRelease.SHA256 {
							fatal(fmt.Errorf("published release %s@%s is immutable", oldPack.Name, oldRelease.Version))
						}
						break
					}
				}
				if !found {
					fatal(fmt.Errorf("published release %s@%s must remain in marketplace/releases", oldPack.Name, oldRelease.Version))
				}
			}
		}
	}
	payload, err := json.MarshalIndent(cat, "", "  ")
	fatal(err)
	env, err := json.MarshalIndent(marketplace.Envelope{KeyID: marketplace.OwnerSigningKeyID, Payload: base64.StdEncoding.EncodeToString(payload), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.PrivateKey(key), payload))}, "", "  ")
	fatal(err)
	if err := os.MkdirAll(filepath.Dir(*out), 0755); err != nil {
		fatal(err)
	}
	write(*out, env)
	if err := os.MkdirAll(*site, 0755); err != nil {
		fatal(err)
	}
	write(filepath.Join(*site, "catalog.json"), env)
	for _, p := range cat.Packs {
		for _, r := range p.Releases {
			source := releaseSource[r.Artifact]
			if source == "" {
				fatal(fmt.Errorf("missing source for %s", r.Artifact))
			}
			target := filepath.Join(*site, filepath.FromSlash(r.Artifact))
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				fatal(err)
			}
			if err := copyFile(source, target); err != nil {
				fatal(err)
			}
		}
	}
	fmt.Printf("Built signed catalog with %d packs in %s\n", len(cat.Packs), *site)
}

func checkSeedPacks(dir string) {
	catalog, err := marketplace.LoadCatalog()
	fatal(err)
	entries, err := os.ReadDir(dir)
	fatal(err)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		fatal(err)
		p, err := packs.LoadPack(strings.NewReader(string(data)))
		fatal(err)
		matched := false
		for _, cp := range catalog.Packs {
			if cp.Name != p.Metadata.Name {
				continue
			}
			latest, latestOK := cp.Latest()
			if !latestOK || strings.TrimPrefix(latest.Version, "v") != strings.TrimPrefix(p.Metadata.Version, "v") {
				fatal(fmt.Errorf("shipped pack %s@%s is not the embedded catalog's latest stable release", p.Metadata.Name, p.Metadata.Version))
			}
			for _, r := range cp.Releases {
				if strings.TrimPrefix(r.Version, "v") == strings.TrimPrefix(p.Metadata.Version, "v") && r.VerifyArtifact(data) == nil {
					matched = true
				}
			}
		}
		if !matched {
			fatal(fmt.Errorf("shipped pack %s@%s does not match embedded signed catalog", p.Metadata.Name, p.Metadata.Version))
		}
	}
}

func write(path string, data []byte) {
	if err := os.WriteFile(path, data, 0644); err != nil {
		fatal(err)
	}
}
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}
func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
