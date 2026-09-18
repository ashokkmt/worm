package output_test

import (
	"bufio"
	"context"
	"encoding/json"
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
