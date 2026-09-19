package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"worm/internal/ingest"
	"worm/internal/model"
	"worm/internal/output"
	"worm/internal/packs"
	"worm/internal/pipeline"
	"worm/internal/rawstore"
)

func main() {
	dbPath := flag.String("db", "data/worm.db", "Path to SQLite raw store database")
	packsDir := flag.String("packs", "packs", "Path to YAML parser packs directory")
	workers := flag.Int("workers", 4, "Number of concurrent pipeline workers")
	readStdin := flag.Bool("stdin", false, "Read logs from stdin line-by-line")
	verifyRawID := flag.String("verify", "", "Cryptographically verify a raw record by raw_id")

	// Multi-transport ingestion flags
	syslogUDP := flag.String("syslog-udp", ":514", "UDP address for Syslog listener (:1514 for non-root, 'none' to disable)")
	syslogTCP := flag.String("syslog-tcp", ":514", "TCP address for Syslog listener (:1514 for non-root, 'none' to disable)")
	httpAddr := flag.String("http", ":8080", "HTTP address for REST ingestion ('none' to disable)")
	httpKey := flag.String("http-key", "", "Optional API key for HTTP POST authentication")
	inboxDir := flag.String("inbox", "data/inbox", "Path to monitored spool inbox directory ('none' to disable)")
	inputFile := flag.String("file", "", "One-shot batch file to ingest immediately")

	// Output sink flags
	outputFile := flag.String("output-file", "data/output/normalized.ndjson", "Path to write normalized NDJSON output ('none' to disable)")
	stdoutOutput := flag.Bool("stdout", true, "Emit normalized NDJSON events to stdout")

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

	// 1. Load Parser Packs
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

	// 2. Configure Output Sinks
	var activeSinks []output.OutputSink
	if *stdoutOutput {
		activeSinks = append(activeSinks, output.NewStdoutSink())
	}
	if *outputFile != "" && *outputFile != "none" {
		fileSink, err := output.NewNDJSONSink(*outputFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: Failed to create NDJSON file sink at %s: %v\n", *outputFile, err)
		} else {
			activeSinks = append(activeSinks, fileSink)
			fmt.Fprintf(os.Stderr, " Output sink active: %s (NDJSON)\n", *outputFile)
		}
	}

	var compositeSink output.OutputSink
	if len(activeSinks) > 0 {
		compositeSink = output.NewMultiSink(activeSinks...)
	} else {
		compositeSink = output.NewStdoutSink()
	}

	// 3. Initialize & Start Pipeline
	p := pipeline.New(pipeline.Config{
		Workers:     *workers,
		BufferSize:  2000,
		PackManager: packManager,
	}, store, compositeSink)
	p.Start()

	// 4. Initialize Ingestion Adapters
	mgr := ingest.NewManager(2000)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if *syslogUDP != "" && *syslogUDP != "none" {
		mgr.Register(ingest.NewSyslogUDPListener(*syslogUDP))
		fmt.Fprintf(os.Stderr, " Ingest adapter configured: Syslog UDP on %s\n", *syslogUDP)
	}
	if *syslogTCP != "" && *syslogTCP != "none" {
		mgr.Register(ingest.NewSyslogTCPListener(*syslogTCP))
		fmt.Fprintf(os.Stderr, " Ingest adapter configured: Syslog TCP on %s\n", *syslogTCP)
	}
	if *httpAddr != "" && *httpAddr != "none" {
		mgr.Register(ingest.NewHTTPListener(*httpAddr, *httpKey))
		fmt.Fprintf(os.Stderr, " Ingest adapter configured: HTTP POST on %s/api/v1/ingest\n", *httpAddr)
	}
	if *inboxDir != "" && *inboxDir != "none" {
		mgr.Register(ingest.NewFileWatcher(*inboxDir, 1*time.Second))
		fmt.Fprintf(os.Stderr, " Ingest adapter configured: Spool Inbox on %s\n", *inboxDir)
	}

	if err := mgr.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: Ingest manager start error: %v\n", err)
	}
	p.ConnectIngest(ctx, mgr.Channel())

	hasListeners := (*syslogUDP != "" && *syslogUDP != "none") ||
		(*syslogTCP != "" && *syslogTCP != "none") ||
		(*httpAddr != "" && *httpAddr != "none") ||
		(*inboxDir != "" && *inboxDir != "none")

	doneFile := make(chan struct{})
	if *inputFile != "" {
		go func() {
			defer close(doneFile)
			fmt.Fprintf(os.Stderr, " Ingesting one-shot file: %s...\n", *inputFile)
			if err := ingest.IngestFile(ctx, *inputFile, mgr.Inbound()); err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: Failed to ingest file %s: %v\n", *inputFile, err)
			} else {
				fmt.Fprintf(os.Stderr, " Finished ingesting file: %s\n", *inputFile)
			}
		}()
	}

	// 6. Check if stdin is requested or is a pipe
	stat, _ := os.Stdin.Stat()
	isPipe := (stat.Mode() & os.ModeCharDevice) == 0

	doneReading := make(chan struct{})
	if *readStdin || isPipe {
		go func() {
			defer close(doneReading)
			scanner := bufio.NewScanner(os.Stdin)
			buf := make([]byte, 64*1024)
			scanner.Buffer(buf, 1024*1024)

			for scanner.Scan() {
				line := scanner.Bytes()
				if len(line) == 0 {
					continue
				}

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
	}

	fmt.Fprintf(os.Stderr, "========================================================\n")
	fmt.Fprintf(os.Stderr, " Pipeline running with %d workers. Awaiting events...\n", *workers)

	// Graceful shutdown handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	if !hasListeners && *inputFile != "" {
		select {
		case <-sigChan:
			fmt.Fprintf(os.Stderr, "\nReceived shutdown signal. Stopping pipeline...\n")
		case <-doneFile:
			// Allow ConnectIngest to transfer records from mgr.Channel into pipeline
			time.Sleep(50 * time.Millisecond)
			for i := 0; i < 100; i++ {
				if p.Stats().Pending == 0 {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			fmt.Fprintf(os.Stderr, "Finished processing file. Stopping pipeline...\n")
		}
	} else if isPipe || *readStdin {
		select {
		case <-sigChan:
			fmt.Fprintf(os.Stderr, "\nReceived shutdown signal. Stopping pipeline...\n")
		case <-doneReading:
			fmt.Fprintf(os.Stderr, "Finished reading from stdin. Draining pipeline...\n")
		}
	} else {
		<-sigChan
		fmt.Fprintf(os.Stderr, "\nReceived shutdown signal. Stopping pipeline...\n")
	}

	// Graceful shutdown sequence
	cancel()
	_ = mgr.Stop()
	p.Stop()
	_ = compositeSink.Flush()
	_ = compositeSink.Close()

	stats := p.Stats()
	valid, reason := stats.VerifyInvariant()

	fmt.Fprintf(os.Stderr, "\n--- Final Pipeline Loss Accounting ---\n")
	fmt.Fprintf(os.Stderr, "Accepted:    %d\n", stats.Accepted)
	fmt.Fprintf(os.Stderr, "Normalized:  %d\n", stats.Normalized)
	fmt.Fprintf(os.Stderr, "Quarantined: %d\n", stats.Quarantined)
	fmt.Fprintf(os.Stderr, "Pending:     %d\n", stats.Pending)
	fmt.Fprintf(os.Stderr, "Delivered:   %d\n", stats.Delivered)
	fmt.Fprintf(os.Stderr, "Loss Audit:  %s\n", reason)
	fmt.Fprintf(os.Stderr, "--------------------------------------\n")

	if !valid {
		os.Exit(2)
	}
}
