package decode

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
	"worm/internal/model"
)

// clfRegex matches NCSA Common and Combined Log Format (CLF).
// Example: 10.0.1.50 - john [20/Sep/2026:10:15:01 +0530] "GET /dashboard HTTP/1.1" 200 15234 "https://..." "Mozilla/..."
var clfRegex = regexp.MustCompile(`^(\S+)\s+(\S+)\s+(\S+)\s+\[([^\]]+)\]\s+"([A-Z]+)\s+([^\s]+)\s+([^"]+)"\s+(\d{3})\s+(\S+)(?:\s+"([^"]*)"\s+"([^"]*)")?`)

// genericKVRegex matches simple key=value pairs in unstructured text lines.
var genericKVRegex = regexp.MustCompile(`(?:^|\s+)([a-zA-Z0-9_\.\-]+)=([^\s]+)`)

// TextDecoder implements the Decoder interface for declarative text, NCSA Combined Log Format,
// and unstructured log lines.
type TextDecoder struct{}

// NewTextDecoder returns a new TextDecoder instance.
func NewTextDecoder() *TextDecoder {
	return &TextDecoder{}
}

// Name returns the canonical format name.
func (d *TextDecoder) Name() string {
	return "text"
}

// Detect checks if the payload is text formatted.
func (d *TextDecoder) Detect(raw []byte) float64 {
	if len(raw) == 0 {
		return 0.0
	}
	if !utf8.Valid(raw) {
		return 0.0
	}

	s := strings.TrimSpace(string(raw))
	if len(s) == 0 {
		return 0.0
	}

	// High confidence if it matches Combined Log Format
	if clfRegex.MatchString(s) {
		return 0.85
	}

	// High confidence if it contains multiple structured key=value pairs
	if kvs := genericKVRegex.FindAllStringSubmatch(s, -1); len(kvs) >= 2 {
		return 0.65
	}

	// Arbitrary or unstructured text without recognized pattern should not exceed 0.50 threshold
	return 0.10
}

// Decode parses a raw text payload into a DecodedRecord.
func (d *TextDecoder) Decode(raw []byte) ([]*model.DecodedRecord, error) {
	if len(raw) > MaxPayloadBytes {
		return nil, fmt.Errorf("text payload %d bytes exceeds maximum limit of %d", len(raw), MaxPayloadBytes)
	}

	if len(raw) == 0 {
		return nil, errors.New("empty text payload")
	}

	s := strings.TrimSpace(string(raw))
	if len(s) == 0 {
		return nil, errors.New("empty text string")
	}
	if len(s) > MaxTextLine {
		return nil, fmt.Errorf("text line length %d exceeds maximum limit of %d", len(s), MaxTextLine)
	}

	fields := make(map[string]any)
	headers := make(map[string]any)
	format := "text"

	// Check for Combined Log Format
	if match := clfRegex.FindStringSubmatch(s); match != nil {
		format = "text_clf"
		fields["client_ip"] = match[1]
		fields["ident"] = match[2]
		fields["auth_user"] = match[3]
		fields["timestamp"] = match[4]
		fields["http_method"] = match[5]
		fields["url_path"] = match[6]
		fields["http_version"] = match[7]

		if code, err := strconv.Atoi(match[8]); err == nil {
			fields["status_code"] = code
		} else {
			fields["status_code"] = match[8]
		}

		if match[9] != "-" {
			if bytesVal, err := strconv.ParseInt(match[9], 10, 64); err == nil {
				fields["response_bytes"] = bytesVal
			} else {
				fields["response_bytes"] = match[9]
			}
		} else {
			fields["response_bytes"] = int64(0)
		}

		if len(match) > 10 && match[10] != "" {
			fields["referrer"] = match[10]
		}
		if len(match) > 11 && match[11] != "" {
			fields["user_agent"] = match[11]
		}

		// Save the full line as message
		fields["message"] = s
	} else {
		// Generic unstructured text line
		fields["message"] = s

		// Extract any embedded key=value tokens
		matches := genericKVRegex.FindAllStringSubmatch(s, -1)
		for _, m := range matches {
			if len(m) == 3 {
				fields[m[1]] = m[2]
			}
		}
	}

	rec := &model.DecodedRecord{
		RecordOrdinal: 0,
		Format:        format,
		Headers:       headers,
		Fields:        fields,
		RawPayload:    raw,
	}

	return []*model.DecodedRecord{rec}, nil
}
