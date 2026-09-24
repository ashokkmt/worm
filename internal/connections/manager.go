package connections

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Manager coordinates the lifecycle, validation, atomic apply, and rollback of Connection resources.
type Manager struct {
	mu            sync.RWMutex
	dir           string
	sourcesDir    string
	sinksDir      string
	connections   map[string]*Connection
	filePaths     map[string]string
	previousState map[string]*Connection
	backupFile    string
	backupContent []byte
	addedFile     string
}

// NewManager creates a new Connection Manager.
func NewManager(dir string) *Manager {
	return &Manager{
		dir:         dir,
		connections: make(map[string]*Connection),
		filePaths:   make(map[string]string),
	}
}

// SetResourceDirs configures the dedicated directories for kind: Source and kind: Sink manifests.
func (m *Manager) SetResourceDirs(sourcesDir, sinksDir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sourcesDir = sourcesDir
	m.sinksDir = sinksDir
}

// LoadDir scans the configured directory and loads all valid connection YAML manifests.
func (m *Manager) LoadDir(dir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	targetDir := dir
	if targetDir == "" {
		targetDir = m.dir
	}
	if targetDir == "" {
		return errors.New("connection directory not configured")
	}

	entries, err := os.ReadDir(targetDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read connections dir: %w", err)
	}

	if m.connections == nil {
		m.connections = make(map[string]*Connection)
	}
	if m.filePaths == nil {
		m.filePaths = make(map[string]string)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}

		filePath := filepath.Join(targetDir, name)
		data, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", filePath, err)
		}

		conn, err := LoadConnection(strings.NewReader(string(data)))
		if err != nil {
			return fmt.Errorf("failed to load %s: %w", filePath, err)
		}
		m.connections[conn.Metadata.Name] = conn
		m.filePaths[conn.Metadata.Name] = filePath
	}

	return nil
}

// List returns all active connections sorted by name.
func (m *Manager) List() []*Connection {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list := make([]*Connection, 0, len(m.connections))
	for _, c := range m.connections {
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Metadata.Name < list[j].Metadata.Name
	})
	return list
}

// ListSummaries returns flattened summaries for all active connections.
func (m *Manager) ListSummaries() []ConnectionSummary {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list := make([]ConnectionSummary, 0, len(m.connections))
	for _, c := range m.connections {
		list = append(list, c.Summary())
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})
	return list
}

// GetRawYAML retrieves the raw YAML contents of a connection by name.
func (m *Manager) GetRawYAML(name string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if p, ok := m.filePaths[name]; ok && p != "" {
		if data, err := os.ReadFile(p); err == nil {
			return data, nil
		}
	}
	conn, ok := m.connections[name]
	if !ok {
		return nil, fmt.Errorf("connection %q not found", name)
	}
	return yaml.Marshal(conn)
}

// Get finds an active connection by name.
func (m *Manager) Get(name string) (*Connection, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	c, ok := m.connections[name]
	if !ok {
		return nil, fmt.Errorf("connection %q not found", name)
	}
	return c, nil
}

// Validate verifies a connection YAML stream without applying it.
func (m *Manager) Validate(r string) (*Connection, error) {
	return LoadConnection(strings.NewReader(r))
}

// ApplyFile stages, validates, tests connectivity, and commits a connection file atomically.
func (m *Manager) ApplyFile(filename string, content []byte) (*Connection, error) {
	// 1. Strict decode & schema/semantic validation
	candidate, err := LoadConnection(strings.NewReader(string(content)))
	if err != nil {
		return nil, fmt.Errorf("validation error: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	targetDir := m.dir
	if candidate.Kind == "Source" && m.sourcesDir != "" {
		targetDir = m.sourcesDir
	} else if candidate.Kind == "Sink" && m.sinksDir != "" {
		targetDir = m.sinksDir
	}

	if targetDir == "" {
		return nil, errors.New("connection directory not configured")
	}

	cleanBase := filepath.Clean(targetDir)
	if strings.ContainsAny(filename, "/\\") || strings.Contains(filename, "..") {
		return nil, errors.New("invalid filename: path traversal characters detected")
	}
	fn := filepath.Base(filename)
	targetPath := filepath.Join(cleanBase, fn)

	var existingContent []byte
	isNew := true
	if data, err := os.ReadFile(targetPath); err == nil {
		existingContent = data
		isNew = false
	}

	// 2. Stage temporary file
	tmpPath := targetPath + fmt.Sprintf(".tmp-%d", time.Now().UnixNano())
	if err := os.MkdirAll(cleanBase, 0755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(tmpPath, content, 0644); err != nil {
		return nil, fmt.Errorf("failed to write staging file: %w", err)
	}

	// 3. Atomic rename to targetPath
	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("failed to commit connection file: %w", err)
	}

	// Save rollback state
	m.previousState = make(map[string]*Connection, len(m.connections))
	for k, v := range m.connections {
		m.previousState[k] = v
	}

	if isNew {
		m.addedFile = targetPath
		m.backupFile = ""
		m.backupContent = nil
	} else {
		m.addedFile = ""
		m.backupFile = targetPath
		m.backupContent = existingContent
	}

	m.connections[candidate.Metadata.Name] = candidate
	if m.filePaths == nil {
		m.filePaths = make(map[string]string)
	}
	m.filePaths[candidate.Metadata.Name] = targetPath
	return candidate, nil
}

// Rollback restores the previous connection state and reverses on-disk file operations.
func (m *Manager) Rollback() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.previousState == nil {
		return errors.New("no previous connection state available to rollback")
	}

	if m.addedFile != "" {
		_ = os.Remove(m.addedFile)
		m.addedFile = ""
	}

	if m.backupFile != "" && m.backupContent != nil {
		_ = os.WriteFile(m.backupFile, m.backupContent, 0644)
		m.backupFile = ""
		m.backupContent = nil
	}

	m.connections = m.previousState
	m.previousState = nil
	return nil
}
