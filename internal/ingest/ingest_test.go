package ingest_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"worm/internal/ingest"
	"worm/internal/model"
)

func TestSyslogUDPListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Port :0 binds to any free ephemeral port
	listener := ingest.NewSyslogUDPListener("127.0.0.1:0")
	out := make(chan model.IngestedRecord, 10)

	errCh := make(chan error, 1)
	go func() {
		errCh <- listener.Start(ctx, out)
	}()

	// Wait for listener to bind
	var addr net.Addr
	for i := 0; i < 20; i++ {
		addr = listener.Addr()
		if addr != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if addr == nil {
		t.Fatalf("UDP listener failed to bind in time")
	}

	// Send UDP packet
	conn, err := net.Dial("udp", addr.String())
	if err != nil {
		t.Fatalf("failed to dial UDP listener: %v", err)
	}
	defer conn.Close()

	msg := []byte("<14>1 2026-09-20T10:00:00Z fw-01 paloalto - TRAFFIC - action=allow")
	_, err = conn.Write(msg)
	if err != nil {
		t.Fatalf("failed to write UDP message: %v", err)
	}

	select {
	case rec := <-out:
		if rec.Transport != "syslog-udp" {
			t.Errorf("expected transport syslog-udp, got %s", rec.Transport)
		}
		if !bytes.Equal(rec.RawBytes, msg) {
			t.Errorf("payload mismatch: got %q, want %q", string(rec.RawBytes), string(msg))
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for UDP message")
	}

	_ = listener.Stop()
	cancel()
}

func TestSyslogTCPListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listener := ingest.NewSyslogTCPListener("127.0.0.1:0")
	out := make(chan model.IngestedRecord, 10)

	go func() {
		_ = listener.Start(ctx, out)
	}()

	var addr net.Addr
	for i := 0; i < 20; i++ {
		addr = listener.Addr()
		if addr != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if addr == nil {
		t.Fatalf("TCP listener failed to bind in time")
	}

	conn, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatalf("failed to dial TCP: %v", err)
	}
	defer conn.Close()

	line1 := "<86>Sep 20 10:20:01 srv-01 sshd[123]: Accepted publickey for deploy"
	line2 := "<86>Sep 20 10:20:05 srv-01 sshd[124]: Failed password for root"
	_, _ = fmt.Fprintf(conn, "%s\n%s\n", line1, line2)

	for _, expected := range []string{line1, line2} {
		select {
		case rec := <-out:
			if rec.Transport != "syslog-tcp" {
				t.Errorf("expected transport syslog-tcp, got %s", rec.Transport)
			}
			if string(rec.RawBytes) != expected {
				t.Errorf("payload mismatch: got %q, want %q", string(rec.RawBytes), expected)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for TCP message: %s", expected)
		}
	}

	_ = listener.Stop()
	cancel()
}

func TestHTTPListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listener := ingest.NewHTTPListener("127.0.0.1:0", "secret-token")
	out := make(chan model.IngestedRecord, 10)

	go func() {
		_ = listener.Start(ctx, out)
	}()

	var addr net.Addr
	for i := 0; i < 20; i++ {
		addr = listener.Addr()
		if addr != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if addr == nil {
		t.Fatalf("HTTP listener failed to bind in time")
	}

	targetURL := fmt.Sprintf("http://%s/api/v1/ingest", addr.String())

	// 1. Unauthorized request
	req, _ := http.NewRequest(http.MethodPost, targetURL, bytes.NewReader([]byte(`{"test":1}`)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 2. Authorized single JSON request
	req, _ = http.NewRequest(http.MethodPost, targetURL, bytes.NewReader([]byte(`{"service":"payment-gateway","amount":100}`)))
	req.Header.Set("X-WORM-Key", "secret-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	select {
	case rec := <-out:
		if rec.Transport != "http-post" {
			t.Errorf("expected transport http-post, got %s", rec.Transport)
		}
		if !bytes.Contains(rec.RawBytes, []byte("payment-gateway")) {
			t.Errorf("payload missing payment-gateway: %s", string(rec.RawBytes))
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for HTTP record")
	}

	_ = listener.Stop()
	cancel()
}

func TestFileWatcherAndIngestFile(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "worm_spool_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	inboxDir := filepath.Join(tempDir, "inbox")
	_ = os.MkdirAll(inboxDir, 0755)

	// Create test files
	logFile := filepath.Join(inboxDir, "access.log")
	logContent := "10.0.1.50 - john [20/Sep/2026:10:00:00 +0000] \"GET / HTTP/1.1\" 200 1234\n10.0.1.51 - - [20/Sep/2026:10:00:01 +0000] \"GET /admin HTTP/1.1\" 403 500\n"
	if err := os.WriteFile(logFile, []byte(logContent), 0644); err != nil {
		t.Fatalf("failed to write test log file: %v", err)
	}

	csvFile := filepath.Join(inboxDir, "audit.csv")
	csvContent := "timestamp,source_ip,destination_ip,action\n2026-09-20T10:00:00Z,10.0.1.50,10.0.1.1,query\n2026-09-20T10:00:01Z,10.0.1.51,10.0.1.1,update\n"
	if err := os.WriteFile(csvFile, []byte(csvContent), 0644); err != nil {
		t.Fatalf("failed to write test csv file: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcher := ingest.NewFileWatcher(inboxDir, 50*time.Millisecond)
	out := make(chan model.IngestedRecord, 10)

	go func() {
		_ = watcher.Start(ctx, out)
	}()

	// We expect 2 log lines from access.log and 1 full CSV document from audit.csv
	received := 0
	deadline := time.After(3 * time.Second)

	for received < 3 {
		select {
		case rec := <-out:
			if rec.Transport != "file" {
				t.Errorf("expected transport file, got %s", rec.Transport)
			}
			if rec.Ack != nil {
				rec.Ack <- nil
			}
			received++
		case <-deadline:
			t.Fatalf("timed out waiting for spooled records (received %d/3)", received)
		}
	}

	// Verify files were moved to processed/
	processedDir := filepath.Join(inboxDir, "processed")
	var entries []os.DirEntry
	for i := 0; i < 20; i++ {
		entries, _ = os.ReadDir(processedDir)
		if len(entries) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(entries) < 2 {
		t.Errorf("expected at least 2 processed files, got %d", len(entries))
	}

	_ = watcher.Stop()
	cancel()
}

func TestManagerLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := ingest.NewManager(100)
	udp := ingest.NewSyslogUDPListener("127.0.0.1:0")
	tcp := ingest.NewSyslogTCPListener("127.0.0.1:0")

	mgr.Register(udp)
	mgr.Register(tcp)

	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("manager start failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	if err := mgr.Stop(); err != nil {
		t.Fatalf("manager stop failed: %v", err)
	}

	// Output channel should be closed
	_, ok := <-mgr.Channel()
	if ok {
		t.Errorf("expected closed channel after manager Stop")
	}
}

func TestSyslogTCP_RFC6587_OctetCounting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listener := ingest.NewSyslogTCPListener("127.0.0.1:0")
	out := make(chan model.IngestedRecord, 10)

	go func() {
		_ = listener.Start(ctx, out)
	}()

	var addr net.Addr
	for i := 0; i < 20; i++ {
		addr = listener.Addr()
		if addr != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if addr == nil {
		t.Fatalf("listener address empty")
	}

	conn, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	defer conn.Close()

	// RFC 6587 octet counting frame: "42 <14>1 2026-09-24T12:00:00Z host app - - msg"
	msg := "<14>1 2026-09-24T12:00:00Z host app - - msg"
	framed := fmt.Sprintf("%d %s", len(msg), msg)

	if _, err := conn.Write([]byte(framed)); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	select {
	case rec := <-out:
		if string(rec.RawBytes) != msg {
			t.Errorf("expected exact payload %q, got %q", msg, string(rec.RawBytes))
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for octet-counted syslog record")
	}
	_ = listener.Stop()
}

func TestFileCommitFailureMovesToFailed(t *testing.T) {
	dir := t.TempDir()
	inboxDir := filepath.Join(dir, "inbox")
	_ = os.MkdirAll(inboxDir, 0755)

	logFile := filepath.Join(inboxDir, "failing.log")
	if err := os.WriteFile(logFile, []byte("single line log\n"), 0644); err != nil {
		t.Fatalf("write file failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcher := ingest.NewFileWatcher(inboxDir, 20*time.Millisecond)
	out := make(chan model.IngestedRecord, 10)

	go func() {
		_ = watcher.Start(ctx, out)
	}()

	// Read record and return error on Ack (simulating raw commit failure)
	select {
	case rec := <-out:
		if rec.Ack != nil {
			rec.Ack <- fmt.Errorf("sqlite disk I/O error")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for record")
	}

	// Wait for file watcher to move to failed/
	failedDir := filepath.Join(inboxDir, "failed")
	var entries []os.DirEntry
	for i := 0; i < 20; i++ {
		entries, _ = os.ReadDir(failedDir)
		if len(entries) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if len(entries) != 1 {
		t.Errorf("expected file to move to failed/ directory after commit error, got %d files", len(entries))
	}
	_ = watcher.Stop()
}
