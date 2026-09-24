package output

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
	"worm/internal/model"
)

// HTTPOutputConfig configures delivery to a generic HTTP SIEM endpoint.
type HTTPOutputConfig struct {
	Name          string        `json:"name"`
	URL           string        `json:"url"`
	Format        string        `json:"format"` // "ndjson" or "json"
	BatchEvents   int           `json:"batch_events"`
	FlushInterval time.Duration `json:"flush_interval"`
	Timeout       time.Duration `json:"timeout"`
	AuthHeader    string        `json:"auth_header,omitempty"`
	AuthToken     string        `json:"auth_token,omitempty"`
	MaxRetries    int           `json:"max_retries"`
	MinBackoff    time.Duration `json:"min_backoff"`
	MaxBackoff    time.Duration `json:"max_backoff"`
}

// HTTPSIEMSink buffers normalized events and delivers them to an HTTP SIEM endpoint.
type HTTPSIEMSink struct {
	cfg        HTTPOutputConfig
	client     *http.Client
	mu         sync.Mutex
	buffer     []*model.NormalizedEvent
	flushTimer *time.Timer
	closed     bool
}

// NewHTTPSIEMSink creates a new HTTP SIEM delivery sink.
func NewHTTPSIEMSink(cfg HTTPOutputConfig) *HTTPSIEMSink {
	if cfg.BatchEvents <= 0 {
		cfg.BatchEvents = 100
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 1 * time.Second
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 3
	}
	if cfg.MinBackoff <= 0 {
		cfg.MinBackoff = 100 * time.Millisecond
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 10 * time.Second
	}
	if cfg.Format == "" {
		cfg.Format = "ndjson"
	}

	sink := &HTTPSIEMSink{
		cfg: cfg,
		client: &http.Client{
			Timeout: cfg.Timeout,
		},
		buffer: make([]*model.NormalizedEvent, 0, cfg.BatchEvents),
	}

	return sink
}

// Emit enqueues an event for delivery and flushes when batch limit is reached.
func (s *HTTPSIEMSink) Emit(ctx context.Context, event *model.NormalizedEvent) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("http sink is closed")
	}

	s.buffer = append(s.buffer, event)
	readyToFlush := len(s.buffer) >= s.cfg.BatchEvents
	s.mu.Unlock()

	if readyToFlush {
		return s.Flush(ctx)
	}

	return nil
}

// Flush sends all buffered events to the configured HTTP SIEM endpoint.
func (s *HTTPSIEMSink) Flush(ctx context.Context) error {
	s.mu.Lock()
	if len(s.buffer) == 0 {
		s.mu.Unlock()
		return nil
	}

	batch := s.buffer
	s.buffer = make([]*model.NormalizedEvent, 0, s.cfg.BatchEvents)
	s.mu.Unlock()

	payload, contentType, err := s.serializeBatch(batch)
	if err != nil {
		return fmt.Errorf("failed to serialize batch: %w", err)
	}

	return s.sendWithRetry(ctx, payload, contentType)
}

func (s *HTTPSIEMSink) serializeBatch(batch []*model.NormalizedEvent) ([]byte, string, error) {
	if s.cfg.Format == "json" {
		data, err := json.Marshal(batch)
		return data, "application/json", err
	}

	// Default NDJSON
	var buf bytes.Buffer
	for _, evt := range batch {
		line, err := json.Marshal(evt)
		if err != nil {
			return nil, "", err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), "application/x-ndjson", nil
}

func (s *HTTPSIEMSink) sendWithRetry(ctx context.Context, payload []byte, contentType string) error {
	var lastErr error
	backoff := s.cfg.MinBackoff

	attempts := s.cfg.MaxRetries + 1
	if attempts <= 0 {
		attempts = 1
	}

	for attempt := 0; attempt < attempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.URL, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("failed to create http request: %w", err)
		}

		req.Header.Set("Content-Type", contentType)
		if s.cfg.AuthHeader != "" && s.cfg.AuthToken != "" {
			req.Header.Set(s.cfg.AuthHeader, s.cfg.AuthToken)
		}

		resp, err := s.client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("http siem responded with status %d", resp.StatusCode)
		} else {
			lastErr = err
		}

		// Wait backoff if more attempts remain
		if attempt < attempts-1 {
			select {
			case <-time.After(backoff):
				backoff *= 2
				if backoff > s.cfg.MaxBackoff {
					backoff = s.cfg.MaxBackoff
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	return fmt.Errorf("failed to deliver batch after %d attempts: %w", attempts, lastErr)
}

// Close flushes remaining events and shuts down the sink.
func (s *HTTPSIEMSink) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return s.Flush(context.Background())
}
