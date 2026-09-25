package integration_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"worm/internal/ingest"
	"worm/internal/model"
	"worm/internal/output"
	"worm/internal/packs"
	"worm/internal/pipeline"
	"worm/internal/rawstore"
)

func findProjectRoot(t *testing.T) string {
	t.Helper()
	candidates := []string{
		"../../",
		"./",
		"../",
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, "packs")); err == nil {
			return c
		}
	}
	t.Fatalf("could not locate project root with packs/ directory")
	return ""
}

func TestEndToEndPipeline_AllTransportsAndFormats(t *testing.T) {
	root := findProjectRoot(t)

	tempDir, err := os.MkdirTemp("", "worm_e2e_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "worm.db")
	inboxDir := filepath.Join(tempDir, "inbox")
	outputPath := filepath.Join(tempDir, "output.ndjson")

	// 1. Raw Store
	store, err := rawstore.New(dbPath)
	if err != nil {
		t.Fatalf("failed to init raw store: %v", err)
	}
	defer store.Close()

	// 2. Load all 7 Parser Packs
	snap, err := packs.LoadDir(filepath.Join(root, "packs"))
	if err != nil {
		t.Fatalf("failed to load parser packs: %v", err)
	}
	packManager := packs.NewSnapshotManager(snap)

	// 3. Output Sink
	sink, err := output.NewNDJSONSink(outputPath)
	if err != nil {
		t.Fatalf("failed to init output sink: %v", err)
	}
	defer sink.Close()

	// 4. Pipeline
	p := pipeline.New(pipeline.Config{
		Workers:     4,
		BufferSize:  1000,
		PackManager: packManager,
	}, store, sink)
	p.Start()

	// 5. Ingest Manager & Adapters
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	udpListener := ingest.NewSyslogUDPListener("127.0.0.1:0")
	tcpListener := ingest.NewSyslogTCPListener("127.0.0.1:0")
	httpListener := ingest.NewHTTPListener("127.0.0.1:0", "")
	fileWatcher := ingest.NewFileWatcher(inboxDir, 50*time.Millisecond)

	mgr := ingest.NewManager(1000)
	mgr.Register(udpListener)
	mgr.Register(tcpListener)
	mgr.Register(httpListener)
	mgr.Register(fileWatcher)

	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("failed to start ingest manager: %v", err)
	}

	p.ConnectIngest(ctx, mgr.Channel())

	// Wait for network listeners to bind
	time.Sleep(100 * time.Millisecond)

	udpAddr := udpListener.Addr()
	tcpAddr := tcpListener.Addr()
	httpAddr := httpListener.Addr()

	if udpAddr == nil || tcpAddr == nil || httpAddr == nil {
		t.Fatalf("one or more listeners failed to bind network address")
	}

	// === Transmit events across all modalities ===

	// 1. Syslog UDP: Palo Alto Firewall (Valid)
	udpConn, err := net.Dial("udp", udpAddr.String())
	if err != nil {
		t.Fatalf("dial udp failed: %v", err)
	}
	defer udpConn.Close()

	paMsg := []byte("<14>1 2026-09-20T10:05:23+05:30 pa-fw-01 paloalto - TRAFFIC - vendor_product=PAN-OS action=allow src=10.0.1.50 dst=203.0.113.25 sport=49312 dport=443 proto=tcp bytes_sent=2340 bytes_recv=18720 rule=allow-outbound-https session_id=8827364 app=ssl")
	_, _ = udpConn.Write(paMsg)

	// 2. Syslog UDP: Cisco ASA (Valid)
	asaMsg := []byte("<166>Sep 20 10:10:15 asa-gw-01 %ASA-6-302013: Built inbound TCP connection 2914823 for outside:198.51.100.50/52431 (198.51.100.50/52431) to inside:10.0.1.100/443 (10.0.1.100/443)")
	_, _ = udpConn.Write(asaMsg)

	// 3. Syslog UDP: Suricata CEF (Valid)
	cefMsg := []byte("CEF:0|Suricata|IDPS|6.0.0|2001219|ET SCAN Potential SSH Scan|6|src=45.33.32.156 dst=10.0.1.5 spt=44100 dpt=22 proto=TCP cnt=15 act=Alert msg=Potential SSH brute force detected cs1=ET/Open cs1Label=RuleCategory")
	_, _ = udpConn.Write(cefMsg)

	// 4. Syslog UDP: Malformed header (Should Quarantine)
	badSyslog := []byte("<>1 2026-09-20T BAD_TIMESTAMP pa-fw-01")
	_, _ = udpConn.Write(badSyslog)

	// 5. Syslog TCP: Linux SSHD auth (Valid)
	tcpConn, err := net.Dial("tcp", tcpAddr.String())
	if err != nil {
		t.Fatalf("dial tcp failed: %v", err)
	}
	defer tcpConn.Close()

	sshdMsg := "<86>Sep 20 10:20:01 srv-prod-01 sshd[14523]: Accepted publickey for deploy from 10.0.1.100 port 52431 ssh2: RSA SHA256:nThbg6kXUpJWGl7E1IGOCspRomTxdCARLviKw6E5SY8"
	_, _ = fmt.Fprintf(tcpConn, "%s\n", sshdMsg)

	// 6. HTTP POST: Payment Gateway JSON (Valid)
	httpURL := fmt.Sprintf("http://%s/api/v1/ingest", httpAddr.String())
	validJSON := `{"timestamp":"2026-09-20T10:30:05+05:30","level":"INFO","service":"payment-gateway","trace_id":"abc-125-def","user_id":"user_4823","action":"payment_process","amount":2500.00,"currency":"INR","status":"success","error_code":null,"message":"Payment processed successfully","client_ip":"10.0.1.52","server":"pay-srv-01","response_time_ms":450}`
	resp, err := http.Post(httpURL, "application/json", bytes.NewReader([]byte(validJSON)))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP post failed: %v, status: %v", err, resp.StatusCode)
	}
	resp.Body.Close()

	// 7. HTTP POST: Malformed JSON (Should Quarantine)
	badJSON := `{"timestamp":"2026-09-20T10:30:05+05:30","level":"INFO",broken_json`
	resp, err = http.Post(httpURL, "application/json", bytes.NewReader([]byte(badJSON)))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP post malformed failed: %v", err)
	}
	resp.Body.Close()

	// 8. File Spool: Nginx CLF (Valid line + Malformed line)
	clfContent := "10.0.1.50 - john [20/Sep/2026:10:15:01 +0530] \"GET /dashboard HTTP/1.1\" 200 15234 \"https://internal.example.com/\" \"Mozilla/5.0\"\nTHIS IS NOT A VALID LOG LINE\n"
	_ = os.WriteFile(filepath.Join(inboxDir, "webaccess.log"), []byte(clfContent), 0644)

	// 9. File Spool: PostgreSQL CSV (Valid 2 data rows)
	csvContent := "timestamp,source_type,client_ip,server_ip,port,user_id,action,statement\n2026-09-20T10:00:00Z,postgres_audit,10.0.1.50,10.0.1.5,5432,pg_admin,query,SELECT * FROM users\n2026-09-20T10:00:01Z,postgres_audit,10.0.1.51,10.0.1.5,5432,app_service,update,UPDATE users SET active = true\n"
	_ = os.WriteFile(filepath.Join(inboxDir, "db_audit.csv"), []byte(csvContent), 0644)

	// Wait for pipeline processing and file spool pickup
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		stats := p.Stats()
		// We expect:
		// 1 (PA) + 1 (ASA) + 1 (CEF) + 1 (BadSyslog) + 1 (SSHD) + 1 (ValidJSON) + 1 (BadJSON)
		// + 2 (Web CLF: 1 valid, 1 garbage) + 2 (CSV rows expanded from 1 table) = 11 total events!
		if stats.Accepted >= 11 && stats.Pending == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Graceful shutdown
	_ = mgr.Stop()
	p.Stop()
	_ = sink.Flush()

	// 6. Loss Accounting Audit
	stats := p.Stats()
	valid, reason := stats.VerifyInvariant()
	if !valid {
		t.Fatalf("Loss accounting invariant failed: %s (accepted=%d, normalized=%d, quarantined=%d, pending=%d)",
			reason, stats.Accepted, stats.Normalized, stats.Quarantined, stats.Pending)
	}

	t.Logf("Loss Accounting Audit: %s (Accepted: %d, Normalized: %d, Quarantined: %d, Pending: %d, Delivered: %d)",
		reason, stats.Accepted, stats.Normalized, stats.Quarantined, stats.Pending, stats.Delivered)

	if stats.Accepted < 11 {
		t.Errorf("expected at least 11 accepted events across all adapters, got %d", stats.Accepted)
	}
	if stats.Normalized < 7 {
		t.Errorf("expected at least 7 normalized events across all sources, got %d", stats.Normalized)
	}
	if stats.Quarantined < 3 {
		t.Errorf("expected at least 3 quarantined events (bad syslog, bad json, bad clf), got %d", stats.Quarantined)
	}
	if stats.Pending != 0 {
		t.Errorf("expected 0 pending events after drain, got %d", stats.Pending)
	}
}

type failingOutputSink struct{}

func (f *failingOutputSink) Emit(ctx context.Context, event *model.NormalizedEvent) error {
	return errors.New("simulated remote sink network down")
}
func (f *failingOutputSink) Flush() error { return nil }
func (f *failingOutputSink) Close() error { return nil }

func TestIntegration_SinkFailureAndOctetFraming_BA027(t *testing.T) {
	root := findProjectRoot(t)

	tempDir, err := os.MkdirTemp("", "worm_failure_e2e_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "worm.db")
	store, err := rawstore.New(dbPath)
	if err != nil {
		t.Fatalf("failed to init raw store: %v", err)
	}
	defer store.Close()

	snap, err := packs.LoadDir(filepath.Join(root, "packs"))
	if err != nil {
		t.Fatalf("failed to load packs: %v", err)
	}
	packMgr := packs.NewSnapshotManager(snap)

	// Injected failing sink
	failSink := &failingOutputSink{}

	p := pipeline.New(pipeline.Config{
		Workers:     2,
		BufferSize:  100,
		PackManager: packMgr,
	}, store, failSink)
	p.Start()

	// Ingest via Syslog TCP with RFC 6587 octet counting
	tcpAddr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("resolve tcp addr failed: %v", err)
	}
	tcpListener := ingest.NewSyslogTCPListener(tcpAddr.String())
	mgr := ingest.NewManager(100)
	mgr.Register(tcpListener)

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("failed to start ingest manager: %v", err)
	}
	p.ConnectIngest(ctx, mgr.Channel())

	// Wait for listener to bind
	time.Sleep(50 * time.Millisecond)

	boundAddr := tcpListener.Addr()
	tcpConn, err := net.Dial("tcp", boundAddr.String())
	if err != nil {
		t.Fatalf("dial tcp failed: %v", err)
	}
	defer tcpConn.Close()

	// Octet-counted message (RFC 6587: "<len> <msg>")
	msg := "<86>Sep 20 10:20:01 srv-prod-01 sshd[14523]: Accepted publickey for deploy from 10.0.1.100 port 52431 ssh2: RSA SHA256:nThbg6kXUpJWGl7E1IGOCspRomTxdCARLviKw6E5SY8"
	octetFramed := fmt.Sprintf("%d %s", len(msg), msg)
	_, _ = tcpConn.Write([]byte(octetFramed))

	// Wait for processing
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		stats := p.Stats()
		if stats.Accepted >= 1 && stats.Pending == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	_ = mgr.Stop()
	p.Stop()

	// Invariant check
	stats := p.Stats()
	valid, reason := stats.VerifyInvariant()
	if !valid {
		t.Fatalf("Loss accounting invariant failed: %s", reason)
	}
	if stats.Quarantined != 1 {
		t.Errorf("expected event to be quarantined due to sink error, got quarantined=%d", stats.Quarantined)
	}

	// Verify quarantine record has reason "sink_error" and is ReplayEligible
	quarList, err := store.GetQuarantined(ctx, 10, 0)
	if err != nil || len(quarList) == 0 {
		t.Fatalf("failed to retrieve quarantine: %v", err)
	}
	if quarList[0].Reason != "sink_error" {
		t.Errorf("expected reason 'sink_error', got %s", quarList[0].Reason)
	}
	if !quarList[0].ReplayEligible {
		t.Errorf("expected ReplayEligible to be true for sink error")
	}
}

type switchSink struct {
	mu        sync.Mutex
	fail      bool
	delivered int
}

func (s *switchSink) Emit(context.Context, *model.NormalizedEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return fmt.Errorf("simulated outage")
	}
	s.delivered++
	return nil
}
func (*switchSink) Flush() error { return nil }
func (*switchSink) Close() error { return nil }
func TestDurableOutboxRestartAndDrain(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "restart.db")
	store, err := rawstore.New(db)
	if err != nil {
		t.Fatal(err)
	}
	failing := &switchSink{fail: true}
	dyn := output.NewDynamicSink()
	if err := dyn.Replace(map[string]output.OutputSink{"siem": failing}); err != nil {
		t.Fatal(err)
	}
	p := pipeline.New(pipeline.Config{Workers: 1, BufferSize: 10}, store, dyn)
	p.Start()
	if err := p.SubmitSync(context.Background(), model.IngestedRecord{Transport: "test", SourceIP: "local", RawBytes: []byte(`{"event":"x"}`), ReceivedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	st := p.Stats()
	if st.Normalized != 1 || st.DeliveryPending != 1 {
		q, _ := store.GetQuarantined(context.Background(), 10, 0)
		t.Fatalf("before restart: %+v quarantine=%+v destinations=%v", st, q, dyn.Names())
	}
	p.Stop()
	_ = store.Close()
	store, err = rawstore.New(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	healthy := &switchSink{}
	dyn = output.NewDynamicSink()
	_ = dyn.Replace(map[string]output.OutputSink{"siem": healthy})
	p = pipeline.New(pipeline.Config{Workers: 1, BufferSize: 10}, store, dyn)
	p.Start()
	defer p.Stop()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st = p.Stats()
		if st.DeliveryPending == 0 && st.Delivered == 1 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	st = p.Stats()
	if st.DeliveryPending != 0 || st.Delivered != 1 {
		t.Fatalf("after restart: %+v", st)
	}
}

func TestAcceptedRawIsRecoveredAfterRestart(t *testing.T) {
	db := filepath.Join(t.TempDir(), "accepted-restart.db")
	store, err := rawstore.New(db)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := store.Store(context.Background(), model.IngestedRecord{Transport: "http-post", SourceIP: "127.0.0.1", RawBytes: []byte(`{"event":"committed-before-crash"}`), ReceivedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = rawstore.New(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sink := pipeline.NewMemorySink()
	p := pipeline.New(pipeline.Config{Workers: 1, BufferSize: 4}, store, sink)
	p.Start()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if st := p.Stats(); st.Normalized == 1 && st.Pending == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	p.Stop()
	st := p.Stats()
	if st.Normalized != 1 || st.Pending != 0 || len(sink.Events()) != 1 {
		t.Fatalf("accepted raw was not recovered: raw=%s stats=%+v emitted=%d", raw.RawID, st, len(sink.Events()))
	}
	recovered, err := store.Retrieve(context.Background(), raw.RawID)
	if err != nil || recovered.Status != model.StatusNormalized {
		t.Fatalf("unexpected recovered parent: %+v err=%v", recovered, err)
	}
}
