package rawstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
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

	if err := initSchema(db); err != nil {
		db.Close()
		return nil, err
	}

	return &RawStore{db: db}, nil
}

// CurrentSchemaVersion tracks the active database schema migration version.
const CurrentSchemaVersion = 4

func initSchema(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version;").Scan(&version); err != nil {
		return fmt.Errorf("failed to read user_version: %w", err)
	}

	if version < 1 {
		schema := `
		CREATE TABLE IF NOT EXISTS raw_events (
			raw_id TEXT PRIMARY KEY,
			raw_sha256 TEXT NOT NULL,
			byte_count INTEGER NOT NULL,
			transport TEXT NOT NULL,
			source_ip TEXT NOT NULL,
			source_port INTEGER NOT NULL DEFAULT 0,
			received_at TEXT NOT NULL,
			payload BLOB NOT NULL,
			status TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_raw_events_sha ON raw_events(raw_sha256);
		CREATE INDEX IF NOT EXISTS idx_raw_events_status ON raw_events(status);

		CREATE TABLE IF NOT EXISTS quarantine (
			quarantine_id TEXT PRIMARY KEY,
			raw_id TEXT NOT NULL,
			record_ordinal INTEGER NOT NULL DEFAULT 0,
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

		CREATE TABLE IF NOT EXISTS normalized_events (
			event_id TEXT PRIMARY KEY,
			raw_id TEXT NOT NULL,
			record_ordinal INTEGER NOT NULL DEFAULT 0,
			source_category TEXT NOT NULL,
			source_id TEXT NOT NULL,
			severity_id TEXT,
			activity_name TEXT,
			message TEXT,
			event_time INTEGER NOT NULL,
			received_time TEXT NOT NULL,
			event_json TEXT NOT NULL,
			event_sha256 TEXT NOT NULL DEFAULT '',
			FOREIGN KEY (raw_id) REFERENCES raw_events(raw_id)
		);
		CREATE INDEX IF NOT EXISTS idx_norm_cat ON normalized_events(source_category);
		CREATE INDEX IF NOT EXISTS idx_norm_time ON normalized_events(event_time);
		`
		if _, err := db.Exec(schema); err != nil {
			return fmt.Errorf("failed to initialize raw store schema v2: %w", err)
		}
		version = 2
	}

	if version < 2 {
		migrations := []string{
			"ALTER TABLE raw_events ADD COLUMN source_port INTEGER NOT NULL DEFAULT 0;",
			"ALTER TABLE quarantine ADD COLUMN record_ordinal INTEGER NOT NULL DEFAULT 0;",
			"ALTER TABLE normalized_events ADD COLUMN record_ordinal INTEGER NOT NULL DEFAULT 0;",
		}
		for _, m := range migrations {
			_, _ = db.Exec(m)
		}
		version = 2
	}

	if version < 3 {
		_, _ = db.Exec("ALTER TABLE raw_events ADD COLUMN metadata_json TEXT NOT NULL DEFAULT '{}';")
		version = 3
	}

	if version < 4 {
		// A process can restart after a child was quarantined but before its parent
		// raw status was finalized. Keep that recovery replay idempotent.
		if _, err := db.Exec(`DELETE FROM quarantine WHERE rowid NOT IN (
			SELECT MIN(rowid) FROM quarantine GROUP BY raw_id, record_ordinal, stage, reason
		);`); err != nil {
			return fmt.Errorf("failed to deduplicate quarantine rows: %w", err)
		}
		if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_quarantine_child
			ON quarantine(raw_id, record_ordinal, stage, reason);`); err != nil {
			return fmt.Errorf("failed to create quarantine child index: %w", err)
		}
		version = 4
	}

	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d;", CurrentSchemaVersion)); err != nil {
		return fmt.Errorf("failed to set user_version: %w", err)
	}

	return nil
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
		SourcePort: rec.SourcePort,
		Metadata:   rec.Metadata,
		ReceivedAt: rec.ReceivedAt,
		Payload:    rec.RawBytes,
		Status:     model.StatusAccepted,
	}

	metadataJSON, err := json.Marshal(event.Metadata)
	if err != nil {
		return nil, fmt.Errorf("failed to encode receive metadata: %w", err)
	}
	query := `
	INSERT INTO raw_events (raw_id, raw_sha256, byte_count, transport, source_ip, source_port, metadata_json, received_at, payload, status)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
	`
	_, err = s.db.ExecContext(ctx, query,
		event.RawID,
		event.RawSHA256,
		event.ByteCount,
		event.Transport,
		event.SourceIP,
		event.SourcePort,
		string(metadataJSON),
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
	SELECT raw_id, raw_sha256, byte_count, transport, source_ip, source_port, metadata_json, received_at, payload, status
	FROM raw_events WHERE raw_id = ?;
	`
	var (
		evt          model.RawEvent
		receivedAt   string
		statusStr    string
		metadataJSON string
	)

	err := s.db.QueryRowContext(ctx, query, rawID).Scan(
		&evt.RawID,
		&evt.RawSHA256,
		&evt.ByteCount,
		&evt.Transport,
		&evt.SourceIP,
		&evt.SourcePort,
		&metadataJSON,
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
	if err := json.Unmarshal([]byte(metadataJSON), &evt.Metadata); err != nil {
		return nil, fmt.Errorf("invalid receive metadata for %s: %w", rawID, err)
	}

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
	INSERT INTO quarantine (quarantine_id, raw_id, record_ordinal, raw_sha256, stage, reason, error_details, raw_preview, candidate_packs, replay_eligible, quarantined_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(raw_id, record_ordinal, stage, reason) DO UPDATE SET
		error_details=excluded.error_details,
		raw_preview=excluded.raw_preview,
		candidate_packs=excluded.candidate_packs,
		replay_eligible=excluded.replay_eligible;
	`
	_, err = tx.ExecContext(ctx, qQuery,
		entry.QuarantineID,
		entry.RawID,
		entry.RecordOrdinal,
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
	SELECT quarantine_id, raw_id, record_ordinal, raw_sha256, stage, reason, error_details, raw_preview, candidate_packs, replay_eligible, quarantined_at, replayed_at
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
			&e.RecordOrdinal,
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
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate quarantine entries: %w", err)
	}
	return entries, nil
}

// GetQuarantineByID retrieves a single quarantined entry by its quarantine ID.
func (s *RawStore) GetQuarantineByID(ctx context.Context, qID string) (*model.QuarantineEntry, error) {
	query := `
	SELECT quarantine_id, raw_id, record_ordinal, raw_sha256, stage, reason, error_details, raw_preview, candidate_packs, replay_eligible, quarantined_at, replayed_at
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
		&e.RecordOrdinal,
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

// GetReplayPendingIDs returns quarantine IDs eligible for reprocessing.
// If includeAlreadyReplayed is false, returns only records where replayed_at IS NULL.
func (s *RawStore) GetReplayPendingIDs(ctx context.Context, includeAlreadyReplayed bool) ([]string, error) {
	query := `SELECT quarantine_id FROM quarantine WHERE replay_eligible = 1 AND replayed_at IS NULL ORDER BY quarantined_at ASC;`
	if includeAlreadyReplayed {
		query = `SELECT quarantine_id FROM quarantine WHERE replay_eligible = 1 ORDER BY quarantined_at ASC;`
	}

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query replayable quarantine IDs: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate replayable quarantine IDs: %w", err)
	}
	return ids, nil
}

// DeleteQuarantine removes a record from the quarantine table once it has been successfully replayed.
func (s *RawStore) DeleteQuarantine(ctx context.Context, qID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	query := `DELETE FROM quarantine WHERE quarantine_id = ?;`
	_, err := s.db.ExecContext(ctx, query, qID)
	return err
}

// CountRaw returns the total number of raw events in storage.
func (s *RawStore) CountRaw(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM raw_events;").Scan(&count)
	return count, err
}

// CountNormalized returns the total number of normalized events in storage.
func (s *RawStore) CountNormalized(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM normalized_events;").Scan(&count)
	return count, err
}

// CountQuarantine returns the total number of unresolved quarantine records in storage.
func (s *RawStore) CountQuarantine(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM quarantine;").Scan(&count)
	return count, err
}

func (s *RawStore) CountUnmapped(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM normalized_events WHERE EXISTS (SELECT 1 FROM json_each(json_extract(event_json,'$.ocsf.unmapped')))`).Scan(&n)
	return n, err
}

// StoreNormalized inserts or updates a normalized event in the SQLite database.
func (s *RawStore) StoreNormalized(ctx context.Context, event *model.NormalizedEvent) error {
	return s.StoreNormalizedWithOutbox(ctx, event, nil)
}

// StoreNormalizedWithOutbox atomically stores the projection and one idempotent delivery row per destination.
func (s *RawStore) StoreNormalizedWithOutbox(ctx context.Context, event *model.NormalizedEvent, destinations []string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal normalized event: %w", err)
	}
	eventTime := event.Worm.ReceivedTime.UnixMilli()
	switch v := event.OCSF["time"].(type) {
	case int64:
		eventTime = v
	case float64:
		eventTime = int64(v)
	case int:
		eventTime = int64(v)
	}
	severity, activity, msg := "", "", ""
	if v, ok := event.OCSF["severity_id"]; ok && v != nil {
		severity = fmt.Sprint(v)
	}
	if v, ok := event.OCSF["activity_name"].(string); ok {
		activity = v
	}
	if v, ok := event.OCSF["message"].(string); ok {
		msg = v
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existing string
	if err = tx.QueryRowContext(ctx, "SELECT event_id FROM normalized_events WHERE raw_id=? AND record_ordinal=? LIMIT 1", event.Worm.RawID, event.Worm.RecordOrdinal).Scan(&existing); err == nil && existing != "" {
		event.Worm.EventID = existing
		data, _ = json.Marshal(event)
	}
	sum := sha256.Sum256(data)
	_, err = tx.ExecContext(ctx, `INSERT INTO normalized_events(event_id,raw_id,record_ordinal,source_category,source_id,severity_id,activity_name,message,event_time,received_time,event_json,event_sha256) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(event_id) DO UPDATE SET event_json=excluded.event_json,event_sha256=excluded.event_sha256,message=excluded.message`, event.Worm.EventID, event.Worm.RawID, event.Worm.RecordOrdinal, event.Worm.SourceCategory, event.Worm.SourceID, severity, activity, msg, eventTime, event.Worm.ReceivedTime.Format(time.RFC3339Nano), string(data), hex.EncodeToString(sum[:]))
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, dest := range destinations {
		if _, err = tx.ExecContext(ctx, `INSERT INTO outbox(event_id,destination,payload,attempts,next_retry_at,status,created_at) VALUES(?,?,?,0,?,'pending',?) ON CONFLICT(event_id,destination) DO UPDATE SET payload=excluded.payload WHERE outbox.status!='delivered'`, event.Worm.EventID, dest, data, now, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *RawStore) DB() *sql.DB { return s.db }
func (s *RawStore) CountRawStatus(ctx context.Context, status model.RawStatus) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM raw_events WHERE status=?", string(status)).Scan(&n)
	return n, err
}
func (s *RawStore) ListRawByStatus(ctx context.Context, status model.RawStatus) ([]*model.RawEvent, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT raw_id FROM raw_events WHERE status=? ORDER BY received_at", string(status))
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	// RawStore intentionally uses one SQLite connection. Close the ID cursor
	// before Retrieve opens another query or restart recovery will deadlock.
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]*model.RawEvent, 0, len(ids))
	for _, id := range ids {
		e, err := s.Retrieve(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// ListNormalized retrieves paginated normalized events with optional category and severity filtering.
func (s *RawStore) ListNormalized(ctx context.Context, category, severity, search string, limit, offset int) ([]*model.NormalizedEvent, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var conditions []string
	var args []any

	if category != "" && category != "all" {
		conditions = append(conditions, "source_category = ?")
		args = append(args, category)
	}
	if severity != "" && severity != "all" {
		conditions = append(conditions, "severity_id = ?")
		args = append(args, severity)
	}
	if search != "" {
		conditions = append(conditions, "(message LIKE ? OR source_id LIKE ? OR event_id LIKE ?)")
		pattern := "%" + search + "%"
		args = append(args, pattern, pattern, pattern)
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM normalized_events %s;", whereClause)
	var total int
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count normalized events: %w", err)
	}

	dataQuery := fmt.Sprintf(`
		SELECT event_json
		FROM normalized_events
		%s
		ORDER BY event_time DESC
		LIMIT ? OFFSET ?;
	`, whereClause)

	dataArgs := append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, dataQuery, dataArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list normalized events: %w", err)
	}
	defer rows.Close()

	var events []*model.NormalizedEvent
	for rows.Next() {
		var jsonStr string
		if err := rows.Scan(&jsonStr); err != nil {
			return nil, 0, err
		}
		var evt model.NormalizedEvent
		if err := json.Unmarshal([]byte(jsonStr), &evt); err != nil {
			continue
		}
		events = append(events, &evt)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("failed to iterate normalized events: %w", err)
	}

	return events, total, nil
}

// GetNormalizedByID retrieves a single normalized event by its unique event ID.
func (s *RawStore) GetNormalizedByID(ctx context.Context, eventID string) (*model.NormalizedEvent, error) {
	query := `SELECT event_json FROM normalized_events WHERE event_id = ?;`
	var jsonStr string
	err := s.db.QueryRowContext(ctx, query, eventID).Scan(&jsonStr)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("event not found: %s", eventID)
		}
		return nil, fmt.Errorf("failed to query event %s: %w", eventID, err)
	}

	var evt model.NormalizedEvent
	if err := json.Unmarshal([]byte(jsonStr), &evt); err != nil {
		return nil, fmt.Errorf("failed to unmarshal event json: %w", err)
	}
	return &evt, nil
}

// NormalizedVerificationReport holds the cryptographic verification report for a normalized event.
type NormalizedVerificationReport struct {
	EventID         string `json:"event_id"`
	RawID           string `json:"raw_id"`
	StoredHash      string `json:"stored_hash"`
	ComputedHash    string `json:"computed_hash"`
	HashMatches     bool   `json:"hash_matches"`
	StoredMessage   string `json:"stored_message"`
	ExpectedMessage string `json:"expected_message"`
	MessageMatches  bool   `json:"message_matches"`
	RawValid        bool   `json:"raw_valid"`
	Passed          bool   `json:"passed"`
	Status          string `json:"status"`
	FailureReason   string `json:"failure_reason,omitempty"`
}

// VerifyNormalized verifies the cryptographic integrity of a normalized event:
// 1. Checks that sha256(event_json) matches event_sha256.
// 2. Checks that the column message matches the message declared in event_json.
// 3. Checks that the underlying raw wire datagram in raw_events is untampered.
func (s *RawStore) VerifyNormalized(ctx context.Context, eventID string) (*NormalizedVerificationReport, error) {
	query := `SELECT event_id, raw_id, message, event_json, event_sha256 FROM normalized_events WHERE event_id = ?;`
	var (
		evtID      string
		rawID      string
		colMsg     string
		eventJSON  string
		storedHash string
	)
	err := s.db.QueryRowContext(ctx, query, eventID).Scan(&evtID, &rawID, &colMsg, &eventJSON, &storedHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("normalized event not found: %s", eventID)
		}
		return nil, fmt.Errorf("query error for event %s: %w", eventID, err)
	}

	computed := sha256.Sum256([]byte(eventJSON))
	computedHex := hex.EncodeToString(computed[:])

	hashMatches := (storedHash != "" && computedHex == storedHash)

	// Extract message from event_json to check column integrity
	var parsed model.NormalizedEvent
	expectedMsg := ""
	if err := json.Unmarshal([]byte(eventJSON), &parsed); err == nil {
		if m, ok := parsed.OCSF["message"].(string); ok {
			expectedMsg = m
		}
	}
	messageMatches := (colMsg == expectedMsg)

	// Verify raw event and cross-check that message exists in raw wire payload
	rawMatches, _, rawErr := s.Verify(ctx, rawID)
	rawValid := (rawErr == nil && rawMatches)
	rawEvt, _ := s.Retrieve(ctx, rawID)

	rawContentMatches := true
	if rawEvt != nil && colMsg != "" {
		if !strings.Contains(string(rawEvt.Payload), colMsg) {
			rawContentMatches = false
		}
	}

	passed := hashMatches && messageMatches && rawValid && rawContentMatches

	report := &NormalizedVerificationReport{
		EventID:         evtID,
		RawID:           rawID,
		StoredHash:      storedHash,
		ComputedHash:    computedHex,
		HashMatches:     hashMatches,
		StoredMessage:   colMsg,
		ExpectedMessage: expectedMsg,
		MessageMatches:  messageMatches,
		RawValid:        rawValid,
		Passed:          passed,
		Status:          "PASSED",
	}

	if !passed {
		report.Status = "TAMPER DETECTED"
		var reasons []string
		if !hashMatches {
			reasons = append(reasons, "event_json payload was modified (hash mismatch)")
		}
		if !messageMatches {
			reasons = append(reasons, fmt.Sprintf("message column (%q) does not match event_json payload (%q)", colMsg, expectedMsg))
		}
		if !rawContentMatches {
			reasons = append(reasons, fmt.Sprintf("message (%q) does not exist in original raw wire log (tampered projection)", colMsg))
		}
		if !rawValid {
			reasons = append(reasons, "parent raw event wire bytes were modified or corrupted")
		}
		report.FailureReason = strings.Join(reasons, "; ")
	}

	return report, nil
}

// ListQuarantinePaged retrieves a paginated list of dead-letter quarantine entries.
func (s *RawStore) ListQuarantinePaged(ctx context.Context, limit, offset int) ([]model.QuarantineEntry, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM quarantine;").Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `
	SELECT quarantine_id, raw_id, record_ordinal, raw_sha256, stage, reason, error_details, raw_preview, candidate_packs, replay_eligible, quarantined_at, replayed_at
	FROM quarantine
	ORDER BY quarantined_at DESC
	LIMIT ? OFFSET ?;
	`
	rows, err := s.db.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, 0, err
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
			&e.RecordOrdinal,
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
			return nil, 0, err
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
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("failed to iterate quarantine entries: %w", err)
	}

	return entries, total, nil
}

// ListSources returns an aggregated inventory of active data sources.
func (s *RawStore) ListSources(ctx context.Context) ([]map[string]any, error) {
	query := `
	SELECT 
		source_category,
		source_id,
		COUNT(*) as event_count,
		MAX(received_time) as last_seen
	FROM normalized_events
	GROUP BY source_category, source_id
	ORDER BY event_count DESC;
	`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sources []map[string]any
	for rows.Next() {
		var (
			category string
			sourceID string
			count    int64
			lastSeen string
		)
		if err := rows.Scan(&category, &sourceID, &count, &lastSeen); err != nil {
			return nil, err
		}
		sources = append(sources, map[string]any{
			"category":  category,
			"source_id": sourceID,
			"events":    count,
			"last_seen": lastSeen,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate sources: %w", err)
	}
	return sources, nil
}

// Ping verifies that the underlying SQLite database handle is healthy.
func (s *RawStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// Close closes the underlying SQLite database handle.
func (s *RawStore) Close() error {
	return s.db.Close()
}
