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
	fmt.Printf("      ./bin/worm -packs                  # List active parser packs\n")
	fmt.Printf("      ./bin/worm -pack <name>            # View pack YAML\n")
	fmt.Printf("      ./bin/worm -packs -f <pack.yaml>   # Install & reconcile new pack\n")
	fmt.Printf("      ./bin/worm -replay                 # View quarantined DLQ records\n")
	fmt.Printf("      ./bin/worm -replay all             # Replay all quarantined records\n")
	fmt.Printf("      ./bin/worm -status                 # Check daemon status\n")
	fmt.Printf("      ./bin/worm -stop                   # Stop the background daemon\n\n")
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
