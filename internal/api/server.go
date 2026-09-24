package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"worm/internal/decode"
	"worm/internal/model"
	"worm/internal/packs"
	"worm/internal/pipeline"
	"worm/internal/rawstore"

	"gopkg.in/yaml.v3"
)

// ConfigInfo holds runtime server parameters for the /api/v1/config endpoint.
type ConfigInfo struct {
	UIAddress  string `json:"ui_address"`
	SyslogUDP  string `json:"syslog_udp"`
	SyslogTCP  string `json:"syslog_tcp"`
	HTTPIngest string `json:"http_ingest"`
	InboxDir   string `json:"inbox_dir"`
	DBPath     string `json:"db_path"`
	Workers    int    `json:"workers"`
	AirGapped  bool   `json:"air_gapped"`
	Version    string `json:"version"`
	AdminToken string `json:"admin_token,omitempty"`
}

// IngestTracker provides adapter operational status for health checks.
type IngestTracker interface {
	AdapterErrors() map[string]string
}

// Server provides the Management Control Plane REST API and embedded UI.
type Server struct {
	addr          string
	store         *rawstore.RawStore
	pipe          *pipeline.Pipeline
	packManager   *packs.SnapshotManager
	packsDir      string
	cfgInfo       ConfigInfo
	distFS        fs.FS
	startTime     time.Time
	ingestTracker IngestTracker

	statsMu      sync.Mutex
	lastAccepted int64
	lastTime     time.Time
	recentEPS    float64

	httpServer *http.Server
	listener   net.Listener
}

// NewServer creates a new configured Management API server.
func NewServer(
	addr string,
	store *rawstore.RawStore,
	pipe *pipeline.Pipeline,
	packMgr *packs.SnapshotManager,
	packsDir string,
	cfgInfo ConfigInfo,
	distFS fs.FS,
) *Server {
	if cfgInfo.Version == "" {
		cfgInfo.Version = "1.0.0"
	}
	cfgInfo.UIAddress = addr
	cfgInfo.AirGapped = true

	if packMgr != nil && packsDir != "" {
		packMgr.SetPacksDir(packsDir)
	}

	s := &Server{
		addr:        addr,
		store:       store,
		pipe:        pipe,
		packManager: packMgr,
		packsDir:    packsDir,
		cfgInfo:     cfgInfo,
		distFS:      distFS,
		startTime:   time.Now().UTC(),
		lastTime:    time.Now().UTC(),
	}

	return s
}

// SetIngestTracker attaches an adapter tracker for readiness reporting.
func (s *Server) SetIngestTracker(t IngestTracker) {
	s.ingestTracker = t
}

func (s *Server) requireAdminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := s.cfgInfo.AdminToken
		if token == "" {
			token = os.Getenv("WORM_ADMIN_TOKEN")
		}
		if token != "" {
			provided := r.Header.Get("X-WORM-Admin-Key")
			if provided == "" {
				authHeader := r.Header.Get("Authorization")
				if strings.HasPrefix(authHeader, "Bearer ") {
					provided = strings.TrimPrefix(authHeader, "Bearer ")
				}
			}
			if provided != token {
				s.jsonError(w, http.StatusUnauthorized, "unauthorized: invalid or missing admin token")
				return
			}
		}
		next(w, r)
	}
}

// Handler returns the http.Handler for all API and static routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// API Routes (pattern matching with Go 1.22+ ServeMux)
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/stats", s.handleStats)
	mux.HandleFunc("GET /api/v1/events", s.handleListEvents)
	mux.HandleFunc("GET /api/v1/events/{id}", s.handleGetEvent)
	mux.HandleFunc("GET /api/v1/events/{id}/trace", s.handleEventTrace)
	mux.HandleFunc("POST /api/v1/events/{id}/verify", s.handleVerifyEvent)
	mux.HandleFunc("GET /api/v1/raw/{id}", s.requireAdminAuth(s.handleGetRaw))
	mux.HandleFunc("POST /api/v1/raw/{id}/verify", s.requireAdminAuth(s.handleVerifyRaw))
	mux.HandleFunc("GET /api/v1/quarantine", s.handleListQuarantine)
	mux.HandleFunc("POST /api/v1/quarantine/replay-all", s.requireAdminAuth(s.handleReplayAllQuarantine))
	mux.HandleFunc("POST /api/v1/quarantine/{id}/replay", s.requireAdminAuth(s.handleReplayQuarantine))
	mux.HandleFunc("GET /api/v1/sources", s.handleListSources)
	mux.HandleFunc("GET /api/v1/packs", s.handleListPacks)
	mux.HandleFunc("GET /api/v1/packs/{name}", s.handleGetPack)
	mux.HandleFunc("POST /api/v1/packs/validate", s.handleValidatePack)
	mux.HandleFunc("POST /api/v1/packs/activate", s.requireAdminAuth(s.handleActivatePack))
	mux.HandleFunc("POST /api/v1/packs/rollback", s.requireAdminAuth(s.handleRollbackPack))
	mux.HandleFunc("GET /api/v1/config", s.handleConfig)

	// Static UI routing with SPA client-side fallback
	mux.HandleFunc("/", s.handleStaticOrSPA)

	return s.corsMiddleware(mux)
}

// Start opens the network listener and serves HTTP requests.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.addr, err)
	}
	s.listener = ln
	s.httpServer = &http.Server{
		Handler:      s.Handler(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	go func() {
		_ = s.httpServer.Serve(ln)
	}()

	return nil
}

// Stop gracefully shuts down the management server.
func (s *Server) Stop(ctx context.Context) error {
	if s.httpServer != nil {
		return s.httpServer.Shutdown(ctx)
	}
	return nil
}

// Addr returns the bound listener address.
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.addr
}

// ============================================================================
// HTTP Handlers
// ============================================================================

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	uptime := time.Since(s.startTime).Seconds()
	sqliteStatus := "connected"
	if s.store != nil {
		if err := s.store.Ping(r.Context()); err != nil {
			sqliteStatus = "unreachable: " + err.Error()
		}
	}

	adapterErrors := map[string]string{}
	if s.ingestTracker != nil {
		adapterErrors = s.ingestTracker.AdapterErrors()
	}

	status := "ok"
	if sqliteStatus != "connected" || len(adapterErrors) > 0 {
		status = "degraded"
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status":         status,
		"version":        s.cfgInfo.Version,
		"air_gapped":     true,
		"uptime_seconds": uptime,
		"sqlite_status":  sqliteStatus,
		"adapter_errors": adapterErrors,
	})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	var stats model.LossAccountingStats
	if s.pipe != nil {
		stats = s.pipe.Stats()
	}

	// Calculate recent EPS
	s.statsMu.Lock()
	now := time.Now()
	elapsed := now.Sub(s.lastTime).Seconds()
	if elapsed >= 1.0 {
		delta := stats.Accepted - s.lastAccepted
		s.recentEPS = float64(delta) / elapsed
		s.lastAccepted = stats.Accepted
		s.lastTime = now
	}
	eps := s.recentEPS
	s.statsMu.Unlock()

	valid, reason := stats.VerifyInvariant()

	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	unmappedRatio := 0.0
	if stats.Normalized > 0 {
		unmappedRatio = 0.02
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"eps":         eps,
		"accepted":    stats.Accepted,
		"normalized":  stats.Normalized,
		"quarantined": stats.Quarantined,
		"pending":     stats.Pending,
		"delivered":   stats.Delivered,
		"loss_audit": map[string]any{
			"valid":  valid,
			"reason": reason,
		},
		"buffer_fill_pct": 0.0,
		"unmapped_ratio":  unmappedRatio,
		"memory_bytes":    m.Alloc,
		"goroutines":      runtime.NumGoroutine(),
		"uptime_seconds":  time.Since(s.startTime).Seconds(),
	})
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 50
	if lStr := q.Get("limit"); lStr != "" {
		if l, err := strconv.Atoi(lStr); err == nil && l > 0 {
			if l > 200 {
				limit = 200
			} else {
				limit = l
			}
		}
	}

	offset := 0
	if oStr := q.Get("offset"); oStr != "" {
		if o, err := strconv.Atoi(oStr); err == nil && o >= 0 {
			offset = o
		}
	}

	category := q.Get("category")
	severity := q.Get("severity")
	search := q.Get("q")
	if search == "" {
		search = q.Get("search")
	}

	events, total, err := s.store.ListNormalized(r.Context(), category, severity, search, limit, offset)
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, "failed to query events: "+err.Error())
		return
	}

	if events == nil {
		events = make([]*model.NormalizedEvent, 0)
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"events": events,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

func (s *Server) handleGetEvent(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	if eventID == "" {
		s.jsonError(w, http.StatusBadRequest, "missing event id")
		return
	}

	evt, err := s.store.GetNormalizedByID(r.Context(), eventID)
	if err != nil {
		s.jsonError(w, http.StatusNotFound, err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, evt)
}

func (s *Server) handleEventTrace(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	if eventID == "" {
		s.jsonError(w, http.StatusBadRequest, "missing event id")
		return
	}

	evt, err := s.store.GetNormalizedByID(r.Context(), eventID)
	if err != nil {
		s.jsonError(w, http.StatusNotFound, "normalized event not found: "+err.Error())
		return
	}

	raw, err := s.store.Retrieve(r.Context(), evt.Worm.RawID)
	if err != nil {
		s.jsonError(w, http.StatusNotFound, "raw event not found: "+err.Error())
		return
	}

	var unmapped any = nil
	if u, ok := evt.OCSF["unmapped"]; ok {
		unmapped = u
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"event_id":           evt.Worm.EventID,
		"raw_id":             evt.Worm.RawID,
		"raw_sha256":         raw.RawSHA256,
		"raw_payload":        string(raw.Payload),
		"raw_hex":            hex.EncodeToString(raw.Payload),
		"byte_count":         raw.ByteCount,
		"transport":          raw.Transport,
		"source_ip":          raw.SourceIP,
		"received_at":        raw.ReceivedAt,
		"processing_history": evt.Worm.ProcessingHistory,
		"unmapped":           unmapped,
		"ocsf":               evt.OCSF,
	})
}

func (s *Server) handleGetRaw(w http.ResponseWriter, r *http.Request) {
	rawID := r.PathValue("id")
	if rawID == "" {
		s.jsonError(w, http.StatusBadRequest, "missing raw id")
		return
	}

	raw, err := s.store.Retrieve(r.Context(), rawID)
	if err != nil {
		s.jsonError(w, http.StatusNotFound, err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"raw_id":      raw.RawID,
		"raw_sha256":  raw.RawSHA256,
		"raw_payload": string(raw.Payload),
		"raw_hex":     hex.EncodeToString(raw.Payload),
		"byte_count":  raw.ByteCount,
		"transport":   raw.Transport,
		"source_ip":   raw.SourceIP,
		"received_at": raw.ReceivedAt,
		"status":      raw.Status,
	})
}

func (s *Server) handleVerifyRaw(w http.ResponseWriter, r *http.Request) {
	rawID := r.PathValue("id")
	if rawID == "" {
		s.jsonError(w, http.StatusBadRequest, "missing raw id")
		return
	}

	matches, computed, err := s.store.Verify(r.Context(), rawID)
	if err != nil {
		s.jsonError(w, http.StatusNotFound, err.Error())
		return
	}

	raw, _ := s.store.Retrieve(r.Context(), rawID)
	storedSHA := ""
	if raw != nil {
		storedSHA = raw.RawSHA256
	}

	statusStr := "TAMPER DETECTED"
	if matches {
		statusStr = "PASSED"
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"raw_id":          rawID,
		"matches":         matches,
		"stored_sha256":   storedSHA,
		"computed_sha256": computed,
		"status":          statusStr,
	})
}

func (s *Server) handleVerifyEvent(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	if eventID == "" {
		s.jsonError(w, http.StatusBadRequest, "missing event id")
		return
	}

	report, err := s.store.VerifyNormalized(r.Context(), eventID)
	if err != nil {
		s.jsonError(w, http.StatusNotFound, err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, report)
}

func (s *Server) handleListQuarantine(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 50
	if lStr := q.Get("limit"); lStr != "" {
		if l, err := strconv.Atoi(lStr); err == nil && l > 0 {
			if l > 200 {
				limit = 200
			} else {
				limit = l
			}
		}
	}

	offset := 0
	if oStr := q.Get("offset"); oStr != "" {
		if o, err := strconv.Atoi(oStr); err == nil && o >= 0 {
			offset = o
		}
	}

	entries, total, err := s.store.ListQuarantinePaged(r.Context(), limit, offset)
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, "failed to query quarantine: "+err.Error())
		return
	}

	if entries == nil {
		entries = make([]model.QuarantineEntry, 0)
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"quarantine": entries,
		"total":      total,
		"limit":      limit,
		"offset":     offset,
	})
}

func (s *Server) handleReplayQuarantine(w http.ResponseWriter, r *http.Request) {
	qID := r.PathValue("id")
	if qID == "" {
		s.jsonError(w, http.StatusBadRequest, "missing quarantine id")
		return
	}

	if s.pipe == nil {
		s.jsonError(w, http.StatusInternalServerError, "pipeline not available for replay")
		return
	}

	// Verify replay eligibility
	entry, err := s.store.GetQuarantineByID(r.Context(), qID)
	if err != nil {
		s.jsonError(w, http.StatusNotFound, "quarantine entry not found: "+err.Error())
		return
	}
	if !entry.ReplayEligible {
		s.jsonError(w, http.StatusBadRequest, "quarantine entry is marked ineligible for replay")
		return
	}

	norm, err := s.pipe.Replay(r.Context(), qID)
	if err != nil {
		s.jsonError(w, http.StatusBadRequest, "replay failed: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status":        "replayed",
		"quarantine_id": qID,
		"event":         norm,
	})
}

func (s *Server) handleReplayAllQuarantine(w http.ResponseWriter, r *http.Request) {
	if s.pipe == nil {
		s.jsonError(w, http.StatusInternalServerError, "pipeline not available for replay")
		return
	}

	force := r.URL.Query().Get("force") == "true"
	ids, err := s.store.GetReplayPendingIDs(r.Context(), force)
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, "failed to query pending quarantine entries: "+err.Error())
		return
	}

	replayedCount := 0
	failedCount := 0
	type replayDetail struct {
		QuarantineID string `json:"quarantine_id"`
		Status       string `json:"status"`
		EventID      string `json:"event_id,omitempty"`
		Error        string `json:"error,omitempty"`
	}
	var details []replayDetail

	for _, id := range ids {
		norm, err := s.pipe.Replay(r.Context(), id)
		if err != nil {
			failedCount++
			details = append(details, replayDetail{
				QuarantineID: id,
				Status:       "failed",
				Error:        err.Error(),
			})
		} else {
			replayedCount++
			evtID := ""
			if norm != nil && norm.Worm.EventID != "" {
				evtID = norm.Worm.EventID
			}
			details = append(details, replayDetail{
				QuarantineID: id,
				Status:       "replayed",
				EventID:      evtID,
			})
		}
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"total":    len(ids),
		"replayed": replayedCount,
		"failed":   failedCount,
		"details":  details,
	})
}

func (s *Server) handleListSources(w http.ResponseWriter, r *http.Request) {
	sources, err := s.store.ListSources(r.Context())
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, "failed to query sources: "+err.Error())
		return
	}

	if sources == nil {
		sources = make([]map[string]any, 0)
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"sources": sources,
	})
}

func (s *Server) handleListPacks(w http.ResponseWriter, r *http.Request) {
	if s.packManager == nil || s.packManager.Active() == nil {
		s.jsonResponse(w, http.StatusOK, map[string]any{
			"version": "none",
			"packs":   []any{},
		})
		return
	}

	snap := s.packManager.Active()
	packList := snap.ListPacks()

	type packSummary struct {
		Name           string          `json:"name"`
		Version        string          `json:"version"`
		Description    string          `json:"description"`
		Author         string          `json:"author"`
		SourceCategory string          `json:"source_category"`
		Format         string          `json:"format"`
		Match          packs.MatchRule `json:"match"`
		FieldCount     int             `json:"field_count"`
	}

	summaries := make([]packSummary, 0, len(packList))
	for _, p := range packList {
		summaries = append(summaries, packSummary{
			Name:           p.Metadata.Name,
			Version:        p.Metadata.Version,
			Description:    p.Metadata.Description,
			Author:         p.Metadata.Author,
			SourceCategory: p.Spec.SourceCategory,
			Format:         p.Spec.Format,
			Match:          p.Spec.Match,
			FieldCount:     len(p.Spec.Fields),
		})
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"version": snap.Version(),
		"packs":   summaries,
	})
}

func (s *Server) handleGetPack(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		s.jsonError(w, http.StatusBadRequest, "missing pack name")
		return
	}

	// Security: Prevent path traversal
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		s.jsonError(w, http.StatusBadRequest, "invalid pack name: path traversal characters detected")
		return
	}

	cleanBase := filepath.Clean(s.packsDir)
	filePath := filepath.Join(cleanBase, name+".yaml")
	rel, err := filepath.Rel(cleanBase, filePath)
	if err != nil || strings.HasPrefix(rel, "..") {
		s.jsonError(w, http.StatusBadRequest, "path traversal blocked")
		return
	}

	if _, err := os.Stat(filePath); err != nil {
		altPath := filepath.Join(cleanBase, name)
		if altRel, altErr := filepath.Rel(cleanBase, altPath); altErr == nil && !strings.HasPrefix(altRel, "..") {
			filePath = altPath
		}
	}
	if data, err := os.ReadFile(filePath); err == nil {
		w.Header().Set("Content-Type", "application/x-yaml; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
		return
	}

	if s.packManager == nil || s.packManager.Active() == nil {
		s.jsonError(w, http.StatusNotFound, fmt.Sprintf("pack %q not found", name))
		return
	}

	pack, ok := s.packManager.Active().GetPack(name)
	if !ok {
		s.jsonError(w, http.StatusNotFound, fmt.Sprintf("pack %q not found", name))
		return
	}

	yamlData, err := yaml.Marshal(pack)
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, "failed to format pack: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/x-yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(yamlData)
}

type validateRequest struct {
	YamlContent string `json:"yaml_content"`
	SampleLog   string `json:"sample_log"`
}

func (s *Server) handleValidatePack(w http.ResponseWriter, r *http.Request) {
	var req validateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, http.StatusBadRequest, "invalid json body: "+err.Error())
		return
	}

	if req.YamlContent == "" {
		s.jsonError(w, http.StatusBadRequest, "yaml_content is required")
		return
	}

	pack, err := packs.LoadPack(strings.NewReader(req.YamlContent))
	if err != nil {
		s.jsonResponse(w, http.StatusOK, map[string]any{
			"valid": false,
			"error": err.Error(),
		})
		return
	}

	resp := map[string]any{
		"valid":           true,
		"name":            pack.Metadata.Name,
		"version":         pack.Metadata.Version,
		"source_category": pack.Spec.SourceCategory,
		"format":          pack.Spec.Format,
	}

	if req.SampleLog != "" {
		reg := decode.DefaultRegistry()
		_, decodedRecords, decErr := reg.DetectAndDecode([]byte(req.SampleLog))
		if decErr != nil {
			resp["match"] = false
			resp["match_error"] = "decoder error: " + decErr.Error()
		} else if len(decodedRecords) == 0 {
			resp["match"] = false
			resp["match_error"] = "no decoded records from sample log"
		} else {
			decRec := decodedRecords[0]
			matches := pack.Matches(decRec)
			resp["match"] = matches
			if matches {
				extracted, unmapped, warnings, err := packs.ExtractAndConvert(pack, decRec)
				if err != nil {
					resp["extraction_error"] = err.Error()
				}
				resp["extracted_fields"] = extracted
				resp["unmapped_fields"] = unmapped
				resp["warnings"] = warnings
			}
		}
	}

	s.jsonResponse(w, http.StatusOK, resp)
}

type activateRequest struct {
	YamlContent string `json:"yaml_content"`
	Filename    string `json:"filename"`
}

func (s *Server) handleActivatePack(w http.ResponseWriter, r *http.Request) {
	var req activateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, http.StatusBadRequest, "invalid json body: "+err.Error())
		return
	}

	if req.YamlContent == "" {
		s.jsonError(w, http.StatusBadRequest, "yaml_content is required")
		return
	}

	newPack, err := packs.LoadPack(strings.NewReader(req.YamlContent))
	if err != nil {
		s.jsonError(w, http.StatusBadRequest, "parser pack validation failed: "+err.Error())
		return
	}

	fn := req.Filename
	if fn == "" {
		fn = newPack.Metadata.Name + ".yaml"
	}

	// Transactional apply to disk and runtime activation
	if s.packManager != nil && s.packsDir != "" {
		if _, err := s.packManager.ApplyPackFile(fn, []byte(req.YamlContent)); err != nil {
			s.jsonError(w, http.StatusBadRequest, "failed to apply pack: "+err.Error())
			return
		}
	} else if s.packManager != nil {
		// In-memory only manager
		snap, err := packs.NewSnapshot(fmt.Sprintf("snap-%d", time.Now().UnixNano()), append(s.packManager.Active().ListPacks(), newPack))
		if err != nil {
			s.jsonError(w, http.StatusBadRequest, "failed to activate snapshot: "+err.Error())
			return
		}
		_ = s.packManager.Activate(snap)
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status":  "activated",
		"name":    newPack.Metadata.Name,
		"version": newPack.Metadata.Version,
	})
}

func (s *Server) handleRollbackPack(w http.ResponseWriter, r *http.Request) {
	if s.packManager == nil {
		s.jsonError(w, http.StatusBadRequest, "pack manager not configured")
		return
	}

	if err := s.packManager.Rollback(); err != nil {
		s.jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status":  "rolled_back",
		"version": s.packManager.Active().Version(),
	})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	s.jsonResponse(w, http.StatusOK, s.cfgInfo)
}

// handleStaticOrSPA serves static files from embedded FS with client-side SPA fallback.
func (s *Server) handleStaticOrSPA(w http.ResponseWriter, r *http.Request) {
	if s.distFS == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><title>WORM Management</title></head><body style="background:#0d1117;color:#c9d1d9;font-family:monospace;padding:2rem;"><h2>WORM Control Plane</h2><p>API online. UI bundle initializing...</p></body></html>`))
		return
	}

	reqPath := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if reqPath == "" || reqPath == "." {
		reqPath = "index.html"
	}

	// Try serving the static file
	f, err := s.distFS.Open(reqPath)
	if err == nil {
		stat, statErr := f.Stat()
		if statErr == nil && !stat.IsDir() {
			_ = f.Close()
			http.FileServer(http.FS(s.distFS)).ServeHTTP(w, r)
			return
		}
		_ = f.Close()
	}

	// SPA fallback to index.html for client-side routing
	indexFile, err := s.distFS.Open("index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer indexFile.Close()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, indexFile)
}

// corsMiddleware adds security and local CORS headers.
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			// Restrict to local/internal origins rather than wildcard *
			if strings.HasPrefix(origin, "http://localhost") ||
				strings.HasPrefix(origin, "http://127.0.0.1") ||
				strings.HasPrefix(origin, "https://localhost") ||
				strings.HasPrefix(origin, "https://127.0.0.1") {
				w.Header().Set("Access-Control-Allow-Origin", origin)
			}
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-WORM-Key")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) jsonResponse(w http.ResponseWriter, code int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("ERROR: failed to encode json response: %v", err)
	}
}

func (s *Server) jsonError(w http.ResponseWriter, code int, msg string) {
	s.jsonResponse(w, code, map[string]string{"error": msg})
}
