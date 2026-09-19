package model

import (
	"fmt"
	"time"
)

// RawStatus represents the lifecycle state of a raw log record.
type RawStatus string

const (
	StatusAccepted    RawStatus = "accepted"
	StatusNormalized  RawStatus = "normalized"
	StatusQuarantined RawStatus = "quarantined"
	StatusPending     RawStatus = "pending"
)

// IngestedRecord represents a raw datagram or payload accepted at any boundary
// before it is durably committed to the RawStore.
type IngestedRecord struct {
	Transport  string    `json:"transport"`   // e.g. "syslog_udp", "syslog_tcp", "http_post", "file_spool", "stdin"
	SourceIP   string    `json:"source_ip"`   // remote sender IP or "local"
	SourcePort int       `json:"source_port"` // remote sender port or 0
	RawBytes   []byte    `json:"raw_bytes"`   // exact unmodified bytes received on the wire
	ReceivedAt time.Time `json:"received_at"` // ingress timestamp
}

// RawEvent represents a durably committed, cryptographically hashed raw log record.
type RawEvent struct {
	RawID      string    `json:"raw_id"`      // immutable monotonic ID e.g. "worm-raw-20260920-00000001"
	RawSHA256  string    `json:"raw_sha256"`  // hex-encoded SHA-256 digest
	ByteCount  int       `json:"byte_count"`  // exact size of raw payload in bytes
	Transport  string    `json:"transport"`   // transport protocol/channel
	SourceIP   string    `json:"source_ip"`   // remote source IP
	ReceivedAt time.Time `json:"received_at"` // exact reception timestamp
	Payload    []byte    `json:"payload"`     // verbatim wire bytes
	Status     RawStatus `json:"status"`      // accepted, normalized, quarantined
}

// ProcessingStep represents a single stage execution record in the event's audit trail.
type ProcessingStep struct {
	Stage     string    `json:"stage"`               // e.g. "raw_commit", "format_detect", "decode", "parser_match", "normalize"
	Timestamp time.Time `json:"timestamp"`           // timestamp when stage completed
	Result    string    `json:"result"`              // e.g. "ok", "syslog_rfc5424", "network-device-firewall@1.0.0"
	Error     string    `json:"error,omitempty"`     // error message if stage failed
}

// DecodedRecord represents an intermediate syntax tree produced by a format decoder
// (Syslog, JSON, CSV, CEF, Text) before semantic mapping.
type DecodedRecord struct {
	RawID          string         `json:"raw_id"`
	RecordOrdinal  int            `json:"record_ordinal"` // 0 for single, 0..N for batch child records
	Format         string         `json:"format"`         // e.g. "syslog_rfc5424", "json", "csv", "cef"
	Headers        map[string]any `json:"headers"`        // wire-level envelope metadata (e.g. syslog facility/severity)
	Fields         map[string]any `json:"fields"`         // key-value pairs decoded from payload
	RawPayload     []byte         `json:"raw_payload"`    // raw bytes of this specific record
}

// WormEnvelope contains the mandatory provenance, cryptographic lineage, and schema metadata.
type WormEnvelope struct {
	EventID           string           `json:"event_id"`           // unique event UUID e.g. "worm-evt-a1b2c3d4..."
	RawID             string           `json:"raw_id"`             // pointer to parent raw store entry
	RawSHA256         string           `json:"raw_sha256"`         // SHA-256 digest of original raw payload
	RawBytes          int              `json:"raw_bytes"`          // byte length of raw payload
	RecordOrdinal     int              `json:"record_ordinal"`     // ordinal in batch (0 if standalone)
	SourceCategory    string           `json:"source_category"`    // e.g. "network_device", "server", "endpoint"
	SourceID          string           `json:"source_id"`          // source host/app identifier
	ParserPack        string           `json:"parser_pack"`        // active parser pack name
	ParserVersion     string           `json:"parser_version"`     // active parser pack version
	CoreVersion       string           `json:"core_version"`       // WORM engine version e.g. "0.1.0"
	SchemaVersion     string           `json:"schema_version"`     // OCSF schema version e.g. "1.3.0"
	ReceivedTime      time.Time        `json:"received_time"`      // ingress timestamp
	Status            string           `json:"status"`             // "normalized" or "quarantined"
	Warnings          []string         `json:"warnings"`           // non-fatal conversion or mapping warnings
	ProcessingHistory []ProcessingStep `json:"processing_history"` // full chronological audit trail
}

// NormalizedEvent represents the final standardized event emitted to SIEM, data lakes, or ML pipelines.
type NormalizedEvent struct {
	Worm WormEnvelope   `json:"worm"` // WORM governance envelope
	OCSF map[string]any `json:"ocsf"` // canonical OCSF taxonomy fields
}

// QuarantineEntry represents an unparseable, unmatched, or schema-violating event parked in the dead-letter store.
type QuarantineEntry struct {
	QuarantineID   string     `json:"quarantine_id"`             // e.g. "worm-quar-20260920-00000001"
	RawID          string     `json:"raw_id"`                    // pointer to raw event
	RawSHA256      string     `json:"raw_sha256"`                // SHA-256 digest
	Stage          string     `json:"stage"`                     // stage where failure occurred
	Reason         string     `json:"reason"`                    // "unknown_source", "parse_error", "schema_violation", "conversion_error"
	ErrorDetails   string     `json:"error_details"`             // detailed error message or trace
	RawPreview     string     `json:"raw_preview"`               // hex/ASCII preview snippet of raw bytes
	CandidatePacks []string   `json:"candidate_packs,omitempty"` // candidate packs if ambiguous
	ReplayEligible bool       `json:"replay_eligible"`           // true if can be reprocessed via parser pack
	QuarantinedAt  time.Time  `json:"quarantined_at"`            // timestamp quarantined
	ReplayedAt     *time.Time `json:"replayed_at,omitempty"`     // timestamp of last replay attempt
}

// LossAccountingStats tracks the mathematical invariant:
// accepted = normalized + quarantined + pending
type LossAccountingStats struct {
	Accepted    int64 `json:"accepted"`
	Normalized  int64 `json:"normalized"`
	Quarantined int64 `json:"quarantined"`
	Pending     int64 `json:"pending"`
	Delivered   int64 `json:"delivered"`
}

// VerifyInvariant validates that no records are dropped or silently lost.
func (s LossAccountingStats) VerifyInvariant() (bool, string) {
	expected := s.Normalized + s.Quarantined + s.Pending
	if s.Accepted != expected {
		return false, fmt.Sprintf("loss accounting invariant VIOLATED: accepted (%d) != normalized (%d) + quarantined (%d) + pending (%d) [sum=%d]",
			s.Accepted, s.Normalized, s.Quarantined, s.Pending, expected)
	}
	return true, fmt.Sprintf("invariant satisfied: accepted (%d) == normalized (%d) + quarantined (%d) + pending (%d)",
		s.Accepted, s.Normalized, s.Quarantined, s.Pending)
}
