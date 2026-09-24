package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"worm/internal/api"
	"worm/internal/cli"
	"worm/internal/connections"
	"worm/internal/ingest"
	"worm/internal/model"
	"worm/internal/output"
	"worm/internal/packs"
	"worm/internal/pipeline"
	"worm/internal/rawstore"
	"worm/web"
)

func main() {
	// 0. Ergonomic CLI dispatch (e.g. ./bin/worm -packs, ./bin/worm -replay all, ./bin/worm -stop, etc.)
	if len(os.Args) > 1 {
		cmd := os.Args[1]
		switch cmd {
		case "stop", "-stop", "--stop":
			cli.StopDaemon(cli.DefaultPIDFile)
			return
		case "status", "-status", "--status":
			cli.StatusDaemon(cli.DefaultPIDFile, ":9090")
			return
		case "sources", "-sources", "--sources":
			srcDir := "sources"
			for i, a := range os.Args {
				if (a == "-sources-dir" || a == "--sources-dir") && i+1 < len(os.Args) {
					srcDir = os.Args[i+1]
				}
			}
			if len(os.Args) >= 3 && !strings.HasPrefix(os.Args[2], "-") {
				sub := os.Args[2]
				switch sub {
				case "validate":
					for i, a := range os.Args {
						if (a == "-f" || a == "--f") && i+1 < len(os.Args) {
							cli.ValidateSourceCLI(os.Args[i+1])
							return
						}
					}
					fmt.Fprintln(os.Stderr, "Usage: worm sources validate -f <file.yaml>")
					return
				case "test":
					for i, a := range os.Args {
						if (a == "-f" || a == "--f") && i+1 < len(os.Args) {
							cli.TestSourceCLI(os.Args[i+1])
							return
						}
					}
					fmt.Fprintln(os.Stderr, "Usage: worm sources test -f <file.yaml>")
					return
				case "apply":
					for i, a := range os.Args {
						if (a == "-f" || a == "--f") && i+1 < len(os.Args) {
							cli.ApplySourceCLI(os.Args[i+1], ":9090", srcDir)
							return
						}
					}
					fmt.Fprintln(os.Stderr, "Usage: worm sources apply -f <file.yaml>")
					return
				case "rollback":
					cli.RollbackSourceCLI(":9090", srcDir)
					return
				case "list":
					cli.ListSourcesCLI(":9090", srcDir)
					return
				case "get":
					if len(os.Args) >= 4 {
						cli.GetSourceCLI(os.Args[3], ":9090", srcDir)
						return
					}
					fmt.Fprintln(os.Stderr, "Usage: worm sources get <name>")
					return
				}
			}
			cli.ListSourcesCLI(":9090", srcDir)
			return
		case "sinks", "-sinks", "--sinks":
			sinkDir := "sinks"
			for i, a := range os.Args {
				if (a == "-sinks-dir" || a == "--sinks-dir") && i+1 < len(os.Args) {
					sinkDir = os.Args[i+1]
				}
			}
			if len(os.Args) >= 3 && !strings.HasPrefix(os.Args[2], "-") {
				sub := os.Args[2]
				switch sub {
				case "validate":
					for i, a := range os.Args {
						if (a == "-f" || a == "--f") && i+1 < len(os.Args) {
							cli.ValidateSinkCLI(os.Args[i+1])
							return
						}
					}
					fmt.Fprintln(os.Stderr, "Usage: worm sinks validate -f <file.yaml>")
					return
				case "test":
					for i, a := range os.Args {
						if (a == "-f" || a == "--f") && i+1 < len(os.Args) {
							cli.TestSinkCLI(os.Args[i+1])
							return
						}
					}
					fmt.Fprintln(os.Stderr, "Usage: worm sinks test -f <file.yaml>")
					return
				case "apply":
					for i, a := range os.Args {
						if (a == "-f" || a == "--f") && i+1 < len(os.Args) {
							cli.ApplySinkCLI(os.Args[i+1], ":9090", sinkDir)
							return
						}
					}
					fmt.Fprintln(os.Stderr, "Usage: worm sinks apply -f <file.yaml>")
					return
				case "rollback":
					cli.RollbackSinkCLI(":9090", sinkDir)
					return
				case "list":
					cli.ListSinksCLI(":9090", sinkDir)
					return
				case "get":
					if len(os.Args) >= 4 {
						cli.GetSinkCLI(os.Args[3], ":9090", sinkDir)
						return
					}
					fmt.Fprintln(os.Stderr, "Usage: worm sinks get <name>")
					return
				}
			}
			cli.ListSinksCLI(":9090", sinkDir)
			return

		case "packs", "-packs", "--packs":
			pDir := "packs"
			for i, a := range os.Args {
				if (a == "-packs-dir" || a == "--packs-dir") && i+1 < len(os.Args) {
					pDir = os.Args[i+1]
				}
			}
			if len(os.Args) >= 3 && !strings.HasPrefix(os.Args[2], "-") {
				sub := os.Args[2]
				switch sub {
				case "validate":
					for i, a := range os.Args {
						if (a == "-f" || a == "--f") && i+1 < len(os.Args) {
							cli.ValidatePackCLI(os.Args[i+1])
							return
						}
					}
					fmt.Fprintln(os.Stderr, "Usage: worm packs validate -f <file.yaml>")
					return
				case "test":
					var file, sample string
					for i, a := range os.Args {
						if (a == "-f" || a == "--f") && i+1 < len(os.Args) {
							file = os.Args[i+1]
						}
						if (a == "-sample" || a == "--sample") && i+1 < len(os.Args) {
							sample = os.Args[i+1]
						}
					}
					if file != "" {
						cli.TestPackCLI(file, sample)
						return
					}
					fmt.Fprintln(os.Stderr, "Usage: worm packs test -f <file.yaml> [-sample <sample_log_or_file>]")
					return
				case "apply":
					for i, a := range os.Args {
						if (a == "-f" || a == "--f") && i+1 < len(os.Args) {
							cli.ApplyPackCLI(os.Args[i+1], ":9090", pDir)
							return
						}
					}
					fmt.Fprintln(os.Stderr, "Usage: worm packs apply -f <file.yaml>")
					return
				case "rollback":
					cli.RollbackPackCLI(":9090", pDir)
					return
				case "list":
					cli.ListPacksCLI(":9090", pDir)
					return
				case "get":
					if len(os.Args) >= 4 {
						cli.GetPackCLI(os.Args[3], ":9090", pDir)
						return
					}
					fmt.Fprintln(os.Stderr, "Usage: worm packs get <name>")
					return
				}
			}
			for i, a := range os.Args {
				if (a == "-f" || a == "--f") && i+1 < len(os.Args) {
					cli.ApplyPackCLI(os.Args[i+1], ":9090", pDir)
					return
				}
			}
			cli.ListPacksCLI(":9090", pDir)
			return
		case "replay", "-replay", "--replay":
			if len(os.Args) >= 3 {
				target := os.Args[2]
				if target == "all" || target == "--all" {
					cli.ReplayAllCLI(":9090")
					return
				}
				cli.ReplaySingleCLI(target, ":9090")
				return
			}
			cli.ListQuarantineCLI(":9090", "data/worm.db")
			return
		case "replay-all", "-replay-all", "--replay-all":
			cli.ReplayAllCLI(":9090")
			return
		}
	}

	dbPath := flag.String("db", "data/worm.db", "Path to SQLite raw store database")
	packsDir := flag.String("packs-dir", "packs", "Path to YAML parser packs directory")
	sourcesDir := flag.String("sources-dir", "sources", "Path to YAML Ingestion Source resources directory")
	sinksDir := flag.String("sinks-dir", "sinks", "Path to YAML Delivery Sink resources directory")
	workers := flag.Int("workers", 4, "Number of concurrent pipeline workers")
	readStdin := flag.Bool("stdin", false, "Read logs from stdin line-by-line")
	verifyRawID := flag.String("verify", "", "Cryptographically verify a raw record by raw_id")
	verifyEventID := flag.String("verify-event", "", "Cryptographically verify a normalized event by event_id")

	// Detached daemon & CLI flags
	detached := flag.Bool("d", false, "Run WORM engine in detached/daemon mode in the background")
	stopFlag := flag.Bool("stop", false, "Stop running background WORM engine daemon")
	statusFlag := flag.Bool("status", false, "Check status of background WORM engine daemon")
	replayFlag := flag.String("replay", "", "Manage quarantine DLQ: list pending logs, or replay with <quarantine_id> or 'all'")
	replayAllFlag := flag.Bool("replay-all", false, "Replay all quarantined DLQ records")

	// Multi-transport ingestion flags
	syslogUDP := flag.String("syslog-udp", ":514", "UDP address for Syslog listener (:1514 for non-root, 'none' to disable)")
	syslogTCP := flag.String("syslog-tcp", ":514", "TCP address for Syslog listener (:1514 for non-root, 'none' to disable)")
	syslogTLS := flag.String("syslog-tls", ":6514", "TCP address for Syslog TLS (RFC 5425) listener (:7514 for non-root, 'none' to disable)")
	tlsCert := flag.String("tls-cert", "", "Path to X.509 certificate file for Syslog TLS")
	tlsKey := flag.String("tls-key", "", "Path to private key file for Syslog TLS")
	tlsClientCA := flag.String("tls-client-ca", "", "Path to optional client CA file for Syslog TLS mutual authentication (mTLS)")
	httpAddr := flag.String("http", ":8080", "HTTP address for REST ingestion ('none' to disable)")
	httpKey := flag.String("http-key", "", "Optional API key for HTTP POST authentication")
	inboxDir := flag.String("inbox", "data/inbox", "Path to monitored spool inbox directory ('none' to disable)")
	inputFile := flag.String("file", "", "One-shot batch file to ingest immediately")

	// Output sink flags
	outputFile := flag.String("output-file", "data/output/normalized.ndjson", "Path to write normalized NDJSON output ('none' to disable)")
	stdoutOutput := flag.Bool("stdout", true, "Emit normalized NDJSON events to stdout")

	// Management UI and Control Plane flag
	uiAddr := flag.String("ui", ":9090", "Address for Management Web UI and Control Plane API (:9090 default, 'none' to disable)")
	adminToken := flag.String("admin-token", "", "Optional secret token for authenticating control-plane mutation API routes")

	flag.Parse()

	// Handle standalone actions parsed via flags
	if *stopFlag {
		cli.StopDaemon(cli.DefaultPIDFile)
		return
	}
	if *statusFlag {
		cli.StatusDaemon(cli.DefaultPIDFile, *uiAddr)
		return
	}
	if *replayAllFlag {
		cli.ReplayAllCLI(*uiAddr)
		return
	}
	if *replayFlag != "" {
		if *replayFlag == "all" {
			cli.ReplayAllCLI(*uiAddr)
			return
		}
		if *replayFlag == "list" {
			cli.ListQuarantineCLI(*uiAddr, *dbPath)
			return
		}
		cli.ReplaySingleCLI(*replayFlag, *uiAddr)
		return
	}

	// Detached background mode
	if *detached {
		cli.RunDaemonMode(cli.DefaultPIDFile, cli.DefaultLogFile, *uiAddr)
		return
	}

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

	// Normalized event verification mode
	if *verifyEventID != "" {
		ctx := context.Background()
		report, err := store.VerifyNormalized(ctx, *verifyEventID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Event verification failed: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("\n--- Cryptographic Normalized Event Integrity Report ---\n")
		fmt.Printf("Event ID:     %s\n", report.EventID)
		fmt.Printf("Raw ID:       %s\n", report.RawID)
		fmt.Printf("Stored Hash:  %s\n", report.StoredHash)
		fmt.Printf("Actual Hash:  %s\n", report.ComputedHash)
		fmt.Printf("Message:      %s\n", report.StoredMessage)
		fmt.Printf("Raw Source:   ")
		if report.RawValid {
			fmt.Println("PASSED (Cryptographically Exact)")
		} else {
			fmt.Println("FAILED (RAW LOG TAMPERED)")
		}
		fmt.Printf("Integrity:    ")
		if report.Passed {
			fmt.Println("PASSED (Cryptographically Exact)")
		} else {
			fmt.Printf("FAILED (TAMPER DETECTED: %s)\n", report.FailureReason)
		}
		fmt.Printf("-------------------------------------------------------\n")
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

	if *sourcesDir != "" {
		if _, err := os.Stat(*sourcesDir); err == nil {
			srcMgr := connections.NewManager(*sourcesDir)
			_ = srcMgr.LoadDir(*sourcesDir)
			fmt.Fprintf(os.Stderr, " Loaded %d sources from %s\n", len(srcMgr.List()), *sourcesDir)
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
	if *syslogTLS != "" && *syslogTLS != "none" {
		var tlsCfg *tls.Config
		if *tlsCert != "" && *tlsKey != "" {
			cert, err := tls.LoadX509KeyPair(*tlsCert, *tlsKey)
			if err != nil {
				fmt.Fprintf(os.Stderr, "WARNING: Failed to load Syslog TLS cert/key: %v\n", err)
			} else {
				tlsCfg = &tls.Config{
					Certificates: []tls.Certificate{cert},
					MinVersion:   tls.VersionTLS12,
				}
				if *tlsClientCA != "" {
					caCert, err := os.ReadFile(*tlsClientCA)
					if err != nil {
						fmt.Fprintf(os.Stderr, "WARNING: Failed to read client CA: %v\n", err)
					} else {
						caPool := x509.NewCertPool()
						caPool.AppendCertsFromPEM(caCert)
						tlsCfg.ClientCAs = caPool
						tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
					}
				}
			}
		}
		mgr.Register(ingest.NewSyslogTLSListener(*syslogTLS, tlsCfg))
		fmt.Fprintf(os.Stderr, " Ingest adapter configured: Syslog TLS (RFC 5425) on %s\n", *syslogTLS)
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

	// 5. Initialize Management Control Plane & UI Server
	var uiServer *api.Server
	if *uiAddr != "" && *uiAddr != "none" {
		cfgInfo := api.ConfigInfo{
			UIAddress:  *uiAddr,
			SyslogUDP:  *syslogUDP,
			SyslogTCP:  *syslogTCP,
			SyslogTLS:  *syslogTLS,
			HTTPIngest: *httpAddr,
			InboxDir:   *inboxDir,
			DBPath:     *dbPath,
			Workers:    *workers,
			Version:    "1.0.4",
			AdminToken: *adminToken,
		}
		uiServer = api.NewServer(*uiAddr, store, p, packManager, *packsDir, cfgInfo, web.Dist())
		uiServer.SetIngestTracker(mgr)
		connMgr := connections.NewManager(*sinksDir)
		connMgr.SetResourceDirs(*sourcesDir, *sinksDir)
		if *sourcesDir != "" {
			_ = connMgr.LoadDir(*sourcesDir)
		}
		if *sinksDir != "" {
			_ = connMgr.LoadDir(*sinksDir)
		}
		uiServer.SetConnManager(connMgr, *sinksDir)
		if err := uiServer.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: Failed to start Management UI on %s: %v\n", *uiAddr, err)
		} else {
			fmt.Fprintf(os.Stderr, " Management Control Plane & UI active: http://localhost%s\n", *uiAddr)
		}
	}

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
	if uiServer != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = uiServer.Stop(shutdownCtx)
		shutdownCancel()
	}
	cancel()
	_ = mgr.Stop()
	p.Stop()
	_ = compositeSink.Flush()
	_ = compositeSink.Close()

	if os.Getenv("WORM_DAEMON") == "1" {
		_ = os.Remove(cli.DefaultPIDFile)
	}

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
