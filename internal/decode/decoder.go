package decode

import "worm/internal/model"

// Decoder defines the common contract for all wire and syntax format decoders.
// Decoders understand only syntax (structure, delimiters, framing).
// They do NOT assign domain semantics (that belongs to Parser Packs).
type Decoder interface {
	// Name returns the canonical identifier of the format decoder (e.g. "syslog", "json", "csv").
	Name() string

	// Detect inspects raw bytes non-destructively and returns a confidence score between 0.0 and 1.0.
	Detect(raw []byte) float64

	// Decode parses raw bytes into one or more intermediate abstract syntax trees (DecodedRecord).
	// Batch formats (JSON arrays, NDJSON, CSV) split deterministically into multiple child records.
	Decode(raw []byte) ([]*model.DecodedRecord, error)
}
