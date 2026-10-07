package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/mod/semver"
	"worm/internal/marketplace"
	"worm/internal/ocsf"
	"worm/internal/packs"
)

func MarketplaceCLI(args []string, packsDir, uiAddr string) {
	args = stripMarketplaceOptions(args)
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: worm marketplace search|show|refresh")
		os.Exit(2)
	}
	base := GetBaseURL(uiAddr)
	switch args[0] {
	case "search":
		filters := map[string]string{}
		jsonOut, installedOnly := false, false
		terms := []string{}
		for i := 1; i < len(args); i++ {
			a := args[i]
			if a == "--json" {
				jsonOut = true
				continue
			}
			if a == "--installed" {
				installedOnly = true
				continue
			}
			if strings.HasPrefix(a, "--") && i+1 < len(args) {
				key := strings.TrimPrefix(a, "--")
				if key == "vendor" || key == "product" || key == "model" || key == "category" || key == "format" {
					filters[key] = args[i+1]
					i++
					continue
				}
			}
			terms = append(terms, a)
		}
		catalog, err := (marketplace.LocalStore{Dir: packsDir}).ReadCatalog()
		if err != nil {
			failMarketplace(err)
		}
		items := catalog.SearchFiltered(strings.Join(terms, " "), filters["category"], filters["format"], filters["vendor"], filters["product"], filters["model"])
		if installedOnly {
			manifest, e := packs.ReadManifest(packsDir)
			if e != nil {
				failMarketplace(e)
			}
			kept := items[:0]
			for _, item := range items {
				if _, ok := manifest.Entries[item.Name]; ok {
					kept = append(kept, item)
				}
			}
			items = kept
		}
		if jsonOut {
			b, e := json.MarshalIndent(items, "", "  ")
			if e != nil {
				failMarketplace(e)
			}
			fmt.Println(string(b))
			return
		}
		if len(items) == 0 {
			fmt.Println("No matching parser packs.")
			return
		}
		for _, p := range items {
			r, ok := p.Latest()
			v := "none"
			if ok {
				v = r.Version
			}
			fmt.Printf("%-28s %-9s %-16s %s\n", p.Name, v, p.Category, p.Format)
		}
	case "show":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: worm marketplace show <name>")
			os.Exit(2)
		}
		catalog, err := (marketplace.LocalStore{Dir: packsDir}).ReadCatalog()
		if err != nil {
			failMarketplace(err)
		}
		for _, p := range catalog.Packs {
			if p.Name == args[1] {
				data, _ := json.MarshalIndent(p, "", "  ")
				fmt.Println(string(data))
				return
			}
		}
		fmt.Fprintf(os.Stderr, "Marketplace pack %q not found\n", args[1])
		os.Exit(1)
	case "refresh":
		client := marketplaceHTTPClient()
		req, err := http.NewRequest(http.MethodPost, base+"/api/v1/marketplace/refresh", nil)
		if err != nil {
			failMarketplace(err)
		}
		setMarketplaceAuth(req)
		resp, err := client.Do(req)
		if err != nil {
			failMarketplace(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			fmt.Fprintf(os.Stderr, "Marketplace refresh failed: %s\n", resp.Status)
			os.Exit(1)
		}
		fmt.Println("Marketplace catalog refreshed.")
	case "fetch":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: worm marketplace fetch <name>[@version]")
			os.Exit(2)
		}
		name, version := splitPackVersion(args[1])
		b, _ := json.Marshal(map[string]string{"name": name, "version": version})
		req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/marketplace/fetch", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		setMarketplaceAuth(req)
		resp, err := marketplaceHTTPClient().Do(req)
		if err != nil {
			failMarketplace(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			fmt.Fprintf(os.Stderr, "Pack fetch failed: %s\n", resp.Status)
			os.Exit(1)
		}
		fmt.Printf("Fetched %s.\n", name)
	case "export", "import":
		if len(args) < 3 || args[1] != "--dir" {
			fmt.Fprintf(os.Stderr, "Usage: worm marketplace %s --dir <directory>\n", args[0])
			os.Exit(2)
		}
		if args[0] == "export" {
			catalog, err := (marketplace.LocalStore{Dir: packsDir}).ReadCatalog()
			if err != nil {
				failMarketplace(err)
			}
			if err = marketplace.ExportSnapshot(catalog, packsDir, args[2]); err != nil {
				failMarketplace(err)
			}
			fmt.Printf("Offline marketplace snapshot exported to %s.\n", args[2])
		} else {
			if err := marketplace.ImportSnapshot(args[2], packsDir); err != nil {
				failMarketplace(err)
			}
			fmt.Printf("Offline marketplace snapshot imported into %s.\n", packsDir)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown marketplace command %q\n", args[0])
		os.Exit(2)
	}
}

func stripMarketplaceOptions(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--ui" || args[i] == "--packs-dir" || args[i] == "--admin-token" {
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func splitPackVersion(spec string) (string, string) {
	parts := strings.SplitN(spec, "@", 2)
	version := "latest"
	if len(parts) == 2 {
		version = parts[1]
	}
	return parts[0], version
}

func InstallMarketplacePackCLI(name, version, uiAddr string) {
	body, _ := json.Marshal(map[string]string{"name": name, "version": version})
	req, err := http.NewRequest(http.MethodPost, GetBaseURL(uiAddr)+"/api/v1/packs/install", bytes.NewReader(body))
	if err != nil {
		failMarketplace(err)
	}
	req.Header.Set("Content-Type", "application/json")
	setMarketplaceAuth(req)
	resp, err := marketplaceHTTPClient().Do(req)
	if err != nil {
		failMarketplace(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "Pack install failed: %s\n", resp.Status)
		os.Exit(1)
	}
	var result struct {
		Status  string `json:"status"`
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&result); err != nil {
		failMarketplace(err)
	}
	fmt.Printf("%s %s %s\n", result.Status, result.Name, result.Version)
}

func RemoveMarketplacePackCLI(name, uiAddr string) {
	req, err := http.NewRequest(http.MethodDelete, GetBaseURL(uiAddr)+"/api/v1/packs/"+url.PathEscape(name), nil)
	if err != nil {
		failMarketplace(err)
	}
	setMarketplaceAuth(req)
	resp, err := marketplaceHTTPClient().Do(req)
	if err != nil {
		failMarketplace(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "Pack removal failed: %s\n", resp.Status)
		os.Exit(1)
	}
	fmt.Printf("Removed %s. Logs without a matching pack may be quarantined.\n", name)
}

func UpgradeMarketplacePackCLI(name, uiAddr, packsDir string) {
	upgradeRequest(uiAddr, map[string]any{"name": name})
}

func UpgradeAllMarketplacePacksCLI(uiAddr, packsDir string) {
	upgradeRequest(uiAddr, map[string]any{"all": true})
}

func InstallMarketplacePackLocalCLI(name, version, dir string) {
	lock, err := packs.AcquireDirectoryLock(dir)
	if err != nil {
		failMarketplace(err)
	}
	defer lock.Close()
	if err = packs.RecoverTransactions(dir); err != nil {
		failMarketplace(err)
	}
	catalog, err := (marketplace.LocalStore{Dir: dir}).ReadCatalog()
	if err != nil {
		failMarketplace(err)
	}
	var pack *marketplace.Pack
	for i := range catalog.Packs {
		if catalog.Packs[i].Name == name {
			pack = &catalog.Packs[i]
			break
		}
	}
	if pack == nil {
		failMarketplace(fmt.Errorf("pack %q not found", name))
	}
	var release marketplace.Release
	var ok bool
	if version == "" || version == "latest" {
		release, ok = pack.LatestCompatible("v999999.0.0", "worm.io/v1", ocsf.Version)
	} else {
		release, ok = pack.Release(version)
	}
	if !ok {
		failMarketplace(fmt.Errorf("release %s@%s unavailable", name, version))
	}
	artifact := filepath.Join(dir, ".marketplace", "artifacts", name, release.Version, "pack.yaml")
	data, err := os.ReadFile(artifact)
	if err != nil {
		failMarketplace(fmt.Errorf("artifact is not cached; fetch or import it first: %w", err))
	}
	if err = release.VerifyArtifact(data); err != nil {
		failMarketplace(err)
	}
	manifest, err := packs.ReadManifest(dir)
	if err != nil {
		failMarketplace(err)
	}
	filename := name + ".yaml"
	expected := &packs.PackFileExpectation{Exists: false}
	if installed, exists := manifest.Entries[name]; exists {
		filename = installed.Filename
		current, e := os.ReadFile(filepath.Join(dir, filename))
		if e != nil {
			failMarketplace(e)
		}
		expected = &packs.PackFileExpectation{Exists: true, Digest: marketplace.Digest(current)}
	} else if _, e := os.ReadFile(filepath.Join(dir, filename)); e == nil {
		failMarketplace(fmt.Errorf("local pack already occupies %s", filename))
	} else if !os.IsNotExist(e) {
		failMarketplace(e)
	}
	snap, err := packs.LoadDir(dir)
	if err != nil {
		failMarketplace(err)
	}
	mgr := packs.NewSnapshotManager(snap)
	mgr.SetPacksDir(dir)
	if _, err = mgr.ApplyPackFileWithState(filename, data, expected, "marketplace", version != "" && version != "latest"); err != nil {
		failMarketplace(err)
	}
	fmt.Printf("Installed %s %s; active on next startup.\n", name, release.Version)
}

func RemoveMarketplacePackLocalCLI(name, dir string) {
	lock, err := packs.AcquireDirectoryLock(dir)
	if err != nil {
		failMarketplace(err)
	}
	defer lock.Close()
	if err = packs.RecoverTransactions(dir); err != nil {
		failMarketplace(err)
	}
	manifest, err := packs.ReadManifest(dir)
	if err != nil {
		failMarketplace(err)
	}
	entry, ok := manifest.Entries[name]
	if !ok || (entry.Origin != "marketplace" && entry.Origin != "bundled") {
		failMarketplace(fmt.Errorf("pack %q is not marketplace-managed", name))
	}
	data, err := os.ReadFile(filepath.Join(dir, entry.Filename))
	if err != nil {
		failMarketplace(err)
	}
	if marketplace.Digest(data) != entry.Digest {
		failMarketplace(fmt.Errorf("pack %q was modified locally", name))
	}
	snap, err := packs.LoadDir(dir)
	if err != nil {
		failMarketplace(err)
	}
	mgr := packs.NewSnapshotManager(snap)
	mgr.SetPacksDir(dir)
	if _, err = mgr.RemovePackFileExpected(entry.Filename, &packs.PackFileExpectation{Exists: true, Digest: entry.Digest}); err != nil {
		failMarketplace(err)
	}
	fmt.Printf("Removed %s; active on next startup.\n", name)
}

func FetchMarketplacePackLocalCLI(name, version, dir, registryURL string, online bool) {
	catalog, err := (marketplace.LocalStore{Dir: dir}).ReadCatalog()
	if err != nil {
		failMarketplace(err)
	}
	var pack *marketplace.Pack
	for i := range catalog.Packs {
		if catalog.Packs[i].Name == name {
			pack = &catalog.Packs[i]
			break
		}
	}
	if pack == nil {
		failMarketplace(fmt.Errorf("pack %q not found", name))
	}
	var release marketplace.Release
	var ok bool
	if version == "" || version == "latest" {
		release, ok = pack.LatestCompatible("v999999.0.0", "worm.io/v1", ocsf.Version)
	} else {
		release, ok = pack.Release(version)
	}
	if !ok {
		failMarketplace(fmt.Errorf("release %s@%s unavailable", name, version))
	}
	cache := filepath.Join(dir, ".marketplace", "artifacts", name, release.Version, "pack.yaml")
	if data, e := os.ReadFile(cache); e == nil && release.VerifyArtifact(data) == nil {
		fmt.Printf("Cached %s %s.\n", name, release.Version)
		return
	}
	if !online {
		failMarketplace(fmt.Errorf("artifact is not cached; use --marketplace-online to fetch it"))
	}
	store := marketplace.LocalStore{Dir: dir, RegistryURL: registryURL, Online: true}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	data, err := store.FetchArtifact(ctx, nil, release)
	if err != nil {
		failMarketplace(err)
	}
	if err = packs.AtomicWriteFile(dir, cache, data, 0600); err != nil {
		failMarketplace(err)
	}
	fmt.Printf("Fetched %s %s.\n", name, release.Version)
}

func UpgradeMarketplacePackLocalCLI(name, dir string) {
	manifest, err := packs.ReadManifest(dir)
	if err != nil {
		failMarketplace(err)
	}
	entry, ok := manifest.Entries[name]
	if !ok {
		failMarketplace(fmt.Errorf("pack %q is not installed", name))
	}
	if entry.Pinned {
		fmt.Printf("%s is pinned at %s.\n", name, entry.Version)
		return
	}
	v := entry.Version
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	catalog, err := (marketplace.LocalStore{Dir: dir}).ReadCatalog()
	if err != nil {
		failMarketplace(err)
	}
	for _, p := range catalog.Packs {
		if p.Name == name {
			target, ok := p.LatestCompatibleForMajor("v999999.0.0", "worm.io/v1", ocsf.Version, semver.Major(v))
			if !ok || semver.Compare("v"+strings.TrimPrefix(target.Version, "v"), v) <= 0 {
				fmt.Printf("%s is current at %s.\n", name, entry.Version)
				return
			}
			InstallMarketplacePackLocalCLI(name, target.Version, dir)
			return
		}
	}
	failMarketplace(fmt.Errorf("pack %q is missing from catalog", name))
}

func upgradeRequest(uiAddr string, payload any) {
	b, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, GetBaseURL(uiAddr)+"/api/v1/packs/upgrade", bytes.NewReader(b))
	if err != nil {
		failMarketplace(err)
	}
	req.Header.Set("Content-Type", "application/json")
	setMarketplaceAuth(req)
	resp, err := marketplaceHTTPClient().Do(req)
	if err != nil {
		failMarketplace(err)
	}
	defer resp.Body.Close()
	var result struct {
		Results []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			StatusCode int    `json:"status_code"`
			Version    string `json:"version"`
			Error      string `json:"error"`
		} `json:"results"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&result); err != nil {
		failMarketplace(err)
	}
	failed := resp.StatusCode < 200 || resp.StatusCode >= 300
	for _, item := range result.Results {
		if item.Error != "" || item.StatusCode >= 300 {
			failed = true
		}
		fmt.Printf("%s: %s %s %s\n", item.Name, item.Status, item.Version, item.Error)
	}
	if failed {
		os.Exit(1)
	}
}

func ListOutdatedMarketplacePacksCLI(uiAddr, packsDir string) {
	var active struct {
		Packs []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"packs"`
	}
	if err := getMarketplaceJSON(uiAddr, "/api/v1/packs", &active); err != nil {
		failMarketplace(err)
	}
	catalog, err := (marketplace.LocalStore{Dir: packsDir}).ReadCatalog()
	if err != nil {
		failMarketplace(err)
	}
	count := 0
	for _, installed := range active.Packs {
		for _, p := range catalog.Packs {
			if p.Name != installed.Name {
				continue
			}
			v := installed.Version
			if !strings.HasPrefix(v, "v") {
				v = "v" + v
			}
			if latest, ok := p.LatestForMajor(semver.Major(v)); ok && semver.Compare("v"+strings.TrimPrefix(latest.Version, "v"), v) > 0 {
				fmt.Printf("%-28s %s -> %s\n", p.Name, installed.Version, latest.Version)
				count++
			}
			break
		}
	}
	if count == 0 {
		fmt.Println("No compatible newer marketplace releases found in the current catalog.")
	}
}

func getMarketplaceJSON(uiAddr, endpoint string, target any) error {
	req, _ := http.NewRequest(http.MethodGet, GetBaseURL(uiAddr)+endpoint, nil)
	setMarketplaceAuth(req)
	resp, err := marketplaceHTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("management API returned %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

func marketplaceHTTPClient() *http.Client { return &http.Client{Timeout: 12 * time.Second} }

var marketplaceAdminToken string

func SetMarketplaceAdminToken(token string) { marketplaceAdminToken = token }

func setMarketplaceAuth(req *http.Request) {
	token := marketplaceAdminToken
	if token == "" {
		token = os.Getenv("WORM_ADMIN_TOKEN")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}
func failMarketplace(err error) { fmt.Fprintln(os.Stderr, "Marketplace:", err); os.Exit(1) }
