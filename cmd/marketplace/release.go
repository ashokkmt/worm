package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"worm/internal/marketplace"
	"worm/internal/packs"

	"gopkg.in/yaml.v3"
)

type releaseUpdate struct {
	path       string
	data       []byte
	archive    []archiveUpdate
	name       string
	oldVersion string
	newVersion string
}

type archiveUpdate struct {
	path string
	data []byte
}

func prepareReleaseFiles(packDir, metadataFile, registryURL string, catalog *marketplace.Catalog, args []string, changelog string) error {
	if len(args) == 0 {
		return fmt.Errorf("provide one or more pack YAML filenames after -prepare-release")
	}
	changelog = strings.TrimSpace(changelog)
	if changelog == "" {
		return fmt.Errorf("release changelog cannot be empty")
	}
	metadataBytes, err := os.ReadFile(metadataFile)
	if err != nil {
		return fmt.Errorf("read release metadata: %w", err)
	}
	var metadata ownerMetadata
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		return fmt.Errorf("decode release metadata: %w", err)
	}
	if metadata.Releases == nil {
		metadata.Releases = map[string]releaseMetadata{}
	}

	oldPacks := make(map[string]marketplace.Pack, len(catalog.Packs))
	for _, p := range catalog.Packs {
		oldPacks[p.Name] = p
	}
	selected := make(map[string]bool)
	updates := make([]releaseUpdate, 0, len(args))
	seenPaths := map[string]bool{}
	client := &http.Client{Timeout: 30 * time.Second}

	for _, arg := range args {
		name := filepath.Base(arg)
		if filepath.IsAbs(arg) || strings.Contains(filepath.ToSlash(filepath.Clean(arg)), "../") || !strings.HasSuffix(strings.ToLower(name), ".yaml") {
			return fmt.Errorf("release input must be a YAML filename inside %s: %q", packDir, arg)
		}
		path := filepath.Join(packDir, name)
		if seenPaths[path] {
			continue
		}
		seenPaths[path] = true
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read pack %s: %w", arg, err)
		}
		pack, err := packs.LoadPack(strings.NewReader(string(data)))
		if err != nil {
			return fmt.Errorf("load pack %s: %w", arg, err)
		}
		old, ok := oldPacks[pack.Metadata.Name]
		if !ok {
			return fmt.Errorf("%s is not in the signed catalog; add new packs through the normal publishing workflow", pack.Metadata.Name)
		}
		if selected[pack.Metadata.Name] {
			return fmt.Errorf("multiple files contain pack %s", pack.Metadata.Name)
		}
		selected[pack.Metadata.Name] = true
		latest, ok := old.Latest()
		if !ok {
			return fmt.Errorf("signed catalog has no releases for %s", pack.Metadata.Name)
		}
		latestVersion := strings.TrimPrefix(latest.Version, "v")
		currentVersion := strings.TrimPrefix(pack.Metadata.Version, "v")
		if versionPattern.FindStringSubmatch(latestVersion) == nil || versionPattern.FindStringSubmatch(currentVersion) == nil {
			return fmt.Errorf("%s uses an unsupported version; batch release requires MAJOR.MINOR.PATCH versions", pack.Metadata.Name)
		}
		newVersion := ""
		var updated []byte
		isPrepared := versionCompare(currentVersion, latestVersion) > 0 && hasVerifiedArchive(latest, filepath.Join("marketplace", "releases", pack.Metadata.Name, latest.Version+".yaml"))
		if isPrepared {
			newVersion = pack.Metadata.Version
		} else {
			if currentVersion != latestVersion {
				return fmt.Errorf("%s must use its latest signed version %s before preparation; current YAML is %s", pack.Metadata.Name, latest.Version, pack.Metadata.Version)
			}
			newVersion, err = nextPatchVersion(latest.Version)
			if err != nil {
				return fmt.Errorf("choose next version for %s: %w", pack.Metadata.Name, err)
			}
			if _, exists := metadata.Releases[pack.Metadata.Name+"@"+newVersion]; exists {
				return fmt.Errorf("release metadata already exists for %s@%s", pack.Metadata.Name, newVersion)
			}
			updated, err = setMetadataVersion(data, newVersion)
			if err != nil {
				return fmt.Errorf("update %s version: %w", arg, err)
			}
		}

		update := releaseUpdate{path: path, data: updated, name: pack.Metadata.Name, oldVersion: latest.Version, newVersion: newVersion}
		for _, release := range old.Releases {
			archivePath := filepath.Join("marketplace", "releases", pack.Metadata.Name, release.Version+".yaml")
			var archive []byte
			if !isPrepared && strings.TrimPrefix(release.Version, "v") == latestVersion && release.VerifyArtifact(data) == nil {
				archive = data
			} else {
				archive, err = readOrFetchArchive(client, registryURL, archivePath, release)
				if err != nil {
					return fmt.Errorf("preserve exact prior bytes for %s@%s (current YAML may already be edited): %w", pack.Metadata.Name, release.Version, err)
				}
			}
			update.archive = append(update.archive, archiveUpdate{path: archivePath, data: archive})
		}

		releaseID := pack.Metadata.Name + "@" + newVersion
		newRelease, exists := metadata.Releases[releaseID]
		if !exists {
			newRelease = metadata.Releases[pack.Metadata.Name+"@"+latest.Version]
		}
		newRelease.Changelog = changelog
		newRelease.Withdrawn = false
		newRelease.PublishedAt = time.Now().UTC().Format(time.RFC3339)
		metadata.Releases[releaseID] = newRelease
		updates = append(updates, update)
	}
	if len(updates) == 0 {
		return fmt.Errorf("no release preparation changes were needed")
	}

	if err := checkUnselectedChanges(packDir, oldPacks, selected, metadata); err != nil {
		return err
	}
	updatedMetadata, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	updatedMetadata = append(updatedMetadata, '\n')

	for _, update := range updates {
		for _, archive := range update.archive {
			if err := writeAtomic(archive.path, archive.data); err != nil {
				return err
			}
		}
	}
	for _, update := range updates {
		if update.data != nil {
			if err := writeAtomic(update.path, update.data); err != nil {
				return err
			}
			fmt.Printf("Prepared %s: %s -> %s\n", update.name, update.oldVersion, update.newVersion)
		} else {
			fmt.Printf("Already prepared %s@%s\n", update.name, update.newVersion)
		}
	}
	return writeAtomic(metadataFile, updatedMetadata)
}

func checkUnselectedChanges(packDir string, oldPacks map[string]marketplace.Pack, selected map[string]bool, metadata ownerMetadata) error {
	entries, err := os.ReadDir(packDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(packDir, entry.Name()))
		if err != nil {
			return err
		}
		pack, err := packs.LoadPack(strings.NewReader(string(data)))
		if err != nil {
			return fmt.Errorf("load pack %s: %w", entry.Name(), err)
		}
		old, exists := oldPacks[pack.Metadata.Name]
		if !exists || selected[pack.Metadata.Name] {
			continue
		}
		matched := false
		for _, release := range old.Releases {
			if strings.TrimPrefix(release.Version, "v") == strings.TrimPrefix(pack.Metadata.Version, "v") && release.VerifyArtifact(data) == nil {
				matched = true
				break
			}
		}
		if !matched {
			latest, ok := old.Latest()
			if ok && versionCompare(strings.TrimPrefix(pack.Metadata.Version, "v"), strings.TrimPrefix(latest.Version, "v")) > 0 {
				_, hasMetadata := metadata.Releases[pack.Metadata.Name+"@"+pack.Metadata.Version]
				archivePath := filepath.Join("marketplace", "releases", pack.Metadata.Name, latest.Version+".yaml")
				matched = hasMetadata && hasVerifiedArchive(latest, archivePath)
			}
		}
		if !matched {
			return fmt.Errorf("%s differs from the signed catalog; include it in -prepare-release", entry.Name())
		}
	}
	return nil
}

func hasVerifiedArchive(release marketplace.Release, path string) bool {
	data, err := os.ReadFile(path)
	return err == nil && release.VerifyArtifact(data) == nil
}

func readOrFetchArchive(client *http.Client, registryURL, path string, release marketplace.Release) ([]byte, error) {
	for _, candidate := range []string{path, filepath.Join("marketplace-site", filepath.FromSlash(release.Artifact))} {
		if data, err := os.ReadFile(candidate); err == nil {
			if release.VerifyArtifact(data) != nil {
				return nil, fmt.Errorf("existing artifact %s does not match signed SHA-256", candidate)
			}
			return data, nil
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	request, err := http.NewRequest(http.MethodGet, strings.TrimRight(registryURL, "/")+"/"+release.Artifact, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", release.Artifact, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s returned %s", release.Artifact, response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if err := release.VerifyArtifact(data); err != nil {
		return nil, fmt.Errorf("downloaded %s failed signed SHA-256 verification", release.Artifact)
	}
	return data, nil
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".release-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0644); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// shortcut: only stable MAJOR.MINOR.PATCH versions are auto-bumped; add prerelease handling if the marketplace adopts prereleases.
var versionPattern = regexp.MustCompile(`^(v?)(\d+)\.(\d+)\.(\d+)$`)
var versionYAMLLine = regexp.MustCompile(`^(\s*version:\s*)(['"]?)([^'"\s#]+)(['"]?)(\s*(?:#.*)?)$`)

func nextPatchVersion(version string) (string, error) {
	matches := versionPattern.FindStringSubmatch(version)
	if matches == nil {
		return "", fmt.Errorf("unsupported version %q; expected MAJOR.MINOR.PATCH", version)
	}
	var patch int
	if _, err := fmt.Sscanf(matches[4], "%d", &patch); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%s.%s.%d", matches[1], matches[2], matches[3], patch+1), nil
}

func versionCompare(a, b string) int {
	ma, mb := versionPattern.FindStringSubmatch(a), versionPattern.FindStringSubmatch(b)
	if ma == nil || mb == nil {
		return strings.Compare(a, b)
	}
	for i := 2; i <= 4; i++ {
		var va, vb int
		fmt.Sscanf(ma[i], "%d", &va)
		fmt.Sscanf(mb[i], "%d", &vb)
		if va < vb {
			return -1
		}
		if va > vb {
			return 1
		}
	}
	return 0
}

func setMetadataVersion(data []byte, version string) ([]byte, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	if len(document.Content) == 0 {
		return nil, fmt.Errorf("empty YAML document")
	}
	root := document.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "metadata" || root.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(root.Content[i+1].Content); j += 2 {
			key, value := root.Content[i+1].Content[j], root.Content[i+1].Content[j+1]
			if key.Value != "version" {
				continue
			}
			lines := strings.Split(string(data), "\n")
			lineIndex := value.Line - 1
			if lineIndex < 0 || lineIndex >= len(lines) {
				break
			}
			line := lines[lineIndex]
			parts := versionYAMLLine.FindStringSubmatch(line)
			if parts == nil || parts[3] != value.Value || parts[2] != parts[4] {
				break
			}
			lines[lineIndex] = parts[1] + parts[2] + version + parts[4] + parts[5]
			return []byte(strings.Join(lines, "\n")), nil
		}
	}
	return nil, fmt.Errorf("metadata.version not found")
}
