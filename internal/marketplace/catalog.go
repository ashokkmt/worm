// Package marketplace reads and verifies the static WORM parser pack catalog.
package marketplace

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/mod/semver"
	"worm/internal/packs"
)

const CatalogVersion = 1
const OwnerSigningKeyID = "worm-market-2026-v2"

var publicKeys = map[string]string{
	"worm-market-2026": "C/JvarAa2Xt7ywMJ+FGENfRHJdC8aJYk5KnlymRKkVM=",
	OwnerSigningKeyID:  "uQZN+zRl3nSBqU4qqmtC/IXFEXT10N9q0lg4ls9MYoI=",
}

//go:embed catalog.json
var seedCatalog []byte

func LoadCatalog() (*Catalog, error) { return Verify(seedCatalog) }

func SeedCatalogBytes() []byte { return append([]byte(nil), seedCatalog...) }

type LocalStore struct {
	Dir         string
	RegistryURL string
	Online      bool
}

func (s LocalStore) Fetch(ctx context.Context, client *http.Client) (*Catalog, error) {
	if !s.Online {
		return nil, errors.New("marketplace is offline; enable --marketplace-online to refresh")
	}
	if s.RegistryURL == "" {
		return nil, errors.New("marketplace registry URL is not configured")
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 0 && (req.URL.Host != via[0].URL.Host || req.URL.Scheme != via[0].URL.Scheme) {
				return errors.New("cross-origin marketplace redirect blocked")
			}
			return nil
		}}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.RegistryURL, "/")+"/catalog.json", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("marketplace returned %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 4<<20 {
		return nil, errors.New("catalog exceeds 4 MiB limit")
	}
	c, err := Verify(data)
	if err != nil {
		return nil, err
	}
	if s.Dir != "" {
		cache := filepath.Join(s.Dir, ".marketplace", "catalog.json")
		if err = packs.RejectSymlinks(s.Dir, cache); err != nil {
			return nil, err
		}
		if err = packs.AtomicWriteFile(s.Dir, cache, data, 0600); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func (s LocalStore) ReadCatalog() (*Catalog, error) {
	if s.Dir != "" {
		cache := filepath.Join(s.Dir, ".marketplace", "catalog.json")
		if err := packs.RejectSymlinks(s.Dir, cache); err != nil {
			return nil, err
		}
		b, err := os.ReadFile(cache)
		if err == nil {
			c, verifyErr := Verify(b)
			if verifyErr != nil {
				return nil, fmt.Errorf("cached marketplace catalog is invalid; refresh or restore it: %w", verifyErr)
			}
			return c, nil
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read marketplace catalog cache: %w", err)
		}
	}
	return LoadCatalog()
}

type Release struct {
	Version          string   `json:"version"`
	Artifact         string   `json:"artifact"`
	SHA256           string   `json:"sha256"`
	Size             int64    `json:"size"`
	Changelog        string   `json:"changelog,omitempty"`
	Withdrawn        bool     `json:"withdrawn,omitempty"`
	MinWORM          string   `json:"min_worm,omitempty"`
	RequiredDecoders []string `json:"required_decoders,omitempty"`
	PublishedAt      string   `json:"published_at,omitempty"`
	PackAPI          string   `json:"pack_api"`
	OCSF             string   `json:"ocsf"`
}

type Pack struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Publisher   string    `json:"publisher"`
	Category    string    `json:"category"`
	Format      string    `json:"format"`
	Vendor      string    `json:"vendor,omitempty"`
	Product     string    `json:"product,omitempty"`
	Models      []string  `json:"models,omitempty"`
	Tags        []string  `json:"tags,omitempty"`
	Releases    []Release `json:"releases"`
}

type Catalog struct {
	Version   int    `json:"version"`
	Generated string `json:"generated"`
	Packs     []Pack `json:"packs"`
	envelope  []byte
}

func (c *Catalog) SignedEnvelope() []byte { return append([]byte(nil), c.envelope...) }

type Envelope struct {
	KeyID     string `json:"key_id"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

var packName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
var decoderCapabilities = map[string]bool{"syslog": true, "json": true, "csv": true, "cef": true, "xml": true, "leef": true, "text": true}

func Verify(data []byte) (*Catalog, error) {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("invalid catalog envelope: %w", err)
	}
	encodedKey, ok := publicKeys[env.KeyID]
	if !ok {
		return nil, fmt.Errorf("untrusted catalog signing key %q", env.KeyID)
	}
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil {
		return nil, err
	}
	sig, err := base64.StdEncoding.DecodeString(env.Signature)
	if err != nil {
		return nil, errors.New("invalid catalog signature encoding")
	}
	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return nil, errors.New("invalid catalog payload encoding")
	}
	if !ed25519.Verify(ed25519.PublicKey(key), payload, sig) {
		return nil, errors.New("catalog signature verification failed")
	}
	var c Catalog
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, fmt.Errorf("invalid catalog: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	c.envelope = append([]byte(nil), data...)
	return &c, nil
}

func SigningKeyTrusted(keyID string, private ed25519.PrivateKey) bool {
	encoded, ok := publicKeys[keyID]
	if !ok || len(private) != ed25519.PrivateKeySize {
		return false
	}
	trusted, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return false
	}
	return ed25519.PublicKey(private.Public().(ed25519.PublicKey)).Equal(ed25519.PublicKey(trusted))
}

func (c *Catalog) Validate() error {
	if c.Version != CatalogVersion {
		return fmt.Errorf("unsupported catalog version %d", c.Version)
	}
	if c.Generated != "" {
		if _, err := time.Parse(time.RFC3339, c.Generated); err != nil {
			return fmt.Errorf("invalid catalog generated time: %w", err)
		}
	}
	seen := make(map[string]bool, len(c.Packs))
	for _, p := range c.Packs {
		if !packName.MatchString(p.Name) || seen[p.Name] {
			return fmt.Errorf("invalid or duplicate pack name %q", p.Name)
		}
		seen[p.Name] = true
		if len(p.Releases) == 0 {
			return fmt.Errorf("pack %q has no releases", p.Name)
		}
		versions := map[string]bool{}
		for _, r := range p.Releases {
			if strings.TrimSpace(r.Version) != r.Version || strings.TrimSpace(r.MinWORM) != r.MinWORM {
				return fmt.Errorf("release %s contains noncanonical version whitespace", p.Name)
			}
			v := normalizeVersion(r.Version)
			if !semver.IsValid(v) || versions[v] {
				return fmt.Errorf("invalid or duplicate release %q for %s", r.Version, p.Name)
			}
			versions[v] = true
			if r.PackAPI == "" || r.OCSF == "" {
				return fmt.Errorf("release %s/%s is missing compatibility metadata", p.Name, r.Version)
			}
			if r.Size <= 0 || r.Size > 2<<20 {
				return fmt.Errorf("release %s/%s has invalid or oversized artifact size", p.Name, r.Version)
			}
			if r.MinWORM != "" && !semver.IsValid(normalizeVersion(r.MinWORM)) {
				return fmt.Errorf("release %s/%s has invalid min_worm", p.Name, r.Version)
			}
			if r.PublishedAt != "" {
				if _, err := time.Parse(time.RFC3339, r.PublishedAt); err != nil {
					return fmt.Errorf("release %s/%s has invalid published_at", p.Name, r.Version)
				}
			}
			for _, decoder := range r.RequiredDecoders {
				if !decoderCapabilities[strings.ToLower(decoder)] {
					return fmt.Errorf("release %s/%s requires unsupported decoder %q", p.Name, r.Version, decoder)
				}
			}
			if len(r.SHA256) != 64 {
				return fmt.Errorf("release %s/%s has invalid SHA-256", p.Name, r.Version)
			}
			if _, err := hex.DecodeString(r.SHA256); err != nil {
				return fmt.Errorf("release %s/%s has invalid SHA-256", p.Name, r.Version)
			}
			if validateArtifactPath(p.Name, r.Version, r.Artifact) != nil {
				return fmt.Errorf("release %s/%s has unsafe artifact path", p.Name, r.Version)
			}
		}
	}
	return nil
}

func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	if v != "" && !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
}

func validateArtifactPath(name, version, artifact string) error {
	if strings.ContainsAny(artifact, "\\%?#:") || strings.HasPrefix(artifact, "/") || path.Clean(artifact) != artifact || artifact != "packs/"+name+"/"+version+"/pack.yaml" {
		return errors.New("artifact path is not canonical")
	}
	return nil
}

func (p Pack) Latest() (Release, bool) {
	versions := append([]Release(nil), p.Releases...)
	sort.Slice(versions, func(i, j int) bool {
		return semver.Compare(normalizeVersion(versions[i].Version), normalizeVersion(versions[j].Version)) > 0
	})
	for _, r := range versions {
		v := normalizeVersion(r.Version)
		if !r.Withdrawn && semver.Prerelease(v) == "" {
			return r, true
		}
	}
	return Release{}, false
}

func (p Pack) Release(version string) (Release, bool) {
	want := normalizeVersion(version)
	for _, r := range p.Releases {
		if normalizeVersion(r.Version) == want && !r.Withdrawn {
			return r, true
		}
	}
	return Release{}, false
}

func (p Pack) LatestForMajor(major string) (Release, bool) {
	var best Release
	for _, r := range p.Releases {
		v := normalizeVersion(r.Version)
		if semver.Major(v) != major || semver.Prerelease(v) != "" || r.Withdrawn {
			continue
		}
		if best.Version == "" {
			best = r
			continue
		}
		b := normalizeVersion(best.Version)
		if semver.Compare(v, b) > 0 {
			best = r
		}
	}
	return best, best.Version != ""
}

func (p Pack) LatestCompatible(wormVersion, packAPI, ocsfVersion string) (Release, bool) {
	var best Release
	for _, r := range p.Releases {
		v := normalizeVersion(r.Version)
		if r.Withdrawn || semver.Prerelease(v) != "" || r.PackAPI != packAPI || r.OCSF != ocsfVersion {
			continue
		}
		if r.MinWORM != "" && semver.Compare(normalizeVersion(wormVersion), normalizeVersion(r.MinWORM)) < 0 {
			continue
		}
		if !r.hasSupportedDecoders() {
			continue
		}
		if best.Version == "" || semver.Compare(v, normalizeVersion(best.Version)) > 0 {
			best = r
		}
	}
	return best, best.Version != ""
}

func (p Pack) LatestCompatibleForMajor(wormVersion, packAPI, ocsfVersion, major string) (Release, bool) {
	var best Release
	for _, r := range p.Releases {
		v := normalizeVersion(r.Version)
		if r.Withdrawn || semver.Prerelease(v) != "" || semver.Major(v) != major || r.PackAPI != packAPI || r.OCSF != ocsfVersion {
			continue
		}
		if r.MinWORM != "" && semver.Compare(normalizeVersion(wormVersion), normalizeVersion(r.MinWORM)) < 0 {
			continue
		}
		if !r.hasSupportedDecoders() {
			continue
		}
		if best.Version == "" || semver.Compare(v, normalizeVersion(best.Version)) > 0 {
			best = r
		}
	}
	return best, best.Version != ""
}

func (r Release) hasSupportedDecoders() bool {
	for _, d := range r.RequiredDecoders {
		if !decoderCapabilities[strings.ToLower(d)] {
			return false
		}
	}
	return true
}

func (c *Catalog) Search(q, category, format string) []Pack {
	return c.SearchFiltered(q, category, format, "", "", "")
}

func (c *Catalog) SearchFiltered(q, category, format, vendor, product, model string) []Pack {
	q = strings.ToLower(strings.TrimSpace(q))
	category = strings.ToLower(category)
	format = strings.ToLower(format)
	vendor, product, model = strings.ToLower(vendor), strings.ToLower(product), strings.ToLower(model)
	out := make([]Pack, 0)
	for _, p := range c.Packs {
		if category != "" && strings.ToLower(p.Category) != category {
			continue
		}
		if format != "" && strings.ToLower(p.Format) != format {
			continue
		}
		if vendor != "" && !strings.Contains(strings.ToLower(p.Vendor), vendor) {
			continue
		}
		if product != "" && !strings.Contains(strings.ToLower(p.Product), product) {
			continue
		}
		if model != "" {
			found := false
			for _, m := range p.Models {
				if strings.Contains(strings.ToLower(m), model) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		hay := strings.ToLower(strings.Join([]string{p.Name, p.Description, p.Publisher, p.Category, p.Format, p.Vendor, p.Product, strings.Join(p.Models, " "), strings.Join(p.Tags, " ")}, " "))
		if q == "" || strings.Contains(hay, q) {
			out = append(out, p)
		}
	}
	return out
}

func Digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func readArtifact(filename string, release Release) ([]byte, error) {
	info, err := os.Stat(filename)
	if err != nil {
		return nil, err
	}
	if info.Size() != release.Size {
		return nil, fmt.Errorf("artifact size mismatch: expected %d, received %d", release.Size, info.Size())
	}
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, release.Size+1))
	if err != nil {
		return nil, err
	}
	return data, release.VerifyArtifact(data)
}

// CacheBundledArtifacts adopts the current shipped packs once, preserving existing files.
func CacheBundledArtifacts(dir string) error {
	catalog, err := LoadCatalog()
	if err != nil {
		return err
	}
	manifest, err := packs.ReadManifest(dir)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	seed := map[string]struct {
		release  Release
		content  []byte
		filename string
	}{}
	for _, entry := range entries {
		if entry.IsDir() || !(strings.HasSuffix(strings.ToLower(entry.Name()), ".yaml") || strings.HasSuffix(strings.ToLower(entry.Name()), ".yml")) {
			continue
		}
		filename := entry.Name()
		if err := packs.RejectSymlinks(dir, filepath.Join(dir, filename)); err != nil {
			return err
		}
		data, e := os.ReadFile(filepath.Join(dir, filename))
		if e != nil {
			return e
		}
		active, e := packs.LoadPack(strings.NewReader(string(data)))
		if e != nil {
			return e
		}
		for _, pack := range catalog.Packs {
			if pack.Name != active.Metadata.Name {
				continue
			}
			for _, release := range pack.Releases {
				if strings.TrimPrefix(release.Version, "v") == strings.TrimPrefix(active.Metadata.Version, "v") && release.VerifyArtifact(data) == nil {
					seed[pack.Name] = struct {
						release  Release
						content  []byte
						filename string
					}{release, data, filename}
					break
				}
			}
		}
	}
	changed := false
	for name, value := range seed {
		if _, exists := manifest.Entries[name]; exists {
			continue
		}
		manifest.Entries[name] = packs.PackInstall{Name: name, Filename: value.filename, Version: value.release.Version, Digest: value.release.SHA256, Origin: "bundled"}
		artifact := filepath.Join(dir, ".marketplace", "artifacts", name, value.release.Version, "pack.yaml")
		if e := packs.AtomicWriteFile(dir, artifact, value.content, 0600); e != nil {
			return e
		}
		changed = true
	}
	for _, entry := range entries {
		if entry.IsDir() || !(strings.HasSuffix(strings.ToLower(entry.Name()), ".yaml") || strings.HasSuffix(strings.ToLower(entry.Name()), ".yml")) {
			continue
		}
		data, e := os.ReadFile(filepath.Join(dir, entry.Name()))
		if e != nil {
			return e
		}
		pack, e := packs.LoadPack(strings.NewReader(string(data)))
		if e != nil {
			return e
		}
		if _, exists := manifest.Entries[pack.Metadata.Name]; exists {
			continue
		}
		manifest.Entries[pack.Metadata.Name] = packs.PackInstall{Name: pack.Metadata.Name, Filename: entry.Name(), Version: pack.Metadata.Version, Digest: Digest(data), Origin: "custom"}
		changed = true
	}
	if changed {
		return packs.WriteManifest(dir, manifest)
	}
	return nil
}

func (r Release) VerifyArtifact(data []byte) error {
	if int64(len(data)) != r.Size {
		return fmt.Errorf("artifact size mismatch: expected %d, received %d", r.Size, len(data))
	}
	if Digest(data) != r.SHA256 {
		return errors.New("artifact SHA-256 mismatch")
	}
	return nil
}

func ExportSnapshot(c *Catalog, sourceDir, destDir string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if len(c.envelope) == 0 {
		return errors.New("export requires the verified signed catalog envelope")
	}
	if err := os.MkdirAll(destDir, 0700); err != nil {
		return err
	}
	available := []string{}
	for _, p := range c.Packs {
		for _, r := range p.Releases {
			cached := filepath.Join(sourceDir, ".marketplace", "artifacts", p.Name, r.Version, "pack.yaml")
			if err := packs.RejectSymlinks(sourceDir, cached); err != nil {
				return err
			}
			data, err := readArtifact(cached, r)
			if err != nil {
				for _, filename := range []string{p.Name + ".yaml", p.Name + ".yml"} {
					if err := packs.RejectSymlinks(sourceDir, filepath.Join(sourceDir, filename)); err != nil {
						return err
					}
					if candidate, e := readArtifact(filepath.Join(sourceDir, filename), r); e == nil {
						data, err = candidate, nil
						break
					}
				}
			}
			if err != nil {
				continue
			}
			if err = r.VerifyArtifact(data); err != nil {
				return err
			}
			target := filepath.Join(destDir, filepath.FromSlash(r.Artifact))
			if err = packs.AtomicWriteFile(destDir, target, data, 0600); err != nil {
				return err
			}
			available = append(available, r.Artifact)
		}
	}
	sort.Strings(available)
	if err := packs.AtomicWriteFile(destDir, filepath.Join(destDir, "catalog.json"), c.envelope, 0600); err != nil {
		return err
	}
	bundle, _ := json.MarshalIndent(struct {
		Version       int      `json:"version"`
		CatalogSHA256 string   `json:"catalog_sha256"`
		Available     []string `json:"available_artifacts"`
	}{1, Digest(c.envelope), available}, "", "  ")
	return packs.AtomicWriteFile(destDir, filepath.Join(destDir, "bundle.json"), bundle, 0600)
}

func ImportSnapshot(sourceDir, destDir string) error {
	catalogPath := filepath.Join(sourceDir, "catalog.json")
	if err := packs.RejectSymlinks(sourceDir, catalogPath); err != nil {
		return err
	}
	envelope, err := os.ReadFile(catalogPath)
	if err != nil {
		return err
	}
	c, err := Verify(envelope)
	if err != nil {
		return err
	}
	bundlePath := filepath.Join(sourceDir, "bundle.json")
	if err = packs.RejectSymlinks(sourceDir, bundlePath); err != nil {
		return err
	}
	bundleBytes, err := os.ReadFile(bundlePath)
	if err != nil {
		return fmt.Errorf("offline bundle index is missing: %w", err)
	}
	var bundle struct {
		Version       int      `json:"version"`
		CatalogSHA256 string   `json:"catalog_sha256"`
		Available     []string `json:"available_artifacts"`
	}
	if err = json.Unmarshal(bundleBytes, &bundle); err != nil {
		return err
	}
	if bundle.Version != 1 || bundle.CatalogSHA256 != Digest(envelope) {
		return errors.New("offline bundle index does not match signed catalog")
	}
	listed := map[string]bool{}
	for _, artifact := range bundle.Available {
		if listed[artifact] {
			return errors.New("offline bundle lists a duplicate artifact")
		}
		listed[artifact] = true
	}
	for _, p := range c.Packs {
		for _, r := range p.Releases {
			source := filepath.Join(sourceDir, filepath.FromSlash(r.Artifact))
			if err := packs.RejectSymlinks(sourceDir, source); err != nil {
				return err
			}
			data, e := readArtifact(source, r)
			if os.IsNotExist(e) {
				if listed[r.Artifact] {
					return fmt.Errorf("offline bundle lists missing artifact %s", r.Artifact)
				}
				continue
			}
			if e != nil {
				return e
			}
			if e = r.VerifyArtifact(data); e != nil {
				return fmt.Errorf("artifact %s@%s: %w", p.Name, r.Version, e)
			}
			if !listed[r.Artifact] {
				return fmt.Errorf("offline bundle contains unlisted artifact %s", r.Artifact)
			}
			delete(listed, r.Artifact)
		}
	}
	if len(listed) > 0 {
		return errors.New("offline bundle lists artifacts absent from the signed catalog")
	}
	for _, p := range c.Packs {
		for _, r := range p.Releases {
			source := filepath.Join(sourceDir, filepath.FromSlash(r.Artifact))
			if err := packs.RejectSymlinks(sourceDir, source); err != nil {
				return err
			}
			data, e := readArtifact(source, r)
			if os.IsNotExist(e) {
				continue
			}
			if e != nil {
				return e
			}
			if e = r.VerifyArtifact(data); e != nil {
				return e
			}
			destination := filepath.Join(destDir, ".marketplace", "artifacts", p.Name, r.Version, "pack.yaml")
			if err = packs.AtomicWriteFile(destDir, destination, data, 0600); err != nil {
				return err
			}
		}
	}
	cache := filepath.Join(destDir, ".marketplace", "catalog.json")
	return packs.AtomicWriteFile(destDir, cache, envelope, 0600)
}

func (s LocalStore) FetchArtifact(ctx context.Context, client *http.Client, r Release) ([]byte, error) {
	if !s.Online {
		return nil, errors.New("marketplace is offline; only cached artifacts can be installed")
	}
	base, err := url.Parse(strings.TrimRight(s.RegistryURL, "/") + "/")
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(r.Artifact)
	if err != nil {
		return nil, err
	}
	target := base.ResolveReference(u)
	if target.Scheme != base.Scheme || target.Host != base.Host {
		return nil, errors.New("artifact URL is outside the configured registry")
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 0 && (req.URL.Host != via[0].URL.Host || req.URL.Scheme != via[0].URL.Scheme) {
				return errors.New("cross-origin marketplace redirect blocked")
			}
			return nil
		}}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("marketplace artifact returned %s", resp.Status)
	}
	limit := r.Size + 1
	if limit <= 0 || limit > 2<<20 {
		limit = (2 << 20) + 1
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, err
	}
	if err := r.VerifyArtifact(data); err != nil {
		return nil, err
	}
	return data, nil
}
