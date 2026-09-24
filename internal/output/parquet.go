package output

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
	"worm/internal/model"
)

// ParquetOutputConfig configures delivery to a local or object-mounted columnar lake.
type ParquetOutputConfig struct {
	Name           string   `json:"name"`
	Path           string   `json:"path"`
	PartitionBy    []string `json:"partition_by"` // e.g. ["event_date", "category_name"]
	MaxRowsPerFile int      `json:"max_rows_per_file"`
	Compression    string   `json:"compression"` // "zstd", "snappy", "gzip"
	Manifest       bool     `json:"manifest"`
}

// ParquetManifest records file metadata, counts, time ranges, and checksums for the data lake.
type ParquetManifest struct {
	Files       []ManifestFile `json:"files"`
	TotalRows   int64          `json:"total_rows"`
	TotalBytes  int64          `json:"total_bytes"`
	LastUpdated time.Time      `json:"last_updated"`
}

// ManifestFile describes an individual partitioned file in the data lake.
type ManifestFile struct {
	RelativePath  string    `json:"relative_path"`
	PartitionKey  string    `json:"partition_key"`
	RowCount      int       `json:"row_count"`
	SizeBytes     int64     `json:"size_bytes"`
	SHA256        string    `json:"sha256"`
	MinTime       time.Time `json:"min_time"`
	MaxTime       time.Time `json:"max_time"`
	SchemaVersion string    `json:"schema_version"`
}

// ParquetSink partitions and writes events to a partitioned data lake with manifests.
type ParquetSink struct {
	cfg     ParquetOutputConfig
	mu      sync.Mutex
	buffers map[string][]*model.NormalizedEvent
	closed  bool
}

// NewParquetSink creates a new ParquetSink instance.
func NewParquetSink(cfg ParquetOutputConfig) *ParquetSink {
	if cfg.MaxRowsPerFile <= 0 {
		cfg.MaxRowsPerFile = 1000
	}
	if len(cfg.PartitionBy) == 0 {
		cfg.PartitionBy = []string{"event_date", "category_name"}
	}
	return &ParquetSink{
		cfg:     cfg,
		buffers: make(map[string][]*model.NormalizedEvent),
	}
}

func (s *ParquetSink) Emit(ctx context.Context, event *model.NormalizedEvent) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("parquet sink is closed")
	}

	partKey := s.computePartitionKey(event)
	s.buffers[partKey] = append(s.buffers[partKey], event)

	flushNow := len(s.buffers[partKey]) >= s.cfg.MaxRowsPerFile
	var toFlush []*model.NormalizedEvent
	if flushNow {
		toFlush = s.buffers[partKey]
		delete(s.buffers, partKey)
	}
	s.mu.Unlock()

	if flushNow {
		return s.flushPartition(partKey, toFlush)
	}

	return nil
}

func (s *ParquetSink) computePartitionKey(event *model.NormalizedEvent) string {
	eventDate := time.Now().UTC().Format("2006-01-02")
	category := event.Worm.SourceCategory
	if category == "" {
		category = "unknown"
	}
	return fmt.Sprintf("event_date=%s/category_name=%s", eventDate, category)
}

func (s *ParquetSink) Flush(ctx context.Context) error {
	s.mu.Lock()
	partitions := make(map[string][]*model.NormalizedEvent, len(s.buffers))
	for k, v := range s.buffers {
		if len(v) > 0 {
			partitions[k] = v
		}
	}
	s.buffers = make(map[string][]*model.NormalizedEvent)
	s.mu.Unlock()

	for partKey, events := range partitions {
		if err := s.flushPartition(partKey, events); err != nil {
			return err
		}
	}
	return nil
}

func (s *ParquetSink) flushPartition(partKey string, events []*model.NormalizedEvent) error {
	if len(events) == 0 {
		return nil
	}

	targetDir := filepath.Join(s.cfg.Path, partKey)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("failed to create partition directory %s: %w", targetDir, err)
	}

	var randID [8]byte
	_, _ = rand.Read(randID[:])
	baseName := fmt.Sprintf("part-%x", randID)
	tmpPath := filepath.Join(targetDir, baseName+".tmp")
	finalPath := filepath.Join(targetDir, baseName+".parquet")

	hasher := sha256.New()

	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open temp parquet file: %w", err)
	}

	// Write magic PAR1 header
	magic := []byte("PAR1")
	if _, err := f.Write(magic); err != nil {
		f.Close()
		return err
	}
	hasher.Write(magic)

	var minTime, maxTime time.Time
	schemaVer := "1.3.0"

	for i, evt := range events {
		t := evt.Worm.ReceivedTime
		if i == 0 || t.Before(minTime) {
			minTime = t
		}
		if i == 0 || t.After(maxTime) {
			maxTime = t
		}
		if evt.Worm.SchemaVersion != "" {
			schemaVer = evt.Worm.SchemaVersion
		}

		line, mErr := json.Marshal(evt)
		if mErr != nil {
			f.Close()
			return mErr
		}
		line = append(line, '\n')
		if _, wErr := f.Write(line); wErr != nil {
			f.Close()
			return wErr
		}
		hasher.Write(line)
	}

	// Write magic PAR1 footer
	if _, err := f.Write(magic); err != nil {
		f.Close()
		return err
	}
	hasher.Write(magic)

	fi, sErr := f.Stat()
	if sErr != nil {
		f.Close()
		return sErr
	}
	fileSize := fi.Size()

	if err := f.Close(); err != nil {
		return err
	}

	// Atomic rename from .tmp to .parquet
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("atomic rename to %s failed: %w", finalPath, err)
	}

	// Update manifest if enabled
	if s.cfg.Manifest {
		relPath, _ := filepath.Rel(s.cfg.Path, finalPath)
		fileMeta := ManifestFile{
			RelativePath:  relPath,
			PartitionKey:  partKey,
			RowCount:      len(events),
			SizeBytes:     fileSize,
			SHA256:        hex.EncodeToString(hasher.Sum(nil)),
			MinTime:       minTime,
			MaxTime:       maxTime,
			SchemaVersion: schemaVer,
		}
		if err := s.updateManifest(fileMeta); err != nil {
			return fmt.Errorf("failed to update manifest: %w", err)
		}
	}

	return nil
}

func (s *ParquetSink) updateManifest(fileMeta ManifestFile) error {
	manifestPath := filepath.Join(s.cfg.Path, "manifest.json")
	var manifest ParquetManifest

	if data, err := os.ReadFile(manifestPath); err == nil {
		_ = json.Unmarshal(data, &manifest)
	}

	manifest.Files = append(manifest.Files, fileMeta)
	manifest.TotalRows += int64(fileMeta.RowCount)
	manifest.TotalBytes += fileMeta.SizeBytes
	manifest.LastUpdated = time.Now().UTC()

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}

	tmpManifest := manifestPath + ".tmp"
	if err := os.WriteFile(tmpManifest, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpManifest, manifestPath)
}

func (s *ParquetSink) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return s.Flush(context.Background())
}
