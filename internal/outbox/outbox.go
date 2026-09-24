package outbox

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// Entry represents an outbox record in the durable delivery queue.
type Entry struct {
	ID             int64      `json:"id"`
	EventID        string     `json:"event_id"`
	Destination    string     `json:"destination"`
	Payload        []byte     `json:"payload"`
	Attempts       int        `json:"attempts"`
	NextRetryAt    time.Time  `json:"next_retry_at"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
	ErrorDetails   string     `json:"error_details,omitempty"`
	Status         string     `json:"status"` // "pending", "delivered", "failed"
	CreatedAt      time.Time  `json:"created_at"`
}

// Outbox provides a durable SQLite-backed retry queue for reliable at-least-once delivery.
type Outbox struct {
	db *sql.DB
	mu sync.RWMutex
}

// NewOutbox initializes the outbox tables in the specified database.
func NewOutbox(db *sql.DB) (*Outbox, error) {
	if db == nil {
		return nil, fmt.Errorf("db must not be nil")
	}

	query := `
	CREATE TABLE IF NOT EXISTS outbox (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_id TEXT NOT NULL,
		destination TEXT NOT NULL,
		payload BLOB NOT NULL,
		attempts INTEGER NOT NULL DEFAULT 0,
		next_retry_at TIMESTAMP NOT NULL,
		acknowledged_at TIMESTAMP,
		error_details TEXT,
		status TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_outbox_pending ON outbox (status, next_retry_at);
	CREATE INDEX IF NOT EXISTS idx_outbox_event ON outbox (event_id);
	`
	if _, err := db.Exec(query); err != nil {
		return nil, fmt.Errorf("failed to initialize outbox schema: %w", err)
	}

	return &Outbox{db: db}, nil
}

// Enqueue adds an event payload to the durable outbox queue.
func (o *Outbox) Enqueue(ctx context.Context, eventID, destination string, payload []byte) (int64, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	now := time.Now().UTC()
	query := `
	INSERT INTO outbox (event_id, destination, payload, attempts, next_retry_at, status, created_at)
	VALUES (?, ?, ?, 0, ?, 'pending', ?)
	`
	res, err := o.db.ExecContext(ctx, query, eventID, destination, payload, now, now)
	if err != nil {
		return 0, fmt.Errorf("failed to enqueue outbox record: %w", err)
	}
	return res.LastInsertId()
}

// FetchPending retrieves up to limit records that are ready for delivery (status='pending' and next_retry_at <= now).
func (o *Outbox) FetchPending(ctx context.Context, limit int) ([]Entry, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()

	if limit <= 0 {
		limit = 100
	}

	now := time.Now().UTC()
	query := `
	SELECT id, event_id, destination, payload, attempts, next_retry_at, acknowledged_at, error_details, status, created_at
	FROM outbox
	WHERE status = 'pending' AND next_retry_at <= ?
	ORDER BY id ASC
	LIMIT ?
	`
	rows, err := o.db.QueryContext(ctx, query, now, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query pending outbox records: %w", err)
	}
	defer rows.Close()

	var entries []Entry
	for rows.Next() {
		var e Entry
		var ackAt sql.NullTime
		var errDet sql.NullString

		if err := rows.Scan(
			&e.ID, &e.EventID, &e.Destination, &e.Payload,
			&e.Attempts, &e.NextRetryAt, &ackAt, &errDet,
			&e.Status, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan outbox record: %w", err)
		}

		if ackAt.Valid {
			e.AcknowledgedAt = &ackAt.Time
		}
		if errDet.Valid {
			e.ErrorDetails = errDet.String
		}
		entries = append(entries, e)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("outbox rows error: %w", err)
	}

	return entries, nil
}

// MarkDelivered records successful acknowledgement of delivery.
func (o *Outbox) MarkDelivered(ctx context.Context, id int64) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	now := time.Now().UTC()
	query := `
	UPDATE outbox
	SET status = 'delivered', acknowledged_at = ?, error_details = NULL
	WHERE id = ?
	`
	_, err := o.db.ExecContext(ctx, query, now, id)
	return err
}

// MarkFailed increments attempts and sets next retry timestamp with exponential backoff.
func (o *Outbox) MarkFailed(ctx context.Context, id int64, errDetails string, backoff time.Duration, maxAttempts int) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	now := time.Now().UTC()
	nextRetry := now.Add(backoff)

	query := `
	UPDATE outbox
	SET attempts = attempts + 1,
	    next_retry_at = ?,
	    error_details = ?,
	    status = CASE WHEN attempts + 1 >= ? THEN 'failed' ELSE 'pending' END
	WHERE id = ?
	`
	_, err := o.db.ExecContext(ctx, query, nextRetry, errDetails, maxAttempts, id)
	return err
}

// CountByStatus returns the count of records for a given status ('pending', 'delivered', 'failed').
func (o *Outbox) CountByStatus(ctx context.Context, status string) (int64, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()

	var count int64
	query := `SELECT COUNT(*) FROM outbox WHERE status = ?`
	err := o.db.QueryRowContext(ctx, query, status).Scan(&count)
	return count, err
}
