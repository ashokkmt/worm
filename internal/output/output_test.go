package output_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"worm/internal/model"
	"worm/internal/output"
)

func sampleEvent(id string) *model.NormalizedEvent {
	return &model.NormalizedEvent{
		Worm: model.WormEnvelope{
			EventID:        id,
			RawID:          "worm-raw-001",
			RawSHA256:      "abcdef",
			RawBytes:       128,
			RecordOrdinal:  0,
			SourceCategory: "network_device",
			SourceID:       "10.0.1.50",
			ParserPack:     "test-pack",
			ParserVersion:  "1.0.0",
			CoreVersion:    "0.1.0",
			SchemaVersion:  "1.3.0",
			ReceivedTime:   time.Now().UTC(),
			Status:         "normalized",
		},
		OCSF: map[string]any{
			"message": "connection allowed",
			"action":  "allow",
		},
	}
}

func TestNDJSONSink(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "worm_output_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	outPath := filepath.Join(tempDir, "events.ndjson")
	sink, err := output.NewNDJSONSink(outPath)
	if err != nil {
		t.Fatalf("failed to create NDJSON sink: %v", err)
	}

	ctx := context.Background()
	evt1 := sampleEvent("evt-1")
	evt2 := sampleEvent("evt-2")

	if err := sink.Emit(ctx, evt1); err != nil {
		t.Fatalf("failed to emit evt1: %v", err)
	}
	if err := sink.Emit(ctx, evt2); err != nil {
		t.Fatalf("failed to emit evt2: %v", err)
	}

	if err := sink.Flush(); err != nil {
		t.Fatalf("failed to flush sink: %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("failed to close sink: %v", err)
	}

	// Verify file content
	file, err := os.Open(outPath)
	if err != nil {
		t.Fatalf("failed to open output file: %v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var readEvents []*model.NormalizedEvent
	for scanner.Scan() {
		var evt model.NormalizedEvent
		if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
			t.Fatalf("failed to unmarshal line: %v", err)
		}
		readEvents = append(readEvents, &evt)
	}

	if len(readEvents) != 2 {
		t.Fatalf("expected 2 lines in ndjson, got %d", len(readEvents))
	}
	if readEvents[0].Worm.EventID != "evt-1" || readEvents[1].Worm.EventID != "evt-2" {
		t.Errorf("event IDs do not match: %v, %v", readEvents[0].Worm.EventID, readEvents[1].Worm.EventID)
	}
}

func TestMultiSink(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "worm_multi_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	path1 := filepath.Join(tempDir, "sink1.ndjson")
	path2 := filepath.Join(tempDir, "sink2.ndjson")

	s1, err := output.NewNDJSONSink(path1)
	if err != nil {
		t.Fatalf("failed to create s1: %v", err)
	}
	s2, err := output.NewNDJSONSink(path2)
	if err != nil {
		t.Fatalf("failed to create s2: %v", err)
	}
	stdout := output.NewStdoutSink()

	multi := output.NewMultiSink(s1, s2, stdout)
	ctx := context.Background()

	evt := sampleEvent("multi-evt-1")
	if err := multi.Emit(ctx, evt); err != nil {
		t.Fatalf("multi emit failed: %v", err)
	}

	if err := multi.Flush(); err != nil {
		t.Fatalf("multi flush failed: %v", err)
	}
	if err := multi.Close(); err != nil {
		t.Fatalf("multi close failed: %v", err)
	}

	// Verify both files got the event
	for _, p := range []string{path1, path2} {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("failed to read %s: %v", p, err)
		}
		if len(data) == 0 {
			t.Errorf("expected non-empty file for %s", p)
		}
	}
}

type FlakySink struct {
	failCount int
	calls     int
}

func (f *FlakySink) Emit(ctx context.Context, event *model.NormalizedEvent) error {
	f.calls++
	if f.calls <= f.failCount {
		return os.ErrDeadlineExceeded
	}
	return nil
}
func (f *FlakySink) Flush() error { return nil }
func (f *FlakySink) Close() error { return nil }

func TestRetrySink_BA022(t *testing.T) {
	ctx := context.Background()
	evt := sampleEvent("retry-evt")

	// 1. Recoverable failure (fails 2 times, succeeds on 3rd attempt)
	flaky := &FlakySink{failCount: 2}
	retrySink := output.NewRetrySink(flaky, 3, 1*time.Millisecond)
	if err := retrySink.Emit(ctx, evt); err != nil {
		t.Fatalf("expected retry to succeed on 3rd attempt, got: %v", err)
	}
	if flaky.calls != 3 {
		t.Errorf("expected 3 calls, got %d", flaky.calls)
	}

	// 2. Unrecoverable failure (fails all attempts)
	alwaysFails := &FlakySink{failCount: 10}
	failingRetry := output.NewRetrySink(alwaysFails, 3, 1*time.Millisecond)
	if err := failingRetry.Emit(ctx, evt); err == nil {
		t.Fatalf("expected error when all retry attempts exhausted, got nil")
	}
	if alwaysFails.calls != 3 {
		t.Errorf("expected exactly 3 calls, got %d", alwaysFails.calls)
	}
}

func TestHTTPSIEMSink_Deliver(t *testing.T) {
	var receivedPayload []byte
	var receivedContentType string
	var receivedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContentType = r.Header.Get("Content-Type")
		receivedAuth = r.Header.Get("Authorization")
		data, _ := io.ReadAll(r.Body)
		receivedPayload = data
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := output.HTTPOutputConfig{
		URL:         server.URL,
		Format:      "ndjson",
		BatchEvents: 2,
		AuthHeader:  "Authorization",
		AuthToken:   "Bearer secret-token-123",
	}
	sink := output.NewHTTPSIEMSink(cfg)

	ctx := context.Background()
	_ = sink.Emit(ctx, sampleEvent("siem-evt-1"))
	_ = sink.Emit(ctx, sampleEvent("siem-evt-2"))

	if err := sink.Close(); err != nil {
		t.Fatalf("sink.Close failed: %v", err)
	}

	if receivedContentType != "application/x-ndjson" {
		t.Errorf("expected application/x-ndjson, got %s", receivedContentType)
	}
	if receivedAuth != "Bearer secret-token-123" {
		t.Errorf("expected Bearer secret-token-123, got %s", receivedAuth)
	}
	if !bytes.Contains(receivedPayload, []byte("siem-evt-1")) || !bytes.Contains(receivedPayload, []byte("siem-evt-2")) {
		t.Errorf("missing events in payload: %s", string(receivedPayload))
	}
}

func TestKafkaSink_Deliver(t *testing.T) {
	memProducer := output.NewMemoryKafkaProducer()
	cfg := output.KafkaOutputConfig{
		Topic: "worm.normalized.v1",
	}
	sink := output.NewKafkaSink(cfg, memProducer)

	ctx := context.Background()
	_ = sink.Emit(ctx, sampleEvent("kafka-evt-1"))
	_ = sink.Emit(ctx, sampleEvent("kafka-evt-2"))

	if err := sink.Close(); err != nil {
		t.Fatalf("sink.Close failed: %v", err)
	}

	msgs := memProducer.Messages()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages produced, got %d", len(msgs))
	}
	if msgs[0].Topic != "worm.normalized.v1" {
		t.Errorf("expected topic worm.normalized.v1, got %s", msgs[0].Topic)
	}
	if !bytes.Contains(msgs[0].Value, []byte("kafka-evt-1")) {
		t.Errorf("expected kafka-evt-1 in value, got %s", string(msgs[0].Value))
	}
}

func TestParquetSink_PartitionAndManifest(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "worm_parquet_lake_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := output.ParquetOutputConfig{
		Path:           tempDir,
		MaxRowsPerFile: 10,
		Manifest:       true,
	}
	sink := output.NewParquetSink(cfg)

	ctx := context.Background()
	evt1 := sampleEvent("lake-evt-1")
	evt1.Worm.SourceCategory = "network_device"

	evt2 := sampleEvent("lake-evt-2")
	evt2.Worm.SourceCategory = "cloud"

	_ = sink.Emit(ctx, evt1)
	_ = sink.Emit(ctx, evt2)

	if err := sink.Close(); err != nil {
		t.Fatalf("sink.Close failed: %v", err)
	}

	// Verify manifest.json exists
	manifestPath := filepath.Join(tempDir, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("manifest.json not found: %v", err)
	}

	var manifest output.ParquetManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("failed to parse manifest.json: %v", err)
	}

	if manifest.TotalRows != 2 {
		t.Errorf("expected total_rows=2, got %d", manifest.TotalRows)
	}
	if len(manifest.Files) != 2 {
		t.Errorf("expected 2 partitioned files, got %d", len(manifest.Files))
	}

	// Verify each parquet file has PAR1 magic header and footer
	for _, f := range manifest.Files {
		fullPath := filepath.Join(tempDir, f.RelativePath)
		fileData, err := os.ReadFile(fullPath)
		if err != nil {
			t.Fatalf("failed to read parquet file %s: %v", fullPath, err)
		}
		if len(fileData) < 8 {
			t.Fatalf("file %s is too short", fullPath)
		}
		if string(fileData[:4]) != "PAR1" || string(fileData[len(fileData)-4:]) != "PAR1" {
			t.Errorf("file %s missing PAR1 magic header/footer", fullPath)
		}
	}
}
