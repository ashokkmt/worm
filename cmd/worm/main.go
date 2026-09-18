package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"worm/internal/model"
	"worm/internal/packs"
	"worm/internal/pipeline"
	"worm/internal/rawstore"
)

// StdoutSink writes normalized events as NDJSON to stdout.
type StdoutSink struct{}

func (s *StdoutSink) Emit(ctx context.Context, event *model.NormalizedEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func main() {
	dbPath := flag.String("db", "data/worm.db", "Path to SQLite raw store database")
	packsDir := flag.String("packs", "packs", "Path to YAML parser packs directory")
	workers := flag.Int("workers", 4, "Number of concurrent pipeline workers")
	readStdin := flag.Bool("stdin", false, "Read logs from stdin line-by-line")
	verifyRawID := flag.String("verify", "", "Cryptographically verify a raw record by raw_id")
	flag.Parse()

	// Ensure directory exists for database
	if dir := filepath.Dir(*dbPath); dir != "." && dir != "" {
		_ = os.MkdirAll(dir, 0755)
	}

	store, err := rawstore.New(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: Failed to initialize raw store at %s: %v\n", *dbPath, err)
		os.Exit(1)
	}
	defer store.Close()

	// Verification mode
	if *verifyRawID != "" {
		ctx := context.Background()
		raw, err := store.Retrieve(ctx, *verifyRawID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Verification failed: %v\n", err)
			os.Exit(1)
		}
		matches, computed, err := store.Verify(ctx, *verifyRawID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Verification calculation failed: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("\n--- Cryptographic Integrity Report ---\n")
		fmt.Printf("Raw ID:       %s\n", raw.RawID)
		fmt.Printf("Received:     %s\n", raw.ReceivedAt.Format(time.RFC3339Nano))
		fmt.Printf("Transport:    %s (%s)\n", raw.Transport, raw.SourceIP)
		fmt.Printf("Stored Hash:  %s\n", raw.RawSHA256)
		fmt.Printf("Actual Hash:  %s\n", computed)
		fmt.Printf("Integrity:    ")
		if matches {
			fmt.Println("PASSED (Cryptographically Exact)")
		} else {
			fmt.Println("FAILED (TAMPER DETECTED)")
		}
		fmt.Printf("Byte Count:   %d bytes\n", raw.ByteCount)
		fmt.Printf("Payload Preview:\n%s\n", string(raw.Payload))
		fmt.Printf("--------------------------------------\n")
		return
	}

	fmt.Fprintf(os.Stderr, "========================================================\n")
	fmt.Fprintf(os.Stderr, " WORM: Universal Log Pre-processing Framework (SIH 26156)\n")
	var packManager *packs.SnapshotManager
	if *packsDir != "" {
		if _, err := os.Stat(*packsDir); err == nil {
			snap, err := packs.LoadDir(*packsDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "WARNING: Failed to load parser packs from %s: %v\n", *packsDir, err)
			} else {
				packManager = packs.NewSnapshotManager(snap)
				fmt.Fprintf(os.Stderr, " Loaded %d parser packs from %s\n", len(snap.ListPacks()), *packsDir)
			}
		}
	}

	p := pipeline.New(pipeline.Config{
		Workers:     *workers,
		BufferSize:  1000,
		PackManager: packManager,
	}, store, &StdoutSink{})
	p.Start()

	// Graceful signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	doneReading := make(chan struct{})

	// Check if stdin is requested or is a pipe
	stat, _ := os.Stdin.Stat()
	isPipe := (stat.Mode() & os.ModeCharDevice) == 0

	if *readStdin || isPipe {
		go func() {
			defer close(doneReading)
			scanner := bufio.NewScanner(os.Stdin)
			// Increase buffer capacity for large log lines (up to 1MB)
			buf := make([]byte, 64*1024)
			scanner.Buffer(buf, 1024*1024)

			for scanner.Scan() {
				line := scanner.Bytes()
				if len(line) == 0 {
					continue
				}

				// Make an isolated copy of line bytes
				payload := make([]byte, len(line))
				copy(payload, line)

				rec := model.IngestedRecord{
					Transport:  "stdin",
					SourceIP:   "127.0.0.1",
					SourcePort: 0,
					RawBytes:   payload,
					ReceivedAt: time.Now().UTC(),
				}

				if err := p.SubmitSync(context.Background(), rec); err != nil {
					fmt.Fprintf(os.Stderr, "WARN: failed to ingest record: %v\n", err)
				}
			}

			if err := scanner.Err(); err != nil && err != io.EOF {
				fmt.Fprintf(os.Stderr, "WARN: stdin scanner error: %v\n", err)
			}
		}()
	} else {
		fmt.Fprintf(os.Stderr, "Pipeline running. Waiting for events (press Ctrl+C to terminate)...\n")
	}

	// Wait for shutdown signal or stdin completion
	select {
	case <-sigChan:
		fmt.Fprintf(os.Stderr, "\nReceived shutdown signal. Stopping pipeline gracefully...\n")
	case <-doneReading:
		fmt.Fprintf(os.Stderr, "Finished reading from stdin. Draining pipeline...\n")
	}

	p.Stop()

	stats := p.Stats()
	valid, reason := stats.VerifyInvariant()

	fmt.Fprintf(os.Stderr, "\n--- Final Pipeline Loss Accounting ---\n")
	fmt.Fprintf(os.Stderr, "Accepted:    %d\n", stats.Accepted)
	fmt.Fprintf(os.Stderr, "Normalized:  %d\n", stats.Normalized)
	fmt.Fprintf(os.Stderr, "Quarantined: %d\n", stats.Quarantined)
	fmt.Fprintf(os.Stderr, "Pending:     %d\n", stats.Pending)
	fmt.Fprintf(os.Stderr, "Delivered:   %d\n", stats.Delivered)
	fmt.Fprintf(os.Stderr, "Status:      %s\n", reason)
	fmt.Fprintf(os.Stderr, "--------------------------------------\n")

	if !valid {
		os.Exit(2)
	}
}
