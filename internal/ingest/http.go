package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"worm/internal/model"
)

// HTTPListener exposes an HTTP ingestion endpoint (POST /api/v1/ingest).
type HTTPListener struct {
	addr     string
	apiKey   string
	server   *http.Server
	listener net.Listener
	mu       sync.Mutex
}

// NewHTTPListener creates a new HTTP listener on the given address (e.g. ":8080").
func NewHTTPListener(addr string, apiKey string) *HTTPListener {
	if addr == "" {
		addr = ":8080"
	}
	return &HTTPListener{
		addr:   addr,
		apiKey: apiKey,
	}
}

func (h *HTTPListener) Name() string {
	return "http-post"
}

// Addr returns the resolved network address for the HTTP listener.
func (h *HTTPListener) Addr() net.Addr {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.listener != nil {
		return h.listener.Addr()
	}
	return nil
}

func (h *HTTPListener) Start(ctx context.Context, out chan<- model.IngestedRecord) error {
	mux := http.NewServeMux()

	// Health check endpoint
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"time":   time.Now().UTC(),
		})
	})

	// Ingestion endpoint
	mux.HandleFunc("/api/v1/ingest", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error": "method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		// Optional API key authorization
		if h.apiKey != "" {
			reqKey := r.Header.Get("X-WORM-Key")
			if reqKey == "" {
				authHeader := r.Header.Get("Authorization")
				if strings.HasPrefix(authHeader, "Bearer ") {
					reqKey = strings.TrimPrefix(authHeader, "Bearer ")
				}
			}

			if reqKey != h.apiKey {
				http.Error(w, `{"error": "unauthorized"}`, http.StatusUnauthorized)
				return
			}
		}

		// Enforce maximum body size (16MB)
		r.Body = http.MaxBytesReader(w, r.Body, 16*1024*1024)
		defer r.Body.Close()

		// Determine client IP & port
		clientIP, clientPort := h.extractRemoteAddr(r)

		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "failed to read payload: %v"}`, err), http.StatusBadRequest)
			return
		}
		if len(bodyBytes) == 0 {
			http.Error(w, `{"error": "empty body"}`, http.StatusBadRequest)
			return
		}

		cType := strings.ToLower(r.Header.Get("Content-Type"))
		isExplicitNDJSON := strings.Contains(cType, "ndjson") || strings.Contains(cType, "x-ndjson")

		var count int
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = fmt.Sprintf("worm-http-%d", time.Now().UnixNano())
		}
		submit := func(payload []byte) error {
			ack := make(chan error, 1)
			rec := model.IngestedRecord{Transport: "http-post", SourceIP: clientIP, SourcePort: clientPort, RawBytes: payload, ReceivedAt: time.Now().UTC(), Ack: ack, Metadata: model.ReceiveMetadata{Listener: h.addr, RequestID: requestID}}
			select {
			case out <- rec:
			case <-ctx.Done():
				return ctx.Err()
			case <-r.Context().Done():
				return r.Context().Err()
			}
			timer := time.NewTimer(15 * time.Second)
			defer timer.Stop()
			select {
			case err := <-ack:
				return err
			case <-timer.C:
				return fmt.Errorf("raw durability acknowledgement timed out")
			case <-ctx.Done():
				return ctx.Err()
			case <-r.Context().Done():
				return r.Context().Err()
			}
		}

		if isExplicitNDJSON {
			for _, line := range bytes.Split(bodyBytes, []byte("\n")) {
				if len(bytes.TrimSpace(line)) == 0 {
					continue
				}
				if err := submit(append([]byte(nil), line...)); err != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusServiceUnavailable)
					_ = json.NewEncoder(w).Encode(map[string]any{"status": "retry", "accepted": count, "rejected": 1, "error": err.Error(), "request_id": requestID})
					return
				}
				count++
			}
		} else {
			if err := submit(bodyBytes); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "retry", "accepted": 0, "rejected": 1, "error": err.Error(), "request_id": requestID})
				return
			}
			count = 1
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":     "accepted",
			"accepted":   count,
			"request_id": requestID,
		})
	})

	ln, err := net.Listen("tcp", h.addr)
	if err != nil {
		return fmt.Errorf("failed to listen on HTTP address %s: %w", h.addr, err)
	}

	h.mu.Lock()
	h.listener = ln
	h.server = &http.Server{
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	h.mu.Unlock()

	go func() {
		<-ctx.Done()
		_ = h.Stop()
	}()

	if err := h.server.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}

	return nil
}

func (h *HTTPListener) Stop() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.server != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return h.server.Shutdown(shutdownCtx)
	}
	return nil
}

func (h *HTTPListener) extractRemoteAddr(r *http.Request) (string, int) {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0]), 0
		}
	}

	host, portStr, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr, 0
	}
	port, _ := strconv.Atoi(portStr)
	return host, port
}
