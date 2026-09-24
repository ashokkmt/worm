package output_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"worm/internal/output"
)

func BenchmarkNDJSONSink_Emit(b *testing.B) {
	tempDir, err := os.MkdirTemp("", "worm_bench_ndjson_*")
	if err != nil {
		b.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	outPath := filepath.Join(tempDir, "bench_events.ndjson")
	sink, err := output.NewNDJSONSink(outPath)
	if err != nil {
		b.Fatal(err)
	}
	defer sink.Close()

	ctx := context.Background()
	evt := sampleEvent("bench-ndjson-001")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := sink.Emit(ctx, evt); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkKafkaSink_Emit(b *testing.B) {
	memProducer := output.NewMemoryKafkaProducer()
	cfg := output.KafkaOutputConfig{
		Topic: "worm.normalized.v1",
	}
	sink := output.NewKafkaSink(cfg, memProducer)
	defer sink.Close()

	ctx := context.Background()
	evt := sampleEvent("bench-kafka-001")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := sink.Emit(ctx, evt); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParquetSink_Emit(b *testing.B) {
	tempDir, err := os.MkdirTemp("", "worm_bench_parquet_*")
	if err != nil {
		b.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	cfg := output.ParquetOutputConfig{
		Path:           tempDir,
		MaxRowsPerFile: 100000,
		Manifest:       true,
	}
	sink := output.NewParquetSink(cfg)
	defer sink.Close()

	ctx := context.Background()
	evt := sampleEvent("bench-parquet-001")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		evt.Worm.EventID = fmt.Sprintf("bench-parquet-%d", i)
		if err := sink.Emit(ctx, evt); err != nil {
			b.Fatal(err)
		}
	}
}
