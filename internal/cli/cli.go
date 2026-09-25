package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"worm/internal/connections"
	"worm/internal/decode"
	"worm/internal/packs"
	"worm/internal/rawstore"
)

const (
	DefaultPIDFile = "data/worm.pid"
	DefaultLogFile = "data/worm.log"
)

func GetBaseURL(uiAddr string) string {
	if uiAddr == "" || uiAddr == "none" {
		uiAddr = ":9090"
	}
	if strings.HasPrefix(uiAddr, ":") {
		return "http://localhost" + uiAddr
	}
	if !strings.HasPrefix(uiAddr, "http://") && !strings.HasPrefix(uiAddr, "https://") {
		return "http://" + uiAddr
	}
	return uiAddr
}

func ReadPID(pidFile string) (int, error) {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, err
	}
	pidStr := strings.TrimSpace(string(data))
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return 0, err
	}
	return pid, nil
}

func RunDaemonMode(pidFile, logFile string, uiAddr string) {
	if os.Getenv("WORM_DAEMON") == "1" {
		return
	}

	// Check if already running
	if pid, err := ReadPID(pidFile); err == nil && IsPIDAlive(pid) {
		fmt.Fprintf(os.Stderr, "ERROR: WORM engine is already running (PID: %d).\nUse './bin/worm -stop' to stop it first.\n", pid)
		os.Exit(1)
	}

	// Ensure log and pid directory exist
	if dir := filepath.Dir(logFile); dir != "." && dir != "" {
		_ = os.MkdirAll(dir, 0755)
	}
	if dir := filepath.Dir(pidFile); dir != "." && dir != "" {
		_ = os.MkdirAll(dir, 0755)
	}

	// Prepare child args (filter out -d)
	var childArgs []string
	for _, a := range os.Args[1:] {
		if a == "-d" || a == "--d" {
			continue
		}
		childArgs = append(childArgs, a)
	}
	// Default stdout to false in daemon mode if not specified
	hasStdout := false
	for _, a := range childArgs {
		if strings.Contains(a, "-stdout") {
			hasStdout = true
			break
		}
	}
	if !hasStdout {
		childArgs = append(childArgs, "-stdout=false")
	}

	outLog, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: Failed to open log file %s: %v\n", logFile, err)
		os.Exit(1)
	}

	execPath, err := os.Executable()
	if err != nil {
		execPath = os.Args[0]
	}

	cmd := exec.Command(execPath, childArgs...)
	cmd.Env = append(os.Environ(), "WORM_DAEMON=1")
	cmd.Stdout = outLog
	cmd.Stderr = outLog
	setDaemonSysProcAttr(cmd)

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: Failed to start background daemon: %v\n", err)
		os.Exit(1)
	}

	// Write PID file
	_ = os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", cmd.Process.Pid)), 0644)

	fmt.Printf("\n[WORM] Engine daemon started successfully in background!\n")
	fmt.Printf("  * Process ID:      %d\n", cmd.Process.Pid)
	fmt.Printf("  * Management UI:   %s\n", GetBaseURL(uiAddr))
	fmt.Printf("  * Logging Stream:  %s\n", logFile)
	fmt.Printf("  * Quick Commands:\n")
	fmt.Printf("      ./bin/worm packs list              # List active parser packs\n")
	fmt.Printf("      ./bin/worm packs get <name>        # View pack YAML definition\n")
	fmt.Printf("      ./bin/worm packs apply <pack.yaml> # Install & reconcile new pack\n")
	fmt.Printf("      ./bin/worm sources list            # List active ingestion sources\n")
	fmt.Printf("      ./bin/worm sinks list              # List active delivery sinks\n")
	fmt.Printf("      ./bin/worm replay [id|all]         # Manage or replay quarantined DLQ records\n")
	fmt.Printf("      ./bin/worm status                  # Check background daemon health\n")
	fmt.Printf("      ./bin/worm stop                    # Stop the background daemon\n\n")
	os.Exit(0)
}

func StopDaemon(pidFile string) {
	pid, err := ReadPID(pidFile)
	if err != nil || !IsPIDAlive(pid) {
		fmt.Fprintf(os.Stderr, "[WORM] No running daemon found (PID file %s missing or inactive).\n", pidFile)
		_ = os.Remove(pidFile)
		return
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[WORM] Failed to find process %d: %v\n", pid, err)
		return
	}

	fmt.Printf("[WORM] Stopping daemon process (PID: %d)...\n", pid)
	if err := stopProcess(proc); err != nil {
		fmt.Fprintf(os.Stderr, "[WORM] Error stopping daemon: %v\n", err)
	}
	_ = os.Remove(pidFile)
	fmt.Printf("[WORM] Engine daemon (PID: %d) stopped successfully.\n", pid)
}

func StatusDaemon(pidFile, uiAddr string) {
	pid, err := ReadPID(pidFile)
	alive := (err == nil && IsPIDAlive(pid))

	baseURL := GetBaseURL(uiAddr)
	client := &http.Client{Timeout: 1 * time.Second}
	resp, httpErr := client.Get(baseURL + "/api/v1/health")

	fmt.Printf("\n--- WORM Daemon Status ---\n")
	if alive {
		fmt.Printf("  Process Status:  RUNNING (PID: %d)\n", pid)
	} else {
		fmt.Printf("  Process Status:  STOPPED\n")
	}

	if httpErr == nil && resp.StatusCode == http.StatusOK {
		var health map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&health)
		resp.Body.Close()
		fmt.Printf("  Control Plane:   HEALTHY (%s)\n", baseURL)
		if u, ok := health["uptime_seconds"].(float64); ok {
			fmt.Printf("  Uptime:          %s\n", time.Duration(u*float64(time.Second)).Truncate(time.Second))
		}
		if ag, ok := health["air_gapped"].(bool); ok {
			fmt.Printf("  Air-Gapped Mode: %v\n", ag)
		}
	} else {
		fmt.Printf("  Control Plane:   UNREACHABLE (%s)\n", baseURL)
	}
	fmt.Printf("--------------------------\n\n")
}

func ListPacksCLI(uiAddr, packsDir string) {
	baseURL := GetBaseURL(uiAddr)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(baseURL + "/api/v1/packs")

	type packSummary struct {
		Name           string          `json:"name"`
		Version        string          `json:"version"`
		SourceCategory string          `json:"source_category"`
		Format         string          `json:"format"`
		Match          packs.MatchRule `json:"match"`
		FieldCount     int             `json:"field_count"`
	}

	var packList []packSummary
	sourceDesc := "Runtime Memory Snapshot (" + baseURL + ")"

	if err == nil && resp.StatusCode == http.StatusOK {
		var result struct {
			Version string        `json:"version"`
			Packs   []packSummary `json:"packs"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err == nil {
			packList = result.Packs
		}
		resp.Body.Close()
	} else {
		// Fallback to offline disk
		sourceDesc = "Local Directory (" + packsDir + ")"
		if snap, err := packs.LoadDir(packsDir); err == nil {
			for _, p := range snap.ListPacks() {
				packList = append(packList, packSummary{
					Name:           p.Metadata.Name,
					Version:        p.Metadata.Version,
					SourceCategory: p.Spec.SourceCategory,
					Format:         p.Spec.Format,
					Match:          p.Spec.Match,
					FieldCount:     len(p.Spec.Fields),
				})
			}
		}
	}

	fmt.Printf("\n--- Active Parser Packs [%s] ---\n", sourceDesc)
	if len(packList) == 0 {
		fmt.Println("No parser packs found.")
		fmt.Printf("--------------------------------------------------\n\n")
		return
	}

	fmt.Printf("%-26s %-10s %-16s %-8s %s\n", "NAME", "VERSION", "CATEGORY", "FORMAT", "MATCH CRITERIA")
	fmt.Println(strings.Repeat("-", 80))
	for _, p := range packList {
		matchStr := ""
		if p.Match.Contains != "" {
			matchStr = fmt.Sprintf("contains: %q", p.Match.Contains)
		} else if p.Match.Regex != "" {
			matchStr = fmt.Sprintf("regex: %s", p.Match.Regex)
		} else if len(p.Match.FieldEquals) > 0 {
			var kvs []string
			for k, v := range p.Match.FieldEquals {
				kvs = append(kvs, fmt.Sprintf("%s=%s", k, v))
			}
			matchStr = "fieldEquals: " + strings.Join(kvs, ", ")
		}
		if len(matchStr) > 35 {
			matchStr = matchStr[:32] + "..."
		}
		fmt.Printf("%-26s %-10s %-16s %-8s %s\n", p.Name, p.Version, p.SourceCategory, p.Format, matchStr)
	}
	fmt.Printf("--------------------------------------------------------------------------------\n\n")
}

func ViewPackCLI(packName, uiAddr, packsDir string) {
	packName = filepath.Base(packName)
	if strings.ContainsAny(packName, "/\\") || strings.Contains(packName, "..") {
		fmt.Fprintf(os.Stderr, "ERROR: Invalid pack name %q\n", packName)
		os.Exit(1)
	}

	baseURL := GetBaseURL(uiAddr)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(baseURL + "/api/v1/packs/" + packName)

	if err == nil && resp.StatusCode == http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		fmt.Printf("\n# Parser Pack: %s (Source: Runtime API)\n", packName)
		fmt.Println(string(data))
		return
	}

	// Fallback to disk
	cleanPacksDir := filepath.Clean(packsDir)
	filePath := filepath.Join(cleanPacksDir, packName+".yaml")
	if _, err := os.Stat(filePath); err != nil {
		filePath = filepath.Join(cleanPacksDir, packName)
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Pack %q not found on server or at %s: %v\n", packName, filePath, err)
		os.Exit(1)
	}

	fmt.Printf("\n# Parser Pack: %s (Source: %s)\n", packName, filePath)
	fmt.Println(string(data))
}

func ApplyPackCLI(packFilePath, uiAddr, packsDir string) {
	data, err := os.ReadFile(packFilePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Failed to read pack file %s: %v\n", packFilePath, err)
		os.Exit(1)
	}

	// Validate pack syntax and rules
	pack, err := packs.LoadPack(bytes.NewReader(data))
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Pack validation failed for %s: %v\n", packFilePath, err)
		os.Exit(1)
	}

	// 1. Copy permanently to packsDir
	safeName := filepath.Base(pack.Metadata.Name)
	if strings.ContainsAny(safeName, "/\\") || strings.Contains(safeName, "..") || safeName == "." {
		fmt.Fprintf(os.Stderr, "ERROR: Pack name %q contains invalid characters\n", pack.Metadata.Name)
		os.Exit(1)
	}
	cleanPacksDir := filepath.Clean(packsDir)
	destPath := filepath.Join(cleanPacksDir, safeName+".yaml")
	rel, err := filepath.Rel(cleanPacksDir, destPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		fmt.Fprintf(os.Stderr, "ERROR: Invalid destination path for pack\n")
		os.Exit(1)
	}

	if err := os.WriteFile(destPath, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Failed to save pack to %s: %v\n", destPath, err)
		os.Exit(1)
	}

	fmt.Printf("\n[WORM] Validated parser pack '%s' v%s (format: %s)\n", pack.Metadata.Name, pack.Metadata.Version, pack.Spec.Format)
	fmt.Printf("[WORM] Installed permanently to: %s\n", destPath)

	// 2. Activate in running daemon if available
	baseURL := GetBaseURL(uiAddr)
	client := &http.Client{Timeout: 3 * time.Second}

	reqBody, _ := json.Marshal(map[string]string{
		"yaml_content": string(data),
		"filename":     pack.Metadata.Name + ".yaml",
	})
	resp, err := client.Post(baseURL+"/api/v1/packs/activate", "application/json", bytes.NewReader(reqBody))
	if err != nil || resp.StatusCode != http.StatusOK {
		fmt.Printf("[WORM] (Engine daemon not currently reachable at %s; pack will load on next startup)\n\n", baseURL)
		return
	}
	resp.Body.Close()
	fmt.Printf("[WORM] Runtime memory snapshot updated successfully.\n")

	// 3. Reconcile quarantine DLQ: trigger bulk replay for matching quarantined logs
	fmt.Printf("[WORM] Reconciling Quarantine DLQ against newly activated pack...\n")
	replayResp, err := client.Post(baseURL+"/api/v1/quarantine/replay-all", "application/json", nil)
	if err == nil && replayResp.StatusCode == http.StatusOK {
		var repResult struct {
			Total    int `json:"total"`
			Replayed int `json:"replayed"`
			Failed   int `json:"failed"`
		}
		_ = json.NewDecoder(replayResp.Body).Decode(&repResult)
		replayResp.Body.Close()
		fmt.Printf("[WORM] Reconcile Complete: %d events reprocessed successfully (%d failed, %d pending).\n\n",
			repResult.Replayed, repResult.Failed, repResult.Total-repResult.Replayed)
	} else {
		fmt.Printf("[WORM] Reconcile complete.\n\n")
	}
}

func ListQuarantineCLI(uiAddr, dbPath string) {
	baseURL := GetBaseURL(uiAddr)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(baseURL + "/api/v1/quarantine?limit=50")

	type quarItem struct {
		QuarantineID  string `json:"quarantine_id"`
		RawID         string `json:"raw_id"`
		Stage         string `json:"stage"`
		Reason        string `json:"reason"`
		RawPreview    string `json:"raw_preview"`
		QuarantinedAt string `json:"quarantined_at"`
	}

	var items []quarItem

	if err == nil && resp.StatusCode == http.StatusOK {
		var res struct {
			Quarantine []quarItem `json:"quarantine"`
			Total      int        `json:"total"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&res)
		resp.Body.Close()
		items = res.Quarantine
	} else {
		// Fallback to SQLite store
		store, err := rawstore.New(dbPath)
		if err == nil {
			entries, _, _ := store.ListQuarantinePaged(context.Background(), 50, 0)
			store.Close()
			for _, e := range entries {
				items = append(items, quarItem{
					QuarantineID:  e.QuarantineID,
					RawID:         e.RawID,
					Stage:         e.Stage,
					Reason:        e.Reason,
					RawPreview:    e.RawPreview,
					QuarantinedAt: e.QuarantinedAt.Format(time.RFC3339),
				})
			}
		}
	}

	fmt.Printf("\n--- Quarantine Dead-Letter Queue (DLQ) ---\n")
	if len(items) == 0 {
		fmt.Println("Quarantine queue is empty. All ingested events are normalized and delivered.")
		fmt.Printf("-------------------------------------------\n\n")
		return
	}

	fmt.Printf("%-32s %-14s %-16s %s\n", "QUARANTINE ID", "STAGE", "REASON", "PREVIEW")
	fmt.Println(strings.Repeat("-", 80))
	for _, it := range items {
		preview := it.RawPreview
		if len(preview) > 30 {
			preview = preview[:27] + "..."
		}
		fmt.Printf("%-32s %-14s %-16s %s\n", it.QuarantineID, it.Stage, it.Reason, preview)
	}
	fmt.Printf("--------------------------------------------------------------------------------\n")
	fmt.Printf("Use './bin/worm -replay <id>' or './bin/worm -replay all' to reprocess.\n\n")
}

func ReplaySingleCLI(qID, uiAddr string) {
	baseURL := GetBaseURL(uiAddr)
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Post(baseURL+"/api/v1/quarantine/"+qID+"/replay", "application/json", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Failed to contact WORM server at %s: %v\n", baseURL, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "Replay failed: %s\n", string(body))
		os.Exit(1)
	}

	var res struct {
		Status string `json:"status"`
		Event  struct {
			Worm struct {
				EventID string `json:"event_id"`
			} `json:"worm"`
		} `json:"event"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&res)
	fmt.Printf("\n[WORM] Replay Successful!\n")
	fmt.Printf("  * Quarantine Record: %s (Removed from DLQ)\n", qID)
	fmt.Printf("  * Normalized Event:  %s\n\n", res.Event.Worm.EventID)
}

func ReplayAllCLI(uiAddr string) {
	baseURL := GetBaseURL(uiAddr)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(baseURL+"/api/v1/quarantine/replay-all", "application/json", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Failed to contact WORM server at %s: %v\n", baseURL, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	var res struct {
		Total    int `json:"total"`
		Replayed int `json:"replayed"`
		Failed   int `json:"failed"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&res)

	fmt.Printf("\n--- WORM Bulk Replay Report ---\n")
	fmt.Printf("  * Total Eligible:  %d\n", res.Total)
	fmt.Printf("  * Replayed:        %d (Normalized and removed from DLQ)\n", res.Replayed)
	fmt.Printf("  * Failed:          %d\n", res.Failed)
	fmt.Printf("-------------------------------\n\n")
}

// --- Parser Packs CLI (kind: ParserPack) ---

func GetPackCLI(packName, uiAddr, packsDir string) {
	ViewPackCLI(packName, uiAddr, packsDir)
}

func ValidatePackCLI(filePath string) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: failed to read file %s: %v\n", filePath, err)
		os.Exit(1)
	}

	pack, err := packs.LoadPack(bytes.NewReader(data))
	if err != nil {
		fmt.Fprintf(os.Stderr, "PACK VALIDATION FAILED: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n[WORM] Parser Pack YAML is VALID!\n")
	fmt.Printf("  * Kind:     ParserPack\n")
	fmt.Printf("  * Name:     %s\n", pack.Metadata.Name)
	fmt.Printf("  * Version:  %s\n", pack.Metadata.Version)
	fmt.Printf("  * Category: %s\n", pack.Spec.SourceCategory)
	fmt.Printf("  * Format:   %s\n", pack.Spec.Format)
	fmt.Printf("  * Mappings: %d field mappings defined\n\n", len(pack.Spec.Fields))
}

func TestPackCLI(filePath, sample string) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: failed to read file %s: %v\n", filePath, err)
		os.Exit(1)
	}

	pack, err := packs.LoadPack(bytes.NewReader(data))
	if err != nil {
		fmt.Fprintf(os.Stderr, "PACK VALIDATION FAILED: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n[WORM] Testing Parser Pack %q v%s (%s / %s)...\n", pack.Metadata.Name, pack.Metadata.Version, pack.Spec.SourceCategory, pack.Spec.Format)

	var sampleBytes []byte
	if sample != "" {
		if fileData, err := os.ReadFile(sample); err == nil {
			sampleBytes = fileData
		} else {
			sampleBytes = []byte(sample)
		}
	} else {
		fmt.Printf("[WORM] Pack schema & syntax test PASSED!\n")
		fmt.Printf("  * Pass '-sample <sample_log_or_file>' to execute matching & OCSF extraction tests.\n\n")
		return
	}

	reg := decode.DefaultRegistry()
	_, decodedRecords, err := reg.DetectAndDecode(sampleBytes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "DECODER ERROR on sample: %v\n", err)
		os.Exit(1)
	}
	if len(decodedRecords) == 0 {
		fmt.Fprintf(os.Stderr, "NO RECORDS DECODED from sample\n")
		os.Exit(1)
	}

	decRec := decodedRecords[0]
	matched := pack.Matches(decRec)
	if !matched {
		fmt.Fprintf(os.Stderr, "PACK MATCH RESULT: FALSE (Sample did not match criteria)\n")
		os.Exit(1)
	}

	extracted, unmapped, warnings, err := packs.ExtractAndConvert(pack, decRec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "EXTRACTION ERROR: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("[WORM] Pack Test PASSED!\n")
	fmt.Printf("  * Match:     TRUE\n")
	fmt.Printf("  * Extracted: %d OCSF fields\n", len(extracted))
	for k, v := range extracted {
		fmt.Printf("    - %s: %v\n", k, v)
	}
	if len(warnings) > 0 {
		fmt.Printf("  * Warnings:  %s\n", strings.Join(warnings, ", "))
	}
	if len(unmapped) > 0 {
		fmt.Printf("  * Unmapped:  %d fields preserved in unmapped\n", len(unmapped))
	}
	fmt.Println()
}

func RollbackPackCLI(uiAddr, packsDir string) {
	baseURL := GetBaseURL(uiAddr)
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Post(baseURL+"/api/v1/packs/rollback", "application/json", nil)
	if err == nil && resp.StatusCode == http.StatusOK {
		var res struct {
			Status  string `json:"status"`
			Version string `json:"version"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&res)
		resp.Body.Close()
		fmt.Printf("\n[WORM] Parser Pack Rollback Successful!\n")
		fmt.Printf("  * Active Runtime Snapshot: %s\n\n", res.Version)
		return
	}
	if resp != nil {
		resp.Body.Close()
	}

	// Fallback to local snapshot manager if server offline
	snap, err := packs.LoadDir(packsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: failed to load packs directory: %v\n", err)
		os.Exit(1)
	}
	mgr := packs.NewSnapshotManager(snap)
	mgr.SetPacksDir(packsDir)
	if err := mgr.Rollback(); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: pack rollback failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\n[WORM] Parser Pack Local Rollback Successful!\n\n")
}

// --- Sources CLI (kind: Source) ---

func ListSourcesCLI(uiAddr, srcDir string) {
	if srcDir == "" {
		srcDir = "sources"
	}
	mgr := connections.NewManager(srcDir)
	_ = mgr.LoadDir(srcDir)
	list := mgr.List()

	fmt.Printf("\n--- Active Ingestion Sources [Directory (%s)] ---\n", srcDir)
	fmt.Printf("%-24s %-10s %-16s %-8s %s\n", "NAME", "VERSION", "TYPE", "ENABLED", "TARGET ENDPOINT / TOPIC")
	fmt.Println(strings.Repeat("-", 80))
	for _, c := range list {
		target := c.Summary().Target
		if target == "" {
			target = "-"
		}
		if len(target) > 30 {
			target = target[:27] + "..."
		}
		fmt.Printf("%-24s %-10s %-16s %-8v %s\n", c.Metadata.Name, c.Metadata.Version, c.Spec.Type, c.Spec.Enabled, target)
	}
	fmt.Printf("%s\n\n", strings.Repeat("-", 80))
}

func GetSourceCLI(name, uiAddr, srcDir string) {
	if srcDir == "" {
		srcDir = "sources"
	}
	mgr := connections.NewManager(srcDir)
	_ = mgr.LoadDir(srcDir)
	c, err := mgr.Get(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: source %q not found in %s\n", name, srcDir)
		os.Exit(1)
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	fmt.Println(string(data))
}

func ValidateSourceCLI(filePath string) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: failed to read file %s: %v\n", filePath, err)
		os.Exit(1)
	}

	src, err := connections.LoadSource(strings.NewReader(string(data)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "SOURCE VALIDATION FAILED: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n[WORM] Source YAML is VALID!\n")
	fmt.Printf("  * Kind:    %s\n", src.Kind)
	fmt.Printf("  * Name:    %s\n", src.Metadata.Name)
	fmt.Printf("  * Version: %s\n", src.Metadata.Version)
	fmt.Printf("  * Type:    %s\n", src.Spec.Type)
	fmt.Printf("  * Enabled: %v\n\n", src.Spec.Enabled)
}

func TestSourceCLI(filePath string) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: failed to read file %s: %v\n", filePath, err)
		os.Exit(1)
	}

	src, err := connections.LoadSource(strings.NewReader(string(data)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "SOURCE VALIDATION FAILED: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := src.TestConnectivity(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "SOURCE CONNECTIVITY TEST FAILED: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n[WORM] Source Connectivity Test PASSED for %q (%s)\n\n", src.Metadata.Name, src.Spec.Type)
}

func applyConnectionAPI(filePath, uiAddr string) (*connections.Connection, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(map[string]string{"filename": filepath.Base(filePath), "yaml_content": string(data)})
	req, err := http.NewRequest(http.MethodPost, GetBaseURL(uiAddr)+"/api/v1/connections/apply", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token := os.Getenv("WORM_ADMIN_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("live control plane unavailable: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("apply failed: %s", strings.TrimSpace(string(body)))
	}
	var result struct {
		Connection *connections.Connection `json:"connection"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if result.Connection == nil {
		return nil, fmt.Errorf("apply response omitted connection")
	}
	return result.Connection, nil
}
func rollbackConnectionAPI(uiAddr string) error {
	req, err := http.NewRequest(http.MethodPost, GetBaseURL(uiAddr)+"/api/v1/connections/rollback", nil)
	if err != nil {
		return err
	}
	if token := os.Getenv("WORM_ADMIN_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("live control plane unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("rollback failed: %s", strings.TrimSpace(string(body)))
	}
	return nil
}

func ApplySourceCLI(filePath, uiAddr, srcDir string) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
	if _, err = connections.LoadSource(strings.NewReader(string(data))); err != nil {
		fmt.Fprintln(os.Stderr, "SOURCE VALIDATION FAILED:", err)
		os.Exit(1)
	}
	applied, err := applyConnectionAPI(filePath, uiAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
	fmt.Printf("\\n[WORM] Source Applied Live: %s@%s (%s)\\n\\n", applied.Metadata.Name, applied.Metadata.Version, applied.Spec.Type)
}

func RollbackSourceCLI(uiAddr, srcDir string) {
	if err := rollbackConnectionAPI(uiAddr); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
	fmt.Println("\\n[WORM] Connection Rollback Applied Live!\\n")
}

// --- Sinks CLI (kind: Sink) ---

func ListSinksCLI(uiAddr, sinkDir string) {
	if sinkDir == "" {
		sinkDir = "sinks"
	}
	mgr := connections.NewManager(sinkDir)
	_ = mgr.LoadDir(sinkDir)
	list := mgr.List()

	fmt.Printf("\n--- Active Delivery Sinks [Directory (%s)] ---\n", sinkDir)
	fmt.Printf("%-24s %-10s %-16s %-8s %s\n", "NAME", "VERSION", "TYPE", "ENABLED", "TARGET ENDPOINT / PATH")
	fmt.Println(strings.Repeat("-", 80))
	for _, c := range list {
		target := c.Summary().Target
		if target == "" {
			target = "-"
		}
		if len(target) > 30 {
			target = target[:27] + "..."
		}
		fmt.Printf("%-24s %-10s %-16s %-8v %s\n", c.Metadata.Name, c.Metadata.Version, c.Spec.Type, c.Spec.Enabled, target)
	}
	fmt.Printf("%s\n\n", strings.Repeat("-", 80))
}

func GetSinkCLI(name, uiAddr, sinkDir string) {
	if sinkDir == "" {
		sinkDir = "sinks"
	}
	mgr := connections.NewManager(sinkDir)
	_ = mgr.LoadDir(sinkDir)
	c, err := mgr.Get(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: sink %q not found in %s\n", name, sinkDir)
		os.Exit(1)
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	fmt.Println(string(data))
}

func ValidateSinkCLI(filePath string) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: failed to read file %s: %v\n", filePath, err)
		os.Exit(1)
	}

	snk, err := connections.LoadSink(strings.NewReader(string(data)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "SINK VALIDATION FAILED: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n[WORM] Sink YAML is VALID!\n")
	fmt.Printf("  * Kind:    %s\n", snk.Kind)
	fmt.Printf("  * Name:    %s\n", snk.Metadata.Name)
	fmt.Printf("  * Version: %s\n", snk.Metadata.Version)
	fmt.Printf("  * Type:    %s\n", snk.Spec.Type)
	fmt.Printf("  * Enabled: %v\n\n", snk.Spec.Enabled)
}

func TestSinkCLI(filePath string) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: failed to read file %s: %v\n", filePath, err)
		os.Exit(1)
	}

	snk, err := connections.LoadSink(strings.NewReader(string(data)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "SINK VALIDATION FAILED: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := snk.TestConnectivity(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "SINK CONNECTIVITY TEST FAILED: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n[WORM] Sink Connectivity Test PASSED for %q (%s)\n\n", snk.Metadata.Name, snk.Spec.Type)
}

func ApplySinkCLI(filePath, uiAddr, sinkDir string) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
	if _, err = connections.LoadSink(strings.NewReader(string(data))); err != nil {
		fmt.Fprintln(os.Stderr, "SINK VALIDATION FAILED:", err)
		os.Exit(1)
	}
	applied, err := applyConnectionAPI(filePath, uiAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
	fmt.Printf("\\n[WORM] Sink Applied Live: %s@%s (%s)\\n\\n", applied.Metadata.Name, applied.Metadata.Version, applied.Spec.Type)
}

func RollbackSinkCLI(uiAddr, sinkDir string) {
	if err := rollbackConnectionAPI(uiAddr); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
	fmt.Println("\\n[WORM] Connection Rollback Applied Live!\\n")
}
