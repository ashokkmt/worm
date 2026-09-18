package rawstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"worm/internal/model"

	_ "modernc.org/sqlite"
)

// RawStore provides durable, cryptographically verified storage for raw byte streams.
type RawStore struct {
	db      *sql.DB
	writeMu sync.Mutex
	counter atomic.Uint64
}

// New opens an SQLite database, creates required tables and enables WAL mode.
func New(dbPath string) (*RawStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open raw store sqlite db: %w", err)
	}

	// Single connection serialization for SQLite concurrency safety
	db.SetMaxOpenConns(1)

	// Set pragmas for crash resilience and concurrency
	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA synchronous=NORMAL;",
		"PRAGMA busy_timeout=5000;",
		"PRAGMA foreign_keys=ON;",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to set pragma %q: %w", p, err)
		}
	}

	schema := `
	CREATE TABLE IF NOT EXISTS raw_events (
		raw_id TEXT PRIMARY KEY,
		raw_sha256 TEXT NOT NULL,
		byte_count INTEGER NOT NULL,
		transport TEXT NOT NULL,
		source_ip TEXT NOT NULL,
		received_at TEXT NOT NULL,
		payload BLOB NOT NULL,
		status TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_raw_events_sha ON raw_events(raw_sha256);
	CREATE INDEX IF NOT EXISTS idx_raw_events_status ON raw_events(status);

	CREATE TABLE IF NOT EXISTS quarantine (
		quarantine_id TEXT PRIMARY KEY,
		raw_id TEXT NOT NULL,
		raw_sha256 TEXT NOT NULL,
		stage TEXT NOT NULL,
		reason TEXT NOT NULL,
		error_details TEXT,
		raw_preview TEXT NOT NULL,
		candidate_packs TEXT,
		replay_eligible INTEGER NOT NULL,
		quarantined_at TEXT NOT NULL,
		replayed_at TEXT,
		FOREIGN KEY (raw_id) REFERENCES raw_events(raw_id)
	);
	CREATE INDEX IF NOT EXISTS idx_quarantine_raw ON quarantine(raw_id);
	CREATE INDEX IF NOT EXISTS idx_quarantine_reason ON quarantine(reason);
	`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize raw store schema: %w", err)
	}

	return &RawStore{db: db}, nil
}

// Store commits verbatim raw bytes, calculates SHA-256 digest, and assigns an immutable raw ID.
func (s *RawStore) Store(ctx context.Context, rec model.IngestedRecord) (*model.RawEvent, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if rec.ReceivedAt.IsZero() {
		rec.ReceivedAt = time.Now().UTC()
	}

	hash := sha256.Sum256(rec.RawBytes)
	shaHex := hex.EncodeToString(hash[:])

	// Format monotonic, predictable raw ID: worm-raw-YYYYMMDD-<seq>-<rand>
	seq := s.counter.Add(1)
	var randBytes [4]byte
	_, _ = rand.Read(randBytes[:])
	rawID := fmt.Sprintf("worm-raw-%s-%06d-%x", rec.ReceivedAt.Format("20060102"), seq, randBytes)

	event := &model.RawEvent{
		RawID:      rawID,
		RawSHA256:  shaHex,
		ByteCount:  len(rec.RawBytes),
		Transport:  rec.Transport,
		SourceIP:   rec.SourceIP,
		ReceivedAt: rec.ReceivedAt,
		Payload:    rec.RawBytes,
		Status:     model.StatusAccepted,
	}

	query := `
	INSERT INTO raw_events (raw_id, raw_sha256, byte_count, transport, source_ip, received_at, payload, status)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?);
	`
	_, err := s.db.ExecContext(ctx, query,
		event.RawID,
		event.RawSHA256,
		event.ByteCount,
		event.Transport,
		event.SourceIP,
		event.ReceivedAt.Format(time.RFC3339Nano),
		event.Payload,
		string(event.Status),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to persist raw record %s: %w", rawID, err)
	}

	return event, nil
}

// Retrieve returns the raw record and exact bytes by raw ID.
func (s *RawStore) Retrieve(ctx context.Context, rawID string) (*model.RawEvent, error) {
	query := `
	SELECT raw_id, raw_sha256, byte_count, transport, source_ip, received_at, payload, status
	FROM raw_events WHERE raw_id = ?;
	`
	var (
		evt        model.RawEvent
		receivedAt string
		statusStr  string
	)

	err := s.db.QueryRowContext(ctx, query, rawID).Scan(
		&evt.RawID,
		&evt.RawSHA256,
		&evt.ByteCount,
		&evt.Transport,
		&evt.SourceIP,
		&receivedAt,
		&evt.Payload,
		&statusStr,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("raw event not found: %s", rawID)
		}
		return nil, fmt.Errorf("failed to retrieve raw event %s: %w", rawID, err)
	}

	t, err := time.Parse(time.RFC3339Nano, receivedAt)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, receivedAt)
	}
	evt.ReceivedAt = t
	evt.Status = model.RawStatus(statusStr)

	return &evt, nil
}

// Verify recalculates the SHA-256 digest of stored bytes and verifies cryptographic integrity.
func (s *RawStore) Verify(ctx context.Context, rawID string) (bool, string, error) {
	evt, err := s.Retrieve(ctx, rawID)
	if err != nil {
		return false, "", err
	}

	computed := sha256.Sum256(evt.Payload)
	computedHex := hex.EncodeToString(computed[:])

	matches := (computedHex == evt.RawSHA256)
	return matches, computedHex, nil
}

// Quarantine persists an unparseable or schema-violating event in the quarantine table
// and updates the raw event status to "quarantined".
func (s *RawStore) Quarantine(ctx context.Context, entry model.QuarantineEntry) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if entry.QuarantineID == "" {
		var randBytes [4]byte
		_, _ = rand.Read(randBytes[:])
		entry.QuarantineID = fmt.Sprintf("worm-quar-%s-%x", time.Now().Format("20060102"), randBytes)
	}
	if entry.QuarantinedAt.IsZero() {
		entry.QuarantinedAt = time.Now().UTC()
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin quarantine tx: %w", err)
	}
	defer tx.Rollback()

	replayVal := 0
	if entry.ReplayEligible {
		replayVal = 1
	}

	candidateStr := strings.Join(entry.CandidatePacks, ",")
	qQuery := `
	INSERT INTO quarantine (quarantine_id, raw_id, raw_sha256, stage, reason, error_details, raw_preview, candidate_packs, replay_eligible, quarantined_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
	`
	_, err = tx.ExecContext(ctx, qQuery,
		entry.QuarantineID,
		entry.RawID,
		entry.RawSHA256,
		entry.Stage,
		entry.Reason,
		entry.ErrorDetails,
		entry.RawPreview,
		candidateStr,
		replayVal,
		entry.QuarantinedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("failed to insert quarantine entry: %w", err)
	}

	uQuery := `UPDATE raw_events SET status = ? WHERE raw_id = ?;`
	if _, err := tx.ExecContext(ctx, uQuery, string(model.StatusQuarantined), entry.RawID); err != nil {
		return fmt.Errorf("failed to update raw event status to quarantined: %w", err)
	}

	return tx.Commit()
}

// UpdateStatus updates the lifecycle state of a committed raw record.
func (s *RawStore) UpdateStatus(ctx context.Context, rawID string, status model.RawStatus) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	query := `UPDATE raw_events SET status = ? WHERE raw_id = ?;`
	res, err := s.db.ExecContext(ctx, query, string(status), rawID)
	if err != nil {
		return fmt.Errorf("failed to update status of %s: %w", rawID, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("raw event not found for status update: %s", rawID)
	}
	return nil
}

// GetQuarantined retrieves paginated quarantined records.
func (s *RawStore) GetQuarantined(ctx context.Context, limit, offset int) ([]model.QuarantineEntry, error) {
	query := `
	SELECT quarantine_id, raw_id, raw_sha256, stage, reason, error_details, raw_preview, candidate_packs, replay_eligible, quarantined_at, replayed_at
	FROM quarantine
	ORDER BY quarantined_at DESC
	LIMIT ? OFFSET ?;
	`
	rows, err := s.db.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to query quarantine entries: %w", err)
	}
	defer rows.Close()

	var entries []model.QuarantineEntry
	for rows.Next() {
		var (
			e            model.QuarantineEntry
			candidateStr sql.NullString
			replayInt    int
			qAtStr       string
			rAtStr       sql.NullString
		)
		err := rows.Scan(
			&e.QuarantineID,
			&e.RawID,
			&e.RawSHA256,
			&e.Stage,
			&e.Reason,
			&e.ErrorDetails,
			&e.RawPreview,
			&candidateStr,
			&replayInt,
			&qAtStr,
			&rAtStr,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan quarantine entry: %w", err)
		}

		if candidateStr.Valid && candidateStr.String != "" {
			e.CandidatePacks = strings.Split(candidateStr.String, ",")
		}
		e.ReplayEligible = (replayInt == 1)
		t, _ := time.Parse(time.RFC3339Nano, qAtStr)
		e.QuarantinedAt = t
		if rAtStr.Valid {
			rt, _ := time.Parse(time.RFC3339Nano, rAtStr.String)
			e.ReplayedAt = &rt
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// GetQuarantineByID retrieves a single quarantined entry by its quarantine ID.
func (s *RawStore) GetQuarantineByID(ctx context.Context, qID string) (*model.QuarantineEntry, error) {
	query := `
	SELECT quarantine_id, raw_id, raw_sha256, stage, reason, error_details, raw_preview, candidate_packs, replay_eligible, quarantined_at, replayed_at
	FROM quarantine
	WHERE quarantine_id = ?;
	`
	var (
		e            model.QuarantineEntry
		candidateStr sql.NullString
		replayInt    int
		qAtStr       string
		rAtStr       sql.NullString
	)
	err := s.db.QueryRowContext(ctx, query, qID).Scan(
		&e.QuarantineID,
		&e.RawID,
		&e.RawSHA256,
		&e.Stage,
		&e.Reason,
		&e.ErrorDetails,
		&e.RawPreview,
		&candidateStr,
		&replayInt,
		&qAtStr,
		&rAtStr,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("quarantine entry not found: %s", qID)
		}
		return nil, fmt.Errorf("failed to query quarantine entry %s: %w", qID, err)
	}

	if candidateStr.Valid && candidateStr.String != "" {
		e.CandidatePacks = strings.Split(candidateStr.String, ",")
	}
	e.ReplayEligible = (replayInt == 1)
	t, _ := time.Parse(time.RFC3339Nano, qAtStr)
	e.QuarantinedAt = t
	if rAtStr.Valid {
		rt, _ := time.Parse(time.RFC3339Nano, rAtStr.String)
		e.ReplayedAt = &rt
	}
	return &e, nil
}

// MarkReplayed updates the replayed_at timestamp of a quarantined record.
func (s *RawStore) MarkReplayed(ctx context.Context, qID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	nowStr := time.Now().UTC().Format(time.RFC3339Nano)
	query := `UPDATE quarantine SET replayed_at = ? WHERE quarantine_id = ?;`
	res, err := s.db.ExecContext(ctx, query, nowStr, qID)
	if err != nil {
		return fmt.Errorf("failed to mark quarantine %s as replayed: %w", qID, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("quarantine entry not found for replay update: %s", qID)
	}
	return nil
}

// Close closes the underlying SQLite database handle.
func (s *RawStore) Close() error {
	return s.db.Close()
}
