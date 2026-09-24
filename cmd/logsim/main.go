package main

import (
	"bytes"
	"flag"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// SourceSimulator generates scenario-driven logs for a specific source and format.
type SourceSimulator interface {
	Name() string
	Format() string
	Generate(scenario string, timestamp time.Time, rng *rand.Rand) []byte
}

// 1. FirewallSimulator: Palo Alto PAN-OS Syslog
type FirewallSimulator struct{}

func (s *FirewallSimulator) Name() string   { return "firewall" }
func (s *FirewallSimulator) Format() string { return "syslog" }
func (s *FirewallSimulator) Generate(scenario string, ts time.Time, rng *rand.Rand) []byte {
	timeStr := ts.Format("2006-01-02T15:04:05-07:00")
	switch scenario {
	case "bruteforce":
		port := 3389 + rng.Intn(5)
		return []byte(fmt.Sprintf("<12>1 %s pa-fw-01 paloalto - TRAFFIC - vendor_product=PAN-OS action=deny src=45.33.32.156 dst=10.0.1.10 sport=44891 dport=%d proto=tcp bytes_sent=0 bytes_recv=0 rule=block-rdp-inbound session_id=%d app=ms-rdp", timeStr, port, 8827400+rng.Intn(1000)))
	case "scan":
		return []byte(fmt.Sprintf("<12>1 %s pa-fw-01 paloalto - THREAT - vendor_product=PAN-OS action=drop src=203.0.113.99 dst=10.0.1.5 sport=55123 dport=80 proto=tcp threat_id=41000 threat_name=Web-Recon-Scan category=reconnaissance severity=high rule=block-scanners session_id=%d", timeStr, 8827500+rng.Intn(1000)))
	case "malformed":
		return []byte(fmt.Sprintf("<14>1 %s pa-fw-01 paloalto - TRAFFIC - vendor_product=PAN-OS action=allow src=10.0.1.50 dst=203.0.113.25 sport=493", timeStr[:10])) // truncated
	default: // normal
		clientIP := fmt.Sprintf("10.0.1.%d", 50+rng.Intn(6))
		return []byte(fmt.Sprintf("<14>1 %s pa-fw-01 paloalto - TRAFFIC - vendor_product=PAN-OS action=allow src=%s dst=203.0.113.25 sport=%d dport=443 proto=tcp bytes_sent=2340 bytes_recv=18720 rule=allow-outbound-https session_id=%d app=ssl", timeStr, clientIP, 49152+rng.Intn(1000), 8827000+rng.Intn(1000)))
	}
}

// 2. ASASimulator: Cisco ASA Syslog
type ASASimulator struct{}

func (s *ASASimulator) Name() string   { return "asa" }
func (s *ASASimulator) Format() string { return "syslog" }
func (s *ASASimulator) Generate(scenario string, ts time.Time, rng *rand.Rand) []byte {
	timeStr := ts.Format("Jan 02 15:04:05")
	switch scenario {
	case "bruteforce":
		return []byte(fmt.Sprintf("<164>%s asa-gw-01 %%ASA-4-106023: Deny tcp src outside:45.33.32.156/%d dst inside:10.0.1.100/22 by access-group \"outside_in\" [0x0, 0x0]", timeStr, 50000+rng.Intn(1000)))
	case "scan":
		return []byte(fmt.Sprintf("<163>%s asa-gw-01 %%ASA-3-710003: TCP access denied by ACL from 203.0.113.99/12345 to inside:10.0.1.5/3389 on interface outside", timeStr))
	case "malformed":
		return []byte(fmt.Sprintf("<166>%s asa-gw-01 %%ASA-INVALID-MESSAGE", timeStr))
	default: // normal
		clientIP := fmt.Sprintf("10.0.1.%d", 50+rng.Intn(6))
		return []byte(fmt.Sprintf("<166>%s asa-gw-01 %%ASA-6-302013: Built inbound TCP connection %d for outside:198.51.100.50/52431 (198.51.100.50/52431) to inside:%s/443 (%s/443)", timeStr, 2914800+rng.Intn(1000), clientIP, clientIP))
	}
}

// 3. SSHDSimulator: Linux Auth Syslog (RFC 3164)
type SSHDSimulator struct{}

func (s *SSHDSimulator) Name() string   { return "sshd" }
func (s *SSHDSimulator) Format() string { return "syslog" }
func (s *SSHDSimulator) Generate(scenario string, ts time.Time, rng *rand.Rand) []byte {
	timeStr := ts.Format("Jan 02 15:04:05")
	pid := 14500 + rng.Intn(500)
	switch scenario {
	case "bruteforce":
		port := 55000 + rng.Intn(100)
		return []byte(fmt.Sprintf("<86>%s srv-prod-01 sshd[%d]: Failed password for root from 45.33.32.156 port %d ssh2", timeStr, pid, port))
	case "scan":
		return []byte(fmt.Sprintf("<85>%s srv-prod-01 sshd[%d]: pam_unix(sshd:auth): authentication failure; logname= uid=0 euid=0 tty=ssh ruser= rhost=203.0.113.99 user=admin", timeStr, pid))
	case "malformed":
		return []byte(fmt.Sprintf("<86>%s srv-prod-01 sshd: Invalid SSH format line with missing brackets", timeStr))
	default: // normal
		user := "deploy"
		if rng.Float32() > 0.5 {
			user = "ashok"
		}
		return []byte(fmt.Sprintf("<86>%s srv-prod-01 sshd[%d]: Accepted publickey for %s from 10.0.1.100 port %d ssh2: RSA SHA256:nThbg6kXUpJWGl7E1IGOCspRomTxdCARLviKw6E5SY8", timeStr, pid, user, 52000+rng.Intn(1000)))
	}
}

// 4. IDSSimulator: Suricata/Snort CEF
type IDSSimulator struct{}

func (s *IDSSimulator) Name() string   { return "ids" }
func (s *IDSSimulator) Format() string { return "cef" }
func (s *IDSSimulator) Generate(scenario string, ts time.Time, rng *rand.Rand) []byte {
	switch scenario {
	case "bruteforce":
		return []byte("CEF:0|Suricata|IDPS|6.0.0|2001219|ET SCAN Potential SSH Scan|6|src=45.33.32.156 dst=10.0.1.5 spt=44100 dpt=22 proto=TCP cnt=15 act=Alert msg=Potential SSH brute force detected cs1=ET/Open cs1Label=RuleCategory")
	case "scan":
		return []byte("CEF:0|Suricata|IDPS|6.0.0|2100498|ET SCAN Potential Web Path Traversal|8|src=203.0.113.99 dst=10.0.1.100 spt=55123 dpt=80 proto=TCP act=Alert msg=Path traversal attempt detected cs1=ET/Open cs1Label=RuleCategory")
	case "malformed":
		return []byte("CEF:0|Suricata|IDPS|incomplete_header_with_missing_pipes")
	default: // normal
		return []byte(fmt.Sprintf("CEF:0|Suricata|IDPS|6.0.0|2013028|ET POLICY curl User-Agent Outbound|3|src=10.0.1.%d dst=198.51.100.10 spt=49200 dpt=443 proto=TCP act=Alert msg=Non-browser HTTP client detected cs1=ET/Open cs1Label=RuleCategory", 50+rng.Intn(6)))
	}
}

// 5. WebServerSimulator: Nginx Combined Log Format (Text)
type WebServerSimulator struct{}

func (s *WebServerSimulator) Name() string   { return "webserver" }
func (s *WebServerSimulator) Format() string { return "text" }
func (s *WebServerSimulator) Generate(scenario string, ts time.Time, rng *rand.Rand) []byte {
	timeStr := ts.Format("02/Jan/2006:15:04:05 -0700")
	switch scenario {
	case "bruteforce":
		return []byte(fmt.Sprintf("45.33.32.156 - - [%s] \"POST /login HTTP/1.1\" 401 245 \"https://app.example.com/login\" \"Mozilla/5.0 (Windows NT 10.0; Win64; x64)\"", timeStr))
	case "scan":
		paths := []string{"/admin/config", "/../../etc/passwd", "/wp-login.php", "/.env", "/phpmyadmin"}
		p := paths[rng.Intn(len(paths))]
		return []byte(fmt.Sprintf("203.0.113.99 - - [%s] \"GET %s HTTP/1.1\" 403 1024 \"-\" \"python-requests/2.28.0\"", timeStr, p))
	case "malformed":
		return []byte("THIS IS NOT A VALID LOG LINE AND CANNOT BE PARSED")
	default: // normal
		paths := []string{"/dashboard", "/api/v1/reports", "/index.html", "/app.js", "/style.css"}
		p := paths[rng.Intn(len(paths))]
		clientIP := fmt.Sprintf("10.0.1.%d", 50+rng.Intn(6))
		return []byte(fmt.Sprintf("%s - user_%d [%s] \"GET %s HTTP/1.1\" 200 %d \"https://internal.example.com/\" \"Mozilla/5.0\"", clientIP, 100+rng.Intn(50), timeStr, p, 1000+rng.Intn(8000)))
	}
}

// 6. AppSimulator: Payment Gateway JSON
type AppSimulator struct{}

func (s *AppSimulator) Name() string   { return "app" }
func (s *AppSimulator) Format() string { return "json" }
func (s *AppSimulator) Generate(scenario string, ts time.Time, rng *rand.Rand) []byte {
	timeStr := ts.Format("2006-01-02T15:04:05-07:00")
	userID := fmt.Sprintf("user_%04d", 1000+rng.Intn(9000))
	traceID := fmt.Sprintf("trace-%06x", rng.Intn(0xffffff))
	clientIP := fmt.Sprintf("10.0.1.%d", 50+rng.Intn(6))

	switch scenario {
	case "scan", "error":
		return []byte(fmt.Sprintf(`{"timestamp":"%s","level":"ERROR","service":"payment-gateway","trace_id":"%s","user_id":"%s","action":"payment_process","amount":%.2f,"currency":"INR","status":"failed","error_code":"PG_TIMEOUT","message":"Payment gateway timeout after 30s","client_ip":"%s","server":"pay-srv-02","response_time_ms":30000}`, timeStr, traceID, userID, 15000.0, clientIP))
	case "malformed":
		return []byte(fmt.Sprintf(`{"timestamp":"%s","level":"ERROR","service":"payment-gateway"`, timeStr)) // unclosed json
	default: // normal
		amount := 100.0 + float64(rng.Intn(5000))
		return []byte(fmt.Sprintf(`{"timestamp":"%s","level":"INFO","service":"payment-gateway","trace_id":"%s","user_id":"%s","action":"payment_process","amount":%.2f,"currency":"INR","status":"success","error_code":null,"message":"Payment processed successfully","client_ip":"%s","server":"pay-srv-01","response_time_ms":%d}`, timeStr, traceID, userID, amount, clientIP, 150+rng.Intn(400)))
	}
}

// 7. DatabaseSimulator: PostgreSQL Audit CSV
type DatabaseSimulator struct{}

func (s *DatabaseSimulator) Name() string   { return "database" }
func (s *DatabaseSimulator) Format() string { return "csv" }
func (s *DatabaseSimulator) Generate(scenario string, ts time.Time, rng *rand.Rand) []byte {
	timeStr := ts.Format(time.RFC3339)
	header := "timestamp,source_type,client_ip,server_ip,port,user_id,action,statement\n"

	switch scenario {
	case "bruteforce":
		row := fmt.Sprintf("%s,postgres_audit,45.33.32.156,10.0.1.5,5432,root,login_failed,FATAL: password authentication failed for user root\n", timeStr)
		return []byte(header + row)
	case "scan":
		row := fmt.Sprintf("%s,postgres_audit,203.0.113.99,10.0.1.5,5432,anon,unauthorized_access,ERROR: permission denied for relation pg_authid\n", timeStr)
		return []byte(header + row)
	case "malformed":
		return []byte("timestamp,source_type,client_ip\nINVALID_CSV_ROW_MISSING_COLUMNS\n")
	default: // normal
		clientIP := fmt.Sprintf("10.0.1.%d", 50+rng.Intn(6))
		user := fmt.Sprintf("app_srv_%d", 1+rng.Intn(3))
		query := fmt.Sprintf("SELECT * FROM transactions WHERE user_id = '%s' LIMIT 10", clientIP)
		row := fmt.Sprintf("%s,postgres_audit,%s,10.0.1.5,5432,%s,query,%s\n", timeStr, clientIP, user, query)
		return []byte(header + row)
	}
}

func main() {
	sourceFlag := flag.String("source", "all", "Source: firewall|asa|sshd|ids|webserver|app|database|all")
	scenarioFlag := flag.String("scenario", "all", "Scenario: normal|bruteforce|scan|malformed|mixed|all")
	formatFlag := flag.String("format", "stdout", "Output format: stdout|syslog-udp|syslog-tcp|http-post|file")
	targetFlag := flag.String("target", "127.0.0.1:514", "Network target address or URL (e.g. 127.0.0.1:514 or http://127.0.0.1:8080/api/v1/ingest)")
	outputDir := flag.String("output-dir", "simulated", "Output directory when format=file")
	rateFlag := flag.Float64("rate", 10.0, "Events per second (0 = burst as fast as possible)")
	countFlag := flag.Int("count", 100, "Total events to generate (0 = infinite loop)")
	seedFlag := flag.Int64("seed", 42, "Random seed (0 = time-based)")

	flag.Parse()

	var rng *rand.Rand
	if *seedFlag == 0 {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	} else {
		rng = rand.New(rand.NewSource(*seedFlag))
	}

	allSimulators := []SourceSimulator{
		&FirewallSimulator{},
		&ASASimulator{},
		&SSHDSimulator{},
		&IDSSimulator{},
		&WebServerSimulator{},
		&AppSimulator{},
		&DatabaseSimulator{},
	}

	var activeSimulators []SourceSimulator
	if *sourceFlag == "all" {
		activeSimulators = allSimulators
	} else {
		for _, s := range allSimulators {
			if s.Name() == *sourceFlag {
				activeSimulators = append(activeSimulators, s)
			}
		}
	}

	if len(activeSimulators) == 0 {
		fmt.Fprintf(os.Stderr, "Unknown source: %s\n", *sourceFlag)
		os.Exit(1)
	}

	scenarios := []string{"normal", "bruteforce", "scan", "mixed", "malformed"}
	if *scenarioFlag != "all" && *scenarioFlag != "mixed" {
		scenarios = []string{*scenarioFlag}
	}

	// Prepare network sockets if applicable
	var udpConn net.Conn
	var tcpConn net.Conn
	var err error

	if *formatFlag == "syslog-udp" {
		udpConn, err = net.Dial("udp", *targetFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to connect to UDP target %s: %v\n", *targetFlag, err)
			os.Exit(1)
		}
		defer udpConn.Close()
	} else if *formatFlag == "syslog-tcp" {
		tcpConn, err = net.Dial("tcp", *targetFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to connect to TCP target %s: %v\n", *targetFlag, err)
			os.Exit(1)
		}
		defer tcpConn.Close()
	} else if *formatFlag == "file" {
		_ = os.MkdirAll(*outputDir, 0755)
	}

	fmt.Fprintf(os.Stderr, "Starting logsim: sources=%s scenario=%s format=%s count=%d rate=%.1f eps\n",
		*sourceFlag, *scenarioFlag, *formatFlag, *countFlag, *rateFlag)

	var delay time.Duration
	if *rateFlag > 0 {
		delay = time.Duration(float64(time.Second) / *rateFlag)
	}

	var emitted, acked, errCount int
	for {
		if *countFlag > 0 && emitted >= *countFlag {
			break
		}

		sim := activeSimulators[rng.Intn(len(activeSimulators))]
		sc := scenarios[rng.Intn(len(scenarios))]
		if *scenarioFlag == "mixed" {
			// Mixed scenario: 70% normal, 15% bruteforce, 10% scan, 5% malformed
			roll := rng.Float32()
			if roll < 0.70 {
				sc = "normal"
			} else if roll < 0.85 {
				sc = "bruteforce"
			} else if roll < 0.95 {
				sc = "scan"
			} else {
				sc = "malformed"
			}
		}

		payload := sim.Generate(sc, time.Now().UTC(), rng)

		var sendErr error
		switch *formatFlag {
		case "stdout":
			_, sendErr = fmt.Println(string(payload))
		case "syslog-udp":
			_, sendErr = udpConn.Write(payload)
		case "syslog-tcp":
			_, sendErr = fmt.Fprintf(tcpConn, "%s\n", string(payload))
		case "http-post":
			resp, httpErr := http.Post(*targetFlag, "application/json", bytes.NewReader(payload))
			if httpErr != nil {
				sendErr = httpErr
			} else {
				if resp.StatusCode != http.StatusOK {
					sendErr = fmt.Errorf("http status %d", resp.StatusCode)
				}
				_ = resp.Body.Close()
			}
		case "file":
			if sim.Format() == "csv" {
				fileName := fmt.Sprintf("audit_%s_%d.csv", sim.Name(), time.Now().UnixNano())
				filePath := filepath.Join(*outputDir, fileName)
				sendErr = os.WriteFile(filePath, payload, 0644)
			} else {
				filePath := filepath.Join(*outputDir, fmt.Sprintf("%s.log", sim.Name()))
				f, fErr := os.OpenFile(filePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
				if fErr != nil {
					sendErr = fErr
				} else {
					_, sendErr = fmt.Fprintf(f, "%s\n", string(payload))
					_ = f.Close()
				}
			}
		}

		emitted++
		if sendErr != nil {
			errCount++
		} else {
			acked++
		}

		if delay > 0 {
			time.Sleep(delay)
		}
	}

	fmt.Fprintf(os.Stderr, "Finished logsim: emitted=%d acked=%d errors=%d\n", emitted, acked, errCount)
}
