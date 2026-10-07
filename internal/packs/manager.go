package packs

import (
	"crypto/sha256"
	"encoding/json"
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
	mutationMu    sync.Mutex
	mu            sync.RWMutex
	active        *Snapshot
	previous      *Snapshot
	packsDir      string
	backupFile    string
	backupContent []byte
	addedFile     string
}

type rollbackRecord struct {
	Filename string `json:"filename"`
	Existed  bool   `json:"existed"`
	Content  []byte `json:"content,omitempty"`
	Name     string `json:"name,omitempty"`
	Manifest []byte `json:"manifest,omitempty"`
}

type transactionRecord struct {
	Before        rollbackRecord `json:"before"`
	Undo          rollbackRecord `json:"undo"`
	PriorUndo     []byte         `json:"prior_undo,omitempty"`
	PriorManifest []byte         `json:"prior_manifest,omitempty"`
	NextManifest  []byte         `json:"next_manifest,omitempty"`
	Committed     bool           `json:"committed"`
}

type PackFileExpectation struct {
	Exists bool
	Digest string
}

func rollbackPath(dir string) string { return filepath.Join(dir, ".marketplace", "rollback.json") }
func transactionPath(dir string) string {
	return filepath.Join(dir, ".marketplace", "transaction.json")
}

func writeRecord(path string, record rollbackRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err = atomicWrite(path, data, 0600); err != nil {
		return err
	}
	return nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := RejectSymlinks(filepath.Dir(path), path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".worm-tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return atomicReplace(tmp, path)
}

func readUndo(dir string) ([]byte, error) {
	b, err := os.ReadFile(rollbackPath(dir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	return b, err
}

func restoreUndo(dir string, data []byte) error {
	if len(data) == 0 {
		if err := os.Remove(rollbackPath(dir)); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return atomicWrite(rollbackPath(dir), data, 0600)
}

func restoreManifest(dir string, data []byte) error {
	if len(data) == 0 {
		if err := os.Remove(manifestPath(dir)); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return atomicWrite(manifestPath(dir), data, 0600)
}

func validateRecord(record rollbackRecord) error {
	if record.Filename == "" || filepath.Base(record.Filename) != record.Filename || strings.ContainsAny(record.Filename, "/\\") || strings.Contains(record.Filename, "..") {
		return errors.New("unsafe pack transaction filename")
	}
	if ext := strings.ToLower(filepath.Ext(record.Filename)); ext != ".yaml" && ext != ".yml" {
		return errors.New("pack transaction filename must end in .yaml or .yml")
	}
	return nil
}

func writeRollback(dir string, record rollbackRecord) error {
	return writeRecord(rollbackPath(dir), record)
}
func writeTransaction(dir string, record rollbackRecord) error {
	return writeRecord(transactionPath(dir), record)
}

func writeTransactionRecord(dir string, record transactionRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return atomicWrite(transactionPath(dir), data, 0600)
}

func restoreRecord(dir string, record rollbackRecord) error {
	if err := validateRecord(record); err != nil {
		return err
	}
	target := filepath.Join(dir, record.Filename)
	if err := RejectSymlinks(dir, target); err != nil {
		return err
	}
	if record.Existed {
		return atomicWrite(target, record.Content, 0644)
	}
	return durableRemove(target)
}

// RecoverTransactions restores an interrupted on-disk pack mutation before LoadDir.
func RecoverTransactions(dir string) error {
	if err := RejectSymlinks(dir, filepath.Join(dir, ".marketplace")); err != nil {
		return err
	}
	data, err := os.ReadFile(transactionPath(dir))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var record transactionRecord
	if err = json.Unmarshal(data, &record); err != nil {
		return fmt.Errorf("invalid pack recovery record: %w", err)
	}
	if err = validateRecord(record.Before); err != nil {
		return err
	}
	if record.Committed {
		err = writeRollback(dir, record.Undo)
		if err == nil && len(record.NextManifest) > 0 {
			err = restoreManifest(dir, record.NextManifest)
		}
	} else {
		err = restoreRecord(dir, record.Before)
		if err == nil {
			err = restoreUndo(dir, record.PriorUndo)
		}
		if err == nil {
			err = restoreManifest(dir, record.PriorManifest)
		}
	}
	if err != nil {
		return fmt.Errorf("recover parser pack transaction: %w", err)
	}
	return os.Remove(transactionPath(dir))
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
	return m.ApplyPackFileWithState(filename, content, nil, "custom", false)
}

// ApplyPackFileExpected compares the current file state while holding the mutation lock.
func (m *SnapshotManager) ApplyPackFileExpected(filename string, content []byte, expected *PackFileExpectation) (*Snapshot, error) {
	return m.ApplyPackFileWithState(filename, content, expected, "custom", false)
}

func (m *SnapshotManager) ApplyPackFileWithState(filename string, content []byte, expected *PackFileExpectation, origin string, pinned bool) (*Snapshot, error) {
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	pack, err := LoadPack(strings.NewReader(string(content)))
	if err != nil {
		return nil, fmt.Errorf("pack validation failed: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.packsDir == "" {
		return nil, errors.New("packsDir not configured on SnapshotManager")
	}
	filename, target, before, priorUndo, priorManifest, err := m.beginFileTransaction(filename, expected)
	if err != nil {
		return nil, err
	}
	pending := transactionRecord{Before: before, PriorUndo: priorUndo, PriorManifest: priorManifest}
	if err = writeTransactionRecord(m.packsDir, pending); err != nil {
		return nil, fmt.Errorf("persist pack transaction: %w", err)
	}
	if err = atomicWrite(target, content, 0644); err != nil {
		return nil, m.abortTransaction(pending, fmt.Errorf("stage pack: %w", err))
	}
	newSnap, err := LoadDir(m.packsDir)
	if err != nil {
		return nil, m.abortTransaction(pending, fmt.Errorf("validate proposed pack set: %w", err))
	}
	nextManifest, err := nextManifestForApply(m.packsDir, priorManifest, pack.Metadata.Name, filename, content, origin, pinned, before)
	if err != nil {
		return nil, m.abortTransaction(pending, err)
	}
	pending.Committed = true
	pending.Undo = rollbackRecord{Filename: filename, Existed: before.Existed, Content: before.Content, Name: pack.Metadata.Name, Manifest: priorManifest}
	pending.NextManifest = nextManifest
	if err = writeTransactionRecord(m.packsDir, pending); err != nil {
		return nil, m.abortTransaction(transactionRecord{Before: before, PriorUndo: priorUndo, PriorManifest: priorManifest}, fmt.Errorf("commit pack transaction: %w", err))
	}
	if err = writeRollback(m.packsDir, pending.Undo); err != nil {
		return nil, m.abortTransaction(transactionRecord{Before: before, PriorUndo: priorUndo, PriorManifest: priorManifest}, fmt.Errorf("persist rollback state: %w", err))
	}
	if err = restoreManifest(m.packsDir, nextManifest); err != nil {
		return nil, m.abortTransaction(transactionRecord{Before: before, PriorUndo: priorUndo, PriorManifest: priorManifest}, fmt.Errorf("persist pack manifest: %w", err))
	}
	_ = os.Remove(transactionPath(m.packsDir)) // committed state and undo record are durable; recovery retries cleanup
	m.previous, m.active = m.active, newSnap
	m.addedFile = ""
	m.backupFile = target
	m.backupContent = before.Content
	return newSnap, nil
}

// RemovePackFile transactionally removes one active parser pack and keeps its prior bytes for rollback.
func (m *SnapshotManager) RemovePackFile(filename string) (*Snapshot, error) {
	return m.RemovePackFileExpected(filename, nil)
}

func (m *SnapshotManager) RemovePackFileExpected(filename string, expected *PackFileExpectation) (*Snapshot, error) {
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	if strings.ContainsAny(filename, "/\\") || strings.Contains(filename, "..") || filename == "" {
		return nil, errors.New("invalid pack filename")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.packsDir == "" {
		return nil, errors.New("packsDir not configured on SnapshotManager")
	}
	dir := filepath.Clean(m.packsDir)
	filename, target, before, priorUndo, priorManifest, err := m.beginFileTransaction(filename, expected)
	if err != nil {
		return nil, err
	}
	if !before.Existed {
		return nil, os.ErrNotExist
	}
	pack, err := LoadPack(strings.NewReader(string(before.Content)))
	if err != nil {
		return nil, fmt.Errorf("refuse to remove invalid pack file: %w", err)
	}
	nextManifest, err := nextManifestForRemove(dir, priorManifest, pack.Metadata.Name, before)
	if err != nil {
		return nil, err
	}
	pending := transactionRecord{Before: before, PriorUndo: priorUndo, PriorManifest: priorManifest}
	if err = writeTransactionRecord(dir, pending); err != nil {
		return nil, fmt.Errorf("persist pack transaction: %w", err)
	}
	if err = durableRemove(target); err != nil {
		return nil, m.abortTransaction(pending, err)
	}
	newSnap, err := LoadDir(dir)
	if err != nil {
		return nil, m.abortTransaction(pending, fmt.Errorf("validate proposed pack set: %w", err))
	}
	pending.Committed = true
	pending.Undo = rollbackRecord{Filename: filename, Existed: true, Content: before.Content, Name: pack.Metadata.Name, Manifest: priorManifest}
	pending.NextManifest = nextManifest
	if err = writeTransactionRecord(dir, pending); err != nil {
		return nil, m.abortTransaction(transactionRecord{Before: before, PriorUndo: priorUndo, PriorManifest: priorManifest}, err)
	}
	if err = writeRollback(dir, pending.Undo); err != nil {
		return nil, m.abortTransaction(transactionRecord{Before: before, PriorUndo: priorUndo, PriorManifest: priorManifest}, err)
	}
	if err = restoreManifest(dir, nextManifest); err != nil {
		return nil, m.abortTransaction(transactionRecord{Before: before, PriorUndo: priorUndo, PriorManifest: priorManifest}, err)
	}
	_ = os.Remove(transactionPath(dir))
	m.previous = m.active
	m.active = newSnap
	m.addedFile = ""
	m.backupFile = target
	m.backupContent = before.Content
	return newSnap, nil
}

func (m *SnapshotManager) beginFileTransaction(filename string, expected *PackFileExpectation) (string, string, rollbackRecord, []byte, []byte, error) {
	if m.packsDir == "" {
		return "", "", rollbackRecord{}, nil, nil, errors.New("packsDir not configured on SnapshotManager")
	}
	if strings.ContainsAny(filename, "/\\") || strings.Contains(filename, "..") || filename == "" {
		return "", "", rollbackRecord{}, nil, nil, errors.New("invalid pack filename")
	}
	if ext := strings.ToLower(filepath.Ext(filename)); ext != ".yaml" && ext != ".yml" {
		return "", "", rollbackRecord{}, nil, nil, errors.New("pack filename must end in .yaml or .yml")
	}
	dir := filepath.Clean(m.packsDir)
	if err := RecoverTransactions(dir); err != nil {
		return "", "", rollbackRecord{}, nil, nil, err
	}
	target := filepath.Join(dir, filename)
	if err := RejectSymlinks(dir, target); err != nil {
		return "", "", rollbackRecord{}, nil, nil, err
	}
	content, err := os.ReadFile(target)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return "", "", rollbackRecord{}, nil, nil, err
	}
	if expected != nil && (expected.Exists != exists || exists && expected.Digest != digestBytes(content)) {
		return "", "", rollbackRecord{}, nil, nil, errors.New("pack changed since the operation began")
	}
	priorUndo, err := readUndo(dir)
	if err != nil {
		return "", "", rollbackRecord{}, nil, nil, err
	}
	priorManifest, err := os.ReadFile(manifestPath(dir))
	if os.IsNotExist(err) {
		priorManifest = nil
	} else if err != nil {
		return "", "", rollbackRecord{}, nil, nil, err
	}
	return filename, target, rollbackRecord{Filename: filename, Existed: exists, Content: content}, priorUndo, priorManifest, nil
}

func digestBytes(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func nextManifestForApply(dir string, prior []byte, name, filename string, content []byte, origin string, pinned bool, before rollbackRecord) ([]byte, error) {
	m := EmptyManifest()
	if len(prior) > 0 {
		if err := json.Unmarshal(prior, &m); err != nil {
			return nil, fmt.Errorf("read pack manifest: %w", err)
		}
		if m.Entries == nil {
			m.Entries = map[string]PackInstall{}
		}
		if m.History == nil {
			m.History = map[string][]PackRevision{}
		}
	}
	entry, has := m.Entries[name]
	if before.Existed {
		old, err := LoadPack(strings.NewReader(string(before.Content)))
		if err != nil {
			return nil, err
		}
		if old.Metadata.Name != name {
			return nil, errors.New("refuse to replace a file whose pack identity differs from the requested pack")
		}
		rev := PackRevision{Filename: filename, Existed: true, Content: before.Content}
		if has {
			copy := entry
			rev.Previous = &copy
		}
		m.History[name] = append(m.History[name], rev)
	} else {
		if strings.TrimSuffix(filename, filepath.Ext(filename)) != name {
			return nil, errors.New("new pack filename must match pack metadata name")
		}
		m.History[name] = append(m.History[name], PackRevision{Filename: filename, Existed: false})
	}
	pack, err := LoadPack(strings.NewReader(string(content)))
	if err != nil {
		return nil, err
	}
	m.Entries[name] = PackInstall{Name: name, Filename: filename, Version: pack.Metadata.Version, Digest: digestBytes(content), Origin: origin, Pinned: pinned}
	return encodeManifest(m)
}

func nextManifestForRemove(dir string, prior []byte, name string, before rollbackRecord) ([]byte, error) {
	m := EmptyManifest()
	if len(prior) > 0 {
		if err := json.Unmarshal(prior, &m); err != nil {
			return nil, err
		}
		if m.Entries == nil {
			m.Entries = map[string]PackInstall{}
		}
		if m.History == nil {
			m.History = map[string][]PackRevision{}
		}
	}
	entry, has := m.Entries[name]
	rev := PackRevision{Filename: before.Filename, Existed: true, Content: before.Content}
	if has {
		copy := entry
		rev.Previous = &copy
	}
	m.History[name] = append(m.History[name], rev)
	delete(m.Entries, name)
	return encodeManifest(m)
}

func (m *SnapshotManager) abortTransaction(pending transactionRecord, cause error) error {
	if err := restoreRecord(m.packsDir, pending.Before); err != nil {
		return fmt.Errorf("%v; restore failed: %w", cause, err)
	}
	if err := restoreUndo(m.packsDir, pending.PriorUndo); err != nil {
		return fmt.Errorf("%v; restore prior rollback state failed: %w", cause, err)
	}
	if err := restoreManifest(m.packsDir, pending.PriorManifest); err != nil {
		return fmt.Errorf("%v; restore prior manifest failed: %w", cause, err)
	}
	if err := os.Remove(transactionPath(m.packsDir)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("%v; transaction cleanup failed: %w", cause, err)
	}
	return cause
}

// Rollback restores both the in-memory snapshot and the disk file state.
func (m *SnapshotManager) Rollback() error {
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	return m.rollbackInternal()
}

func (m *SnapshotManager) RollbackPack(name string) error {
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	manifest, err := ReadManifest(m.packsDir)
	if err != nil {
		return err
	}
	history := manifest.History[name]
	if len(history) == 0 {
		return fmt.Errorf("no previous state for pack %q", name)
	}
	revision := history[len(history)-1]
	if revision.Filename == "" {
		return errors.New("invalid pack history filename")
	}
	manifest.History[name] = history[:len(history)-1]
	if revision.Previous != nil {
		manifest.Entries[name] = *revision.Previous
	} else {
		delete(manifest.Entries, name)
	}
	next, err := encodeManifest(manifest)
	if err != nil {
		return err
	}
	return m.rollbackWithRecord(&rollbackRecord{Filename: revision.Filename, Existed: revision.Existed, Content: revision.Content, Name: name, Manifest: next})
}

func (m *SnapshotManager) rollbackInternal() error {
	return m.rollbackWithRecord(nil)
}

func (m *SnapshotManager) rollbackWithRecord(override *rollbackRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.packsDir != "" {
		if err := RecoverTransactions(m.packsDir); err != nil {
			return err
		}
		var record rollbackRecord
		if override != nil {
			record = *override
		} else if data, err := os.ReadFile(rollbackPath(m.packsDir)); err == nil {
			if json.Unmarshal(data, &record) != nil {
				return errors.New("invalid pack rollback record")
			}
		}
		if record.Filename != "" {
			if err := validateRecord(record); err != nil {
				return err
			}
			filename, _, current, priorUndo, priorManifest, err := m.beginFileTransaction(record.Filename, nil)
			if err != nil {
				return err
			}
			restoredManifest := record.Manifest
			pending := transactionRecord{Before: current, PriorUndo: priorUndo, PriorManifest: priorManifest}
			if err = writeTransactionRecord(m.packsDir, pending); err != nil {
				return err
			}
			if err = restoreRecord(m.packsDir, record); err != nil {
				return m.abortTransaction(pending, err)
			}
			if err = restoreManifest(m.packsDir, restoredManifest); err != nil {
				return m.abortTransaction(pending, err)
			}
			snap, err := LoadDir(m.packsDir)
			if err != nil {
				return m.abortTransaction(pending, fmt.Errorf("rollback would create an invalid pack set: %w", err))
			}
			undo := rollbackRecord{Filename: filename, Existed: current.Existed, Content: current.Content, Name: record.Name, Manifest: priorManifest}
			nextManifest, err := os.ReadFile(manifestPath(m.packsDir))
			if os.IsNotExist(err) {
				nextManifest = nil
			} else if err != nil {
				return m.abortTransaction(pending, err)
			}
			pending.Committed, pending.Undo, pending.NextManifest = true, undo, nextManifest
			if err = writeTransactionRecord(m.packsDir, pending); err != nil {
				return m.abortTransaction(transactionRecord{Before: current, PriorUndo: priorUndo, PriorManifest: priorManifest}, err)
			}
			if err = writeRollback(m.packsDir, undo); err != nil {
				return m.abortTransaction(transactionRecord{Before: current, PriorUndo: priorUndo, PriorManifest: priorManifest}, err)
			}
			if err = os.Remove(transactionPath(m.packsDir)); err != nil && !os.IsNotExist(err) {
				return err
			}
			m.active, m.previous = snap, nil
			return nil
		}
	}
	if m.previous == nil {
		return errors.New("no previous parser pack state available to rollback")
	}

	// Revert disk changes if applicable
	if m.addedFile != "" {
		if err := os.Remove(m.addedFile); err != nil && !os.IsNotExist(err) {
			return err
		}
		m.addedFile = ""
	}
	if m.backupFile != "" && m.backupContent != nil {
		if err := os.WriteFile(m.backupFile, m.backupContent, 0644); err != nil {
			return err
		}
		m.backupFile = ""
		m.backupContent = nil
	}

	m.active = m.previous
	m.previous = nil
	return nil
}

func recordForState(filename, target string) (rollbackRecord, error) {
	b, err := os.ReadFile(target)
	if os.IsNotExist(err) {
		return rollbackRecord{Filename: filename}, nil
	}
	if err != nil {
		return rollbackRecord{}, err
	}
	return rollbackRecord{Filename: filename, Existed: true, Content: b}, nil
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
			if e.Type()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("parser pack %s must not be a symlink", e.Name())
			}
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
