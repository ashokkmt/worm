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
// of immutable ParserSnapshots.
type SnapshotManager struct {
	mu       sync.RWMutex
	active   *Snapshot
	previous *Snapshot
}

// NewSnapshotManager creates a manager initialized with an optional starting snapshot.
func NewSnapshotManager(initial *Snapshot) *SnapshotManager {
	return &SnapshotManager{
		active: initial,
	}
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

// Rollback restores the previous snapshot if one exists.
func (m *SnapshotManager) Rollback() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.previous == nil {
		return errors.New("no previous snapshot available to rollback")
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
	return NewSnapshot(version, loadedPacks), nil
}
