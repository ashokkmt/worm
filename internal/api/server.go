package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"
	"worm/internal/connections"
	"worm/internal/decode"
	"worm/internal/marketplace"
	"worm/internal/model"
	"worm/internal/ocsf"
	"worm/internal/packs"
	"worm/internal/pipeline"
	"worm/internal/rawstore"

	"gopkg.in/yaml.v3"
)

// ConfigInfo holds runtime server parameters for the /api/v1/config endpoint.
type ConfigInfo struct {
	UIAddress         string `json:"ui_address"`
	SyslogUDP         string `json:"syslog_udp"`
	SyslogTCP         string `json:"syslog_tcp"`
	SyslogTLS         string `json:"syslog_tls,omitempty"`
	HTTPIngest        string `json:"http_ingest"`
	InboxDir          string `json:"inbox_dir"`
	DBPath            string `json:"db_path"`
	Workers           int    `json:"workers"`
	AirGapped         bool   `json:"air_gapped"`
	MarketplaceOnline bool   `json:"marketplace_online"`
	AdminToken        string `json:"-"`
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
	marketplace   marketplace.LocalStore
	packsDir      string
	connManager   *connections.Manager
	connsDir      string
	cfgInfo       ConfigInfo
	version       string
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
	version string,
) *Server {
	cfgInfo.UIAddress = addr
	cfgInfo.AirGapped = false // deprecated: marketplace access does not describe adapter connectivity

	if packMgr != nil && packsDir != "" {
		packMgr.SetPacksDir(packsDir)
		_ = marketplace.CacheBundledArtifacts(packsDir)
	}
	market := marketplace.LocalStore{Dir: packsDir}

	s := &Server{
		addr:        addr,
		store:       store,
		pipe:        pipe,
		packManager: packMgr,
		marketplace: market,
		packsDir:    packsDir,
		cfgInfo:     cfgInfo,
		version:     version,
		distFS:      distFS,
		startTime:   time.Now().UTC(),
		lastTime:    time.Now().UTC(),
	}

	return s
}

// SetMarketplaceOptions configures explicit outbound marketplace access.
func (s *Server) SetMarketplaceOptions(online bool, registryURL string) {
	s.marketplace.Online = online
	s.marketplace.RegistryURL = registryURL
	s.cfgInfo.MarketplaceOnline = online
	s.cfgInfo.AirGapped = !online
}

// SetIngestTracker attaches an adapter tracker for readiness reporting.
func (s *Server) SetIngestTracker(t IngestTracker) {
	s.ingestTracker = t
}

// SetConnManager attaches a connection manager and directory.
func (s *Server) SetConnManager(mgr *connections.Manager, dir string) {
	s.connManager = mgr
	s.connsDir = dir
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

func (s *Server) requireConfiguredAdminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := s.cfgInfo.AdminToken
		if token == "" {
			token = os.Getenv("WORM_ADMIN_TOKEN")
		}
		if token == "" {
			s.jsonError(w, http.StatusForbidden, "marketplace management requires WORM_ADMIN_TOKEN or --admin-token")
			return
		}
		provided := r.Header.Get("X-WORM-Admin-Key")
		if provided == "" && strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			provided = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		if provided != token {
			s.jsonError(w, http.StatusUnauthorized, "unauthorized: invalid or missing admin token")
			return
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
	mux.HandleFunc("GET /api/v1/events/{id}/trace", s.requireAdminAuth(s.handleEventTrace))
	mux.HandleFunc("POST /api/v1/events/{id}/verify", s.handleVerifyEvent)
	mux.HandleFunc("GET /api/v1/raw/{id}", s.requireAdminAuth(s.handleGetRaw))
	mux.HandleFunc("POST /api/v1/raw/{id}/verify", s.requireAdminAuth(s.handleVerifyRaw))
	mux.HandleFunc("GET /api/v1/quarantine", s.handleListQuarantine)
	mux.HandleFunc("POST /api/v1/quarantine/replay-all", s.requireAdminAuth(s.handleReplayAllQuarantine))
	mux.HandleFunc("POST /api/v1/quarantine/{id}/replay", s.requireAdminAuth(s.handleReplayQuarantine))
	mux.HandleFunc("GET /api/v1/sources", s.handleListSources)
	mux.HandleFunc("GET /api/v1/packs", s.handleListPacks)
	mux.HandleFunc("GET /api/v1/marketplace/packs", s.handleMarketplacePacks)
	mux.HandleFunc("GET /api/v1/marketplace/packs/{name}", s.handleMarketplacePack)
	mux.HandleFunc("GET /api/v1/marketplace/status", s.handleMarketplaceStatus)
	mux.HandleFunc("POST /api/v1/marketplace/refresh", s.requireConfiguredAdminAuth(s.handleMarketplaceRefresh))
	mux.HandleFunc("POST /api/v1/marketplace/fetch", s.requireConfiguredAdminAuth(s.handleMarketplaceFetch))
	mux.HandleFunc("GET /api/v1/packs/{name}", s.handleGetPack)
	mux.HandleFunc("POST /api/v1/packs/validate", s.handleValidatePack)
	mux.HandleFunc("POST /api/v1/packs/activate", s.requireConfiguredAdminAuth(s.handleActivatePack))
	mux.HandleFunc("POST /api/v1/packs/install", s.requireConfiguredAdminAuth(s.handleMarketplaceInstall))
	mux.HandleFunc("POST /api/v1/packs/upgrade", s.requireConfiguredAdminAuth(s.handleMarketplaceUpgrade))
	mux.HandleFunc("DELETE /api/v1/packs/{name}", s.requireConfiguredAdminAuth(s.handleMarketplaceRemove))
	mux.HandleFunc("POST /api/v1/packs/rollback", s.requireConfiguredAdminAuth(s.handleRollbackPack))
	mux.HandleFunc("GET /api/v1/connections", s.handleListConnections)
	mux.HandleFunc("GET /api/v1/connections/{name}", s.handleGetConnection)
	mux.HandleFunc("POST /api/v1/connections/validate", s.handleValidateConnection)
	mux.HandleFunc("POST /api/v1/connections/test", s.requireAdminAuth(s.handleTestConnection))
	mux.HandleFunc("POST /api/v1/connections/apply", s.requireAdminAuth(s.handleApplyConnection))
	mux.HandleFunc("POST /api/v1/connections/rollback", s.requireAdminAuth(s.handleRollbackConnection))
	mux.HandleFunc("GET /api/v1/config", s.handleConfig)

	// Static UI routing with SPA client-side fallback
	mux.HandleFunc("/", s.handleStaticOrSPA)

	return s.corsMiddleware(limitRequestBody(mux))
}

// limitRequestBody applies one shared bound to every management API body.
func limitRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		next.ServeHTTP(w, r)
	})
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
		"status":             status,
		"version":            s.version,
		"air_gapped":         false,
		"marketplace_online": s.marketplace.Online,
		"uptime_seconds":     uptime,
		"sqlite_status":      sqliteStatus,
		"adapter_errors":     adapterErrors,
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
	bufferFill := 0.0
	if s.pipe != nil {
		if stats.Normalized > 0 {
			unmappedRatio = float64(s.pipe.UnmappedCount(r.Context())) / float64(stats.Normalized)
		}
		depth, capacity := s.pipe.BufferUsage()
		if capacity > 0 {
			bufferFill = 100 * float64(depth) / float64(capacity)
		}
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
		"buffer_fill_pct":  bufferFill,
		"unmapped_ratio":   unmappedRatio,
		"delivery_pending": stats.DeliveryPending,
		"delivery_failed":  stats.DeliveryFailed,
		"memory_bytes":     m.Alloc,
		"goroutines":       runtime.NumGoroutine(),
		"uptime_seconds":   time.Since(s.startTime).Seconds(),
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
		Origin         string          `json:"origin,omitempty"`
		Pinned         bool            `json:"pinned,omitempty"`
		Digest         string          `json:"digest,omitempty"`
		Filename       string          `json:"filename,omitempty"`
		Modified       bool            `json:"modified,omitempty"`
	}
	manifest, manifestErr := packs.ReadManifest(s.packsDir)
	if manifestErr != nil {
		s.jsonError(w, http.StatusInternalServerError, "installed pack manifest unavailable: "+manifestErr.Error())
		return
	}

	summaries := make([]packSummary, 0, len(packList))
	for _, p := range packList {
		entry := manifest.Entries[p.Metadata.Name]
		modified := false
		if entry.Filename != "" {
			if data, e := os.ReadFile(filepath.Join(s.packsDir, entry.Filename)); e == nil {
				modified = marketplace.Digest(data) != entry.Digest
			} else {
				modified = true
			}
		}
		summaries = append(summaries, packSummary{
			Name:           p.Metadata.Name,
			Version:        p.Metadata.Version,
			Description:    p.Metadata.Description,
			Author:         p.Metadata.Author,
			SourceCategory: p.Spec.SourceCategory,
			Format:         p.Spec.Format,
			Match:          p.Spec.Match,
			FieldCount:     len(p.Spec.Fields),
			Origin:         entry.Origin, Pinned: entry.Pinned, Digest: entry.Digest, Filename: entry.Filename, Modified: modified,
		})
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"version": snap.Version(),
		"packs":   summaries,
	})
}

func (s *Server) handleMarketplacePacks(w http.ResponseWriter, r *http.Request) {
	catalog, err := s.marketplace.ReadCatalog()
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, "marketplace catalog unavailable: "+err.Error())
		return
	}
	items := catalog.SearchFiltered(r.URL.Query().Get("q"), r.URL.Query().Get("category"), r.URL.Query().Get("format"), r.URL.Query().Get("vendor"), r.URL.Query().Get("product"), r.URL.Query().Get("model"))
	limit := 100
	offset := 0
	if n, e := strconv.Atoi(r.URL.Query().Get("limit")); e == nil && n > 0 && n < limit {
		limit = n
	}
	if n, e := strconv.Atoi(r.URL.Query().Get("offset")); e == nil && n > 0 {
		offset = n
	}
	total := len(items)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	manifest, manifestErr := packs.ReadManifest(s.packsDir)
	if manifestErr != nil {
		s.jsonError(w, http.StatusInternalServerError, "installed pack manifest unavailable: "+manifestErr.Error())
		return
	}
	result := make([]map[string]any, 0, end-offset)
	for _, item := range items[offset:end] {
		value := map[string]any{"pack": item}
		if latest, ok := item.LatestCompatible(normalizeSemver(s.version), "worm.io/v1", ocsf.Version); ok {
			value["latest_compatible"] = latest
		}
		if installed, ok := manifest.Entries[item.Name]; ok {
			value["installed"] = installed
			data, e := os.ReadFile(filepath.Join(s.packsDir, installed.Filename))
			value["modified"] = e != nil || marketplace.Digest(data) != installed.Digest
		}
		result = append(result, value)
	}
	s.jsonResponse(w, http.StatusOK, map[string]any{"packs": result, "total": total, "limit": limit, "offset": offset, "generated": catalog.Generated})
}

func (s *Server) handleMarketplacePack(w http.ResponseWriter, r *http.Request) {
	catalog, err := s.marketplace.ReadCatalog()
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	name := r.PathValue("name")
	for _, p := range catalog.Packs {
		if p.Name == name {
			s.jsonResponse(w, http.StatusOK, p)
			return
		}
	}
	s.jsonError(w, http.StatusNotFound, "marketplace pack not found")
}

func (s *Server) handleMarketplaceStatus(w http.ResponseWriter, r *http.Request) {
	catalog, err := s.marketplace.ReadCatalog()
	source := "seed"
	if err != nil {
		source = "unavailable"
	}
	status := map[string]any{"online": s.marketplace.Online, "registry_url": s.marketplace.RegistryURL, "catalog_available": err == nil, "catalog_source": source}
	if err == nil {
		status["generated"] = catalog.Generated
		if _, cacheErr := os.Stat(filepath.Join(s.packsDir, ".marketplace", "catalog.json")); cacheErr == nil {
			status["catalog_source"] = "cached"
		}
	} else {
		status["error"] = err.Error()
	}
	var last map[string]any
	statusPath := filepath.Join(s.packsDir, ".marketplace", "status.json")
	if e := packs.RejectSymlinks(s.packsDir, statusPath); e != nil {
		s.jsonError(w, http.StatusInternalServerError, e.Error())
		return
	}
	if data, e := os.ReadFile(statusPath); e == nil {
		_ = json.Unmarshal(data, &last)
		for k, v := range last {
			status[k] = v
		}
	}
	s.jsonResponse(w, http.StatusOK, status)
}

func (s *Server) handleMarketplaceRefresh(w http.ResponseWriter, r *http.Request) {
	catalog, err := s.marketplace.Fetch(r.Context(), nil)
	if err != nil {
		_ = s.writeMarketplaceStatus(map[string]any{"last_refresh": time.Now().UTC().Format(time.RFC3339), "last_refresh_error": err.Error()})
		s.jsonError(w, http.StatusBadGateway, "marketplace refresh failed: "+err.Error())
		return
	}
	_ = s.writeMarketplaceStatus(map[string]any{"last_refresh": time.Now().UTC().Format(time.RFC3339), "last_refresh_error": ""})
	s.jsonResponse(w, http.StatusOK, map[string]any{"status": "refreshed", "generated": catalog.Generated, "packs": len(catalog.Packs)})
}

func (s *Server) writeMarketplaceStatus(status map[string]any) error {
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	return packs.AtomicWriteFile(s.packsDir, filepath.Join(s.packsDir, ".marketplace", "status.json"), data, 0600)
}

func (s *Server) handleMarketplaceFetch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	catalog, err := s.marketplace.ReadCatalog()
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var pack *marketplace.Pack
	for i := range catalog.Packs {
		if catalog.Packs[i].Name == req.Name {
			pack = &catalog.Packs[i]
			break
		}
	}
	if pack == nil {
		s.jsonError(w, http.StatusNotFound, "marketplace pack not found")
		return
	}
	release, ok := pack.Release(req.Version)
	if req.Version == "" || req.Version == "latest" {
		release, ok = pack.LatestCompatible(normalizeSemver(s.version), "worm.io/v1", ocsf.Version)
	}
	if !ok {
		s.jsonError(w, http.StatusNotFound, "marketplace release not found")
		return
	}
	cache := filepath.Join(s.packsDir, ".marketplace", "artifacts", pack.Name, release.Version, "pack.yaml")
	if data, e := os.ReadFile(cache); e == nil && release.VerifyArtifact(data) == nil {
		s.jsonResponse(w, http.StatusOK, map[string]any{"status": "cached", "name": pack.Name, "version": release.Version, "sha256": release.SHA256})
		return
	}
	data, err := s.marketplace.FetchArtifact(r.Context(), nil, release)
	if err != nil {
		s.jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	if err = packs.AtomicWriteFile(s.packsDir, cache, data, 0600); err != nil {
		s.jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]any{"status": "fetched", "name": pack.Name, "version": release.Version, "sha256": release.SHA256})
}

func (s *Server) handleMarketplaceInstall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name            string `json:"name"`
		Version         string `json:"version"`
		ExpectedVersion string `json:"expected_version"`
		ExpectedSHA256  string `json:"expected_sha256"`
		Pin             *bool  `json:"pin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	catalog, err := s.marketplace.ReadCatalog()
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var selected *marketplace.Pack
	for i := range catalog.Packs {
		if catalog.Packs[i].Name == req.Name {
			selected = &catalog.Packs[i]
			break
		}
	}
	if selected == nil {
		s.jsonError(w, http.StatusNotFound, "marketplace pack not found")
		return
	}
	var release *marketplace.Release
	if req.Version == "" || req.Version == "latest" {
		r, ok := selected.LatestCompatible(s.version, "worm.io/v1", ocsf.Version)
		if ok {
			release = &r
		}
	} else {
		for i := range selected.Releases {
			if strings.TrimPrefix(selected.Releases[i].Version, "v") == strings.TrimPrefix(req.Version, "v") && !selected.Releases[i].Withdrawn {
				release = &selected.Releases[i]
				break
			}
		}
	}
	if release == nil {
		s.jsonError(w, http.StatusNotFound, "compatible marketplace release not found")
		return
	}
	if release.PackAPI != "worm.io/v1" || release.OCSF != ocsf.Version || (release.MinWORM != "" && semver.Compare(normalizeSemver(s.version), normalizeSemver(release.MinWORM)) < 0) {
		s.jsonError(w, http.StatusConflict, "pack release is incompatible with this WORM version")
		return
	}
	if s.packManager == nil || s.packsDir == "" {
		s.jsonError(w, http.StatusServiceUnavailable, "pack manager is not configured")
		return
	}
	manifest, err := packs.ReadManifest(s.packsDir)
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, "read installed pack manifest: "+err.Error())
		return
	}
	filename := selected.Name + ".yaml"
	if installed, ok := manifest.Entries[selected.Name]; ok {
		filename = installed.Filename
	}
	activePath := filepath.Join(s.packsDir, filename)
	data, err := os.ReadFile(activePath)
	var expected *packs.PackFileExpectation
	if err == nil {
		expected = &packs.PackFileExpectation{Exists: true, Digest: marketplace.Digest(data)}
		if req.ExpectedVersion != "" || req.ExpectedSHA256 != "" {
			if (req.ExpectedVersion != "" && req.ExpectedVersion != manifest.Entries[selected.Name].Version) || (req.ExpectedSHA256 != "" && req.ExpectedSHA256 != expected.Digest) {
				s.jsonError(w, http.StatusConflict, "installed pack changed since it was inspected")
				return
			}
		}
		if expected.Digest == release.SHA256 {
			if active, ok := s.packManager.Active().GetPack(selected.Name); ok && active.Metadata.Version == release.Version {
				s.jsonResponse(w, http.StatusOK, map[string]any{"status": "already_installed", "name": selected.Name, "version": release.Version, "sha256": release.SHA256, "activated": true})
				return
			}
		}
	} else if os.IsNotExist(err) {
		expected = &packs.PackFileExpectation{Exists: false}
	} else {
		s.jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err == nil && expected.Digest != release.SHA256 {
		managed := false
		for _, old := range selected.Releases {
			if marketplace.Digest(data) == old.SHA256 {
				managed = true
				break
			}
		}
		if !managed && !manifestEntryMatches(manifest.Entries[selected.Name], filename, expected.Digest) {
			s.jsonError(w, http.StatusConflict, "a different local pack with this name is already installed")
			return
		}
	}
	cache := filepath.Join(s.packsDir, ".marketplace", "artifacts", selected.Name, release.Version, "pack.yaml")
	data, err = os.ReadFile(cache)
	if err == nil {
		err = release.VerifyArtifact(data)
	}
	if err != nil {
		data, err = s.marketplace.FetchArtifact(r.Context(), nil, *release)
		if err != nil {
			s.jsonError(w, http.StatusBadGateway, "verified pack download failed: "+err.Error())
			return
		}
		if e := packs.AtomicWriteFile(s.packsDir, cache, data, 0600); e != nil {
			s.jsonError(w, http.StatusInternalServerError, e.Error())
			return
		}
	}
	pack, err := packs.LoadPack(strings.NewReader(string(data)))
	if err != nil {
		s.jsonError(w, http.StatusBadRequest, "pack validation failed: "+err.Error())
		return
	}
	if pack.Metadata.Name != selected.Name || pack.Metadata.Version != release.Version {
		s.jsonError(w, http.StatusBadRequest, "artifact identity does not match signed catalog")
		return
	}
	pinned := req.Version != "" && req.Version != "latest"
	if req.Pin != nil {
		pinned = *req.Pin
	}
	if _, err = s.packManager.ApplyPackFileWithState(filename, data, expected, "marketplace", pinned); err != nil {
		s.jsonError(w, http.StatusConflict, "pack activation failed: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]any{"status": "installed", "name": selected.Name, "version": release.Version, "sha256": release.SHA256, "activated": true})
}

func manifestEntryMatches(entry packs.PackInstall, filename, digest string) bool {
	return entry.Filename == filename && entry.Digest == digest
}

func (s *Server) handleMarketplaceUpgrade(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		All  bool   `json:"all"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	manifest, err := packs.ReadManifest(s.packsDir)
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var names []string
	if req.All {
		for n, e := range manifest.Entries {
			if (e.Origin == "marketplace" || e.Origin == "bundled") && !e.Pinned {
				names = append(names, n)
			}
		}
		sort.Strings(names)
	} else if req.Name != "" {
		entry, ok := manifest.Entries[req.Name]
		if !ok || (entry.Origin != "marketplace" && entry.Origin != "bundled") {
			s.jsonError(w, http.StatusConflict, "pack is not marketplace-managed")
			return
		}
		names = []string{req.Name}
	} else {
		s.jsonError(w, http.StatusBadRequest, "name or all is required")
		return
	}
	catalog, err := s.marketplace.ReadCatalog()
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	results := make([]map[string]any, 0, len(names))
	failed := false
	for _, name := range names {
		installed := manifest.Entries[name]
		if installed.Pinned {
			results = append(results, map[string]any{"name": name, "status": "skipped", "reason": "pinned"})
			continue
		}
		var item *marketplace.Pack
		for i := range catalog.Packs {
			if catalog.Packs[i].Name == name {
				item = &catalog.Packs[i]
				break
			}
		}
		if item == nil {
			results = append(results, map[string]any{"name": name, "status": "error", "error": "pack is absent from catalog"})
			failed = true
			continue
		}
		major := semver.Major(normalizeSemver(installed.Version))
		target, ok := item.LatestCompatibleForMajor(s.version, "worm.io/v1", ocsf.Version, major)
		if !ok || semver.Compare(normalizeSemver(target.Version), normalizeSemver(installed.Version)) <= 0 {
			results = append(results, map[string]any{"name": name, "status": "current", "version": installed.Version})
			continue
		}
		pin := false
		body, _ := json.Marshal(map[string]any{"name": name, "version": target.Version, "expected_version": installed.Version, "expected_sha256": installed.Digest, "pin": pin})
		rr := httptest.NewRequest(http.MethodPost, "/api/v1/packs/install", bytes.NewReader(body))
		rr.Header.Set("Content-Type", "application/json")
		ww := httptest.NewRecorder()
		s.handleMarketplaceInstall(ww, rr)
		var result map[string]any
		_ = json.Unmarshal(ww.Body.Bytes(), &result)
		result["name"] = name
		result["status_code"] = ww.Code
		results = append(results, result)
		if ww.Code < 200 || ww.Code >= 300 {
			failed = true
		}
	}
	status := http.StatusOK
	if failed {
		status = http.StatusMultiStatus
	}
	s.jsonResponse(w, status, map[string]any{"results": results})
}

func normalizeSemver(v string) string {
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return "v999999.0.0"
	}
	return v
}

func (s *Server) handleMarketplaceRemove(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		s.jsonError(w, http.StatusBadRequest, "invalid pack name")
		return
	}
	if s.packManager == nil || s.packsDir == "" {
		s.jsonError(w, http.StatusServiceUnavailable, "pack manager is not configured")
		return
	}
	catalog, err := s.marketplace.ReadCatalog()
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var entry *marketplace.Pack
	for i := range catalog.Packs {
		if catalog.Packs[i].Name == name {
			entry = &catalog.Packs[i]
			break
		}
	}
	if entry == nil {
		s.jsonError(w, http.StatusConflict, "only catalog-managed packs can be removed through marketplace")
		return
	}
	manifest, err := packs.ReadManifest(s.packsDir)
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	installed, ok := manifest.Entries[name]
	if !ok || (installed.Origin != "marketplace" && installed.Origin != "bundled") {
		s.jsonError(w, http.StatusConflict, "pack is not managed by the marketplace")
		return
	}
	filename := installed.Filename
	data, err := os.ReadFile(filepath.Join(s.packsDir, filename))
	if err != nil {
		s.jsonError(w, http.StatusNotFound, "installed pack not found")
		return
	}
	if marketplace.Digest(data) != installed.Digest {
		s.jsonError(w, http.StatusConflict, "pack was modified locally; refusing marketplace removal")
		return
	}
	snap, err := s.packManager.RemovePackFileExpected(filename, &packs.PackFileExpectation{Exists: true, Digest: marketplace.Digest(data)})
	if err != nil {
		s.jsonError(w, http.StatusConflict, "pack removal failed: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]any{"status": "removed", "name": name, "remaining_packs": len(snap.ListPacks()), "warning": "logs without a matching parser pack may be quarantined"})
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

	var req struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	var err error
	if req.Name != "" {
		err = s.packManager.RollbackPack(req.Name)
	} else {
		err = s.packManager.Rollback()
	}
	if err != nil {
		s.jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status":  "rolled_back",
		"version": s.packManager.Active().Version(),
	})
}

func (s *Server) handleListConnections(w http.ResponseWriter, r *http.Request) {
	if s.connManager == nil {
		s.jsonResponse(w, http.StatusOK, map[string]any{"connections": []any{}})
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]any{
		"connections": s.connManager.ListSummaries(),
	})
}

func (s *Server) handleGetConnection(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if s.connManager == nil {
		s.jsonError(w, http.StatusNotFound, "connections manager not configured")
		return
	}
	raw, err := s.connManager.GetRawYAML(name)
	if err != nil {
		s.jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/x-yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (s *Server) handleValidateConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		YamlContent string `json:"yaml_content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, http.StatusBadRequest, "invalid json body: "+err.Error())
		return
	}
	conn, err := connections.LoadConnection(strings.NewReader(req.YamlContent))
	if err != nil {
		s.jsonResponse(w, http.StatusOK, map[string]any{
			"valid": false,
			"error": err.Error(),
		})
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]any{
		"valid":      true,
		"name":       conn.Metadata.Name,
		"version":    conn.Metadata.Version,
		"type":       conn.Spec.Type,
		"connection": conn,
	})
}

func (s *Server) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		YamlContent string `json:"yaml_content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, http.StatusBadRequest, "invalid json body: "+err.Error())
		return
	}
	conn, err := connections.LoadConnection(strings.NewReader(req.YamlContent))
	if err != nil {
		s.jsonResponse(w, http.StatusOK, map[string]any{
			"status":  "failed",
			"success": false,
			"target":  "spec validation",
			"error":   err.Error(),
			"message": err.Error(),
		})
		return
	}
	target := conn.Summary().Target
	if target == "" {
		target = conn.Metadata.Name
	}
	result, err := conn.TestConnectivityDetailed(r.Context())
	if err != nil {
		s.jsonResponse(w, http.StatusOK, map[string]any{"status": "failed", "success": false, "target": target, "level": "syntax_only", "error": err.Error(), "message": err.Error()})
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]any{"status": "ok", "success": true, "target": target, "level": result.Level, "message": result.Message})
}

func (s *Server) handleApplyConnection(w http.ResponseWriter, r *http.Request) {
	if s.connManager == nil {
		s.jsonError(w, http.StatusBadRequest, "connections manager not configured")
		return
	}
	var req struct {
		Filename    string `json:"filename"`
		YamlContent string `json:"yaml_content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, http.StatusBadRequest, "invalid json body: "+err.Error())
		return
	}
	if req.YamlContent == "" {
		s.jsonError(w, http.StatusBadRequest, "yaml_content is required")
		return
	}
	fn := req.Filename
	if fn == "" {
		parsed, pErr := connections.LoadConnection(strings.NewReader(req.YamlContent))
		if pErr != nil {
			s.jsonError(w, http.StatusBadRequest, "invalid connection yaml: "+pErr.Error())
			return
		}
		fn = parsed.Metadata.Name + ".yaml"
	}
	applied, err := s.connManager.ApplyFile(fn, []byte(req.YamlContent))
	if err != nil {
		s.jsonError(w, http.StatusBadRequest, "failed to apply connection: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status":     "applied",
		"name":       applied.Metadata.Name,
		"version":    applied.Metadata.Version,
		"connection": applied,
	})
}

func (s *Server) handleRollbackConnection(w http.ResponseWriter, r *http.Request) {
	if s.connManager == nil {
		s.jsonError(w, http.StatusBadRequest, "connections manager not configured")
		return
	}
	if err := s.connManager.Rollback(); err != nil {
		s.jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status": "rolled_back",
	})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	s.jsonResponse(w, http.StatusOK, struct {
		ConfigInfo
		Version string `json:"version"`
	}{ConfigInfo: s.cfgInfo, Version: s.version})
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
