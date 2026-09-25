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
	"strings"
	"sync"
	"time"
	"worm/internal/model"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress"
	"github.com/parquet-go/parquet-go/compress/gzip"
	"github.com/parquet-go/parquet-go/compress/snappy"
	"github.com/parquet-go/parquet-go/compress/zstd"
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
	parts := make([]string, 0, len(s.cfg.PartitionBy))
	for _, field := range s.cfg.PartitionBy {
		var value string
		switch field {
		case "event_date":
			value = event.Worm.ReceivedTime.UTC().Format("2006-01-02")
		case "category_name", "source_category":
			value = event.Worm.SourceCategory
		default:
			if v, ok := event.OCSF[field]; ok {
				value = fmt.Sprint(v)
			}
		}
		if value == "" {
			value = "unknown"
		}
		value = strings.Map(func(r rune) rune {
			if r == '/' || r == '\\' || r == 0 {
				return '_'
			}
			return r
		}, value)
		parts = append(parts, field+"="+value)
	}
	return filepath.Join(parts...)
}

func (s *ParquetSink) Flush() error {
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

type parquetRow struct {
	EventID        string `parquet:"event_id"`
	RawID          string `parquet:"raw_id"`
	RawSHA256      string `parquet:"raw_sha256"`
	RecordOrdinal  int64  `parquet:"record_ordinal"`
	SourceCategory string `parquet:"source_category"`
	SourceID       string `parquet:"source_id"`
	SchemaVersion  string `parquet:"schema_version"`
	ReceivedTimeMS int64  `parquet:"received_time_ms"`
	OCSFJSON       string `parquet:"ocsf_json"`
	EventJSON      string `parquet:"event_json"`
}

func parquetCompression(name string) (compress.Codec, error) {
	switch strings.ToLower(name) {
	case "", "zstd":
		return &zstd.Codec{}, nil
	case "snappy":
		return &snappy.Codec{}, nil
	case "gzip":
		return &gzip.Codec{}, nil
	case "none", "uncompressed":
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported parquet compression %q", name)
	}
}

func (s *ParquetSink) flushPartition(partKey string, events []*model.NormalizedEvent) error {
	if len(events) == 0 {
		return nil
	}
	targetDir := filepath.Join(s.cfg.Path, partKey)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("create partition: %w", err)
	}

	var randID [8]byte
	_, _ = rand.Read(randID[:])
	baseName := fmt.Sprintf("part-%x", randID)
	tmpPath := filepath.Join(targetDir, baseName+".tmp")
	finalPath := filepath.Join(targetDir, baseName+".parquet")
	codec, err := parquetCompression(s.cfg.Compression)
	if err != nil {
		return err
	}

	rows := make([]parquetRow, 0, len(events))
	var minTime, maxTime time.Time
	schemaVer := "1.3.0"
	for i, evt := range events {
		eventJSON, err := json.Marshal(evt)
		if err != nil {
			return err
		}
		ocsfJSON, err := json.Marshal(evt.OCSF)
		if err != nil {
			return err
		}
		t := evt.Worm.ReceivedTime.UTC()
		if i == 0 || t.Before(minTime) {
			minTime = t
		}
		if i == 0 || t.After(maxTime) {
			maxTime = t
		}
		if evt.Worm.SchemaVersion != "" {
			schemaVer = evt.Worm.SchemaVersion
		}
		rows = append(rows, parquetRow{evt.Worm.EventID, evt.Worm.RawID, evt.Worm.RawSHA256, int64(evt.Worm.RecordOrdinal), evt.Worm.SourceCategory, evt.Worm.SourceID, evt.Worm.SchemaVersion, t.UnixMilli(), string(ocsfJSON), string(eventJSON)})
	}

	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("open parquet temp file: %w", err)
	}
	opts := []parquet.WriterOption{}
	if codec != nil {
		opts = append(opts, parquet.Compression(codec))
	}
	w := parquet.NewGenericWriter[parquetRow](f, opts...)
	if _, err = w.Write(rows); err == nil {
		err = w.Close()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write parquet: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("commit parquet: %w", err)
	}

	data, err := os.ReadFile(finalPath)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if s.cfg.Manifest {
		relPath, _ := filepath.Rel(s.cfg.Path, finalPath)
		if err := s.updateManifest(ManifestFile{RelativePath: relPath, PartitionKey: partKey, RowCount: len(events), SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), MinTime: minTime, MaxTime: maxTime, SchemaVersion: schemaVer}); err != nil {
			return err
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
	return s.Flush()
}
