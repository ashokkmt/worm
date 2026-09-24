package packs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// SnapshotManager coordinates the safe, thread-safe atomic activation and rollback
// of immutable ParserSnapshots, including disk persistence and rollback.
type SnapshotManager struct {
	mu            sync.RWMutex
	active        *Snapshot
	previous      *Snapshot
	packsDir      string
	backupFile    string
	backupContent []byte
	addedFile     string
}

// NewSnapshotManager creates a manager initialized with an optional starting snapshot.
func NewSnapshotManager(initial *Snapshot) *SnapshotManager {
	return &SnapshotManager{
		active: initial,
	}
}

// SetPacksDir configures the directory managed by this SnapshotManager for persistence.
func (m *SnapshotManager) SetPacksDir(dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.packsDir = dir
}

// Active returns the currently active immutable snapshot.
func (m *SnapshotManager) Active() *Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.active
}

// Activate atomically swaps the active snapshot with the new snapshot,
// preserving the current snapshot as previous for rollback capability.
func (m *SnapshotManager) Activate(newSnap *Snapshot) error {
	if newSnap == nil {
		return errors.New("cannot activate nil snapshot")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.previous = m.active
	m.active = newSnap
	return nil
}

// ApplyPackFile writes a pack file transactionally to packsDir, verifying that the entire
// snapshot remains valid. If invalid, changes on disk are rolled back immediately.
// If valid, the new snapshot is activated and rollback state is preserved.
func (m *SnapshotManager) ApplyPackFile(filename string, content []byte) (*Snapshot, error) {
	// 1. Pre-validate individual pack
	if _, err := LoadPack(strings.NewReader(string(content))); err != nil {
		return nil, fmt.Errorf("pack validation failed: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.packsDir == "" {
		return nil, errors.New("packsDir not configured on SnapshotManager")
	}

	cleanBase := filepath.Clean(m.packsDir)
	if strings.ContainsAny(filename, "/\\") || strings.Contains(filename, "..") {
		return nil, errors.New("invalid pack filename: path traversal characters detected")
	}
	fn := filepath.Base(filename)
	if fn == "." || fn == "" {
		return nil, errors.New("invalid pack filename")
	}
	targetPath := filepath.Join(cleanBase, fn)

	// Check if target already exists to record backup
	var existingContent []byte
	isNew := true
	if data, err := os.ReadFile(targetPath); err == nil {
		existingContent = data
		isNew = false
	}

	// Stage file atomically via temporary write + rename
	tmpPath := targetPath + fmt.Sprintf(".tmp-%d", time.Now().UnixNano())
	if err := os.WriteFile(tmpPath, content, 0644); err != nil {
		return nil, fmt.Errorf("failed to write staging file: %w", err)
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("failed to move staged pack file: %w", err)
	}

	// 2. Validate full snapshot from directory
	newSnap, err := LoadDir(m.packsDir)
	if err != nil {
		// ROLLBACK DISK IMMEDIATELY: restore original state
		if isNew {
			_ = os.Remove(targetPath)
		} else {
			_ = os.WriteFile(targetPath, existingContent, 0644)
		}
		return nil, fmt.Errorf("snapshot validation failed after apply (disk rolled back): %w", err)
	}

	// Apply successful: record disk rollback information
	if isNew {
		m.addedFile = targetPath
		m.backupFile = ""
		m.backupContent = nil
	} else {
		m.addedFile = ""
		m.backupFile = targetPath
		m.backupContent = existingContent
	}

	m.previous = m.active
	m.active = newSnap
	return newSnap, nil
}

// Rollback restores both the in-memory snapshot and the disk file state.
func (m *SnapshotManager) Rollback() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.previous == nil {
		return errors.New("no previous snapshot available to rollback")
	}

	// Revert disk changes if applicable
	if m.addedFile != "" {
		_ = os.Remove(m.addedFile)
		m.addedFile = ""
	}
	if m.backupFile != "" && m.backupContent != nil {
		_ = os.WriteFile(m.backupFile, m.backupContent, 0644)
		m.backupFile = ""
		m.backupContent = nil
	}

	m.active = m.previous
	m.previous = nil
	return nil
}

// LoadDir reads and parses all .yaml and .yml files from a directory,
// validates each pack, and returns a compiled immutable Snapshot.
func LoadDir(dir string) (*Snapshot, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read parser packs directory: %w", err)
	}

	var packFiles []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".yaml" || ext == ".yml" {
			packFiles = append(packFiles, filepath.Join(dir, e.Name()))
		}
	}

	sort.Strings(packFiles)

	var loadedPacks []*ParserPack
	for _, file := range packFiles {
		f, err := os.Open(file)
		if err != nil {
			return nil, fmt.Errorf("failed to open %s: %w", file, err)
		}
		pack, err := LoadPack(f)
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("error loading pack from %s: %w", file, err)
		}
		loadedPacks = append(loadedPacks, pack)
	}

	version := fmt.Sprintf("snap-%d", time.Now().UnixNano())
	return NewSnapshot(version, loadedPacks)
}
