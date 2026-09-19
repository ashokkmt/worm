package ingest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

		// Read and split records (supports single JSON object or newline-delimited NDJSON)
		var count int
		scanner := bufio.NewScanner(r.Body)
		buf := make([]byte, 64*1024)
		scanner.Buffer(buf, 1024*1024)

		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				continue
			}

			payload := make([]byte, len(line))
			copy(payload, line)

			rec := model.IngestedRecord{
				Transport:  "http-post",
				SourceIP:   clientIP,
				SourcePort: clientPort,
				RawBytes:   payload,
				ReceivedAt: time.Now().UTC(),
			}

			select {
			case out <- rec:
				count++
			case <-ctx.Done():
				http.Error(w, `{"error": "server shutting down"}`, http.StatusServiceUnavailable)
				return
			}
		}

		if err := scanner.Err(); err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "failed to read payload: %v"}`, err), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":   "accepted",
			"accepted": count,
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
