package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
	"worm/internal/model"
)

// PollerConfig defines parameters for polling a remote HTTP/cloud endpoint for logs.
type PollerConfig struct {
	Name           string        `json:"name"`
	EndpointURL    string        `json:"endpoint_url"`
	Interval       time.Duration `json:"interval"`
	BatchSize      int           `json:"batch_size"`
	WatermarkPath  string        `json:"watermark_path"`
	AuthHeader     string        `json:"auth_header,omitempty"`
	AuthToken      string        `json:"auth_token,omitempty"`
	RequestTimeout time.Duration `json:"request_timeout"`
}

// CloudPoller periodically fetches logs from a remote REST/cloud log endpoint
// with bounded batches and persists its checkpoint watermark only after raw commit durability.
type CloudPoller struct {
	cfg       PollerConfig
	client    *http.Client
	mu        sync.Mutex
	watermark string
	closed    bool
}

// NewCloudPoller creates a new CloudPoller instance.
func NewCloudPoller(cfg PollerConfig) *CloudPoller {
	if cfg.Interval <= 0 {
		cfg.Interval = 10 * time.Second
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 15 * time.Second
	}
	return &CloudPoller{
		cfg: cfg,
		client: &http.Client{
			Timeout: cfg.RequestTimeout,
		},
	}
}

func (p *CloudPoller) Name() string {
	if p.cfg.Name != "" {
		return "cloud-pull-" + p.cfg.Name
	}
	return "cloud-pull"
}

// LoadWatermark reads the persisted watermark from disk.
func (p *CloudPoller) LoadWatermark() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cfg.WatermarkPath == "" {
		return p.watermark, nil
	}

	data, err := os.ReadFile(p.cfg.WatermarkPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	p.watermark = string(data)
	return p.watermark, nil
}

// SaveWatermark persists the latest successfully committed watermark to disk.
func (p *CloudPoller) SaveWatermark(wm string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.watermark = wm
	if p.cfg.WatermarkPath == "" {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(p.cfg.WatermarkPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(p.cfg.WatermarkPath, []byte(wm), 0600)
}

func (p *CloudPoller) Start(ctx context.Context, out chan<- model.IngestedRecord) error {
	_, _ = p.LoadWatermark()

	ticker := time.NewTicker(p.cfg.Interval)
	defer ticker.Stop()

	// Initial poll immediately
	if err := p.pollOnce(ctx, out); err != nil && ctx.Err() == nil {
		// Log or track initial poll failure, keep running
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			p.mu.Lock()
			closed := p.closed
			p.mu.Unlock()
			if closed {
				return nil
			}

			if err := p.pollOnce(ctx, out); err != nil && ctx.Err() == nil {
				// Retry on next tick
			}
		}
	}
}

type pollBatchResponse struct {
	NextWatermark string            `json:"next_watermark"`
	Records       []json.RawMessage `json:"records"`
}

func (p *CloudPoller) pollOnce(ctx context.Context, out chan<- model.IngestedRecord) error {
	p.mu.Lock()
	currentWM := p.watermark
	p.mu.Unlock()

	reqURL := fmt.Sprintf("%s?limit=%d", p.cfg.EndpointURL, p.cfg.BatchSize)
	if currentWM != "" {
		reqURL += fmt.Sprintf("&since=%s", currentWM)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("failed to construct poll request: %w", err)
	}

	if p.cfg.AuthHeader != "" && p.cfg.AuthToken != "" {
		req.Header.Set(p.cfg.AuthHeader, p.cfg.AuthToken)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("poll request error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("poll returned HTTP %d", resp.StatusCode)
	}

	var batch pollBatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&batch); err != nil {
		return fmt.Errorf("failed to decode poll response: %w", err)
	}

	if len(batch.Records) == 0 {
		return nil
	}

	// Process records sequentially, asserting durable commit before advancing watermark
	for _, recBytes := range batch.Records {
		ackChan := make(chan error, 1)
		rec := model.IngestedRecord{
			RawBytes:   recBytes,
			Transport:  "cloud_pull",
			SourceIP:   p.cfg.EndpointURL,
			ReceivedAt: time.Now().UTC(),
			Ack:        ackChan,
			Metadata: model.ReceiveMetadata{
				ConnectionID: p.cfg.Name,
				Watermark:    currentWM,
			},
		}

		select {
		case out <- rec:
			select {
			case commitErr := <-ackChan:
				if commitErr != nil {
					return fmt.Errorf("raw commit failed for pulled record: %w", commitErr)
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// Advance watermark only after all records in the batch are durably committed
	if batch.NextWatermark != "" {
		if err := p.SaveWatermark(batch.NextWatermark); err != nil {
			return fmt.Errorf("failed to persist watermark: %w", err)
		}
	}

	return nil
}

func (p *CloudPoller) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return nil
}
