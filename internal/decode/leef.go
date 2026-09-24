package decode

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"worm/internal/model"
)

// LEEFDecoder decodes IBM Log Event Extended Format (LEEF) 1.0 and 2.0 events.
type LEEFDecoder struct{}

// NewLEEFDecoder creates a new LEEF format decoder.
func NewLEEFDecoder() *LEEFDecoder {
	return &LEEFDecoder{}
}

// Name returns the canonical format name "leef".
func (d *LEEFDecoder) Name() string {
	return "leef"
}

// Detect checks if the payload appears to be LEEF formatted.
func (d *LEEFDecoder) Detect(raw []byte) float64 {
	s := strings.TrimSpace(string(raw))
	if strings.HasPrefix(s, "LEEF:1.0|") || strings.HasPrefix(s, "LEEF:2.0|") {
		return 0.99
	}
	if strings.HasPrefix(s, "LEEF:") {
		return 0.95
	}
	if strings.Contains(s, "LEEF:1.0|") || strings.Contains(s, "LEEF:2.0|") {
		return 0.85
	}
	return 0.0
}

// Decode parses a raw LEEF event into a DecodedRecord.
func (d *LEEFDecoder) Decode(raw []byte) ([]*model.DecodedRecord, error) {
	if len(raw) > MaxPayloadBytes {
		return nil, fmt.Errorf("LEEF payload %d bytes exceeds maximum limit of %d", len(raw), MaxPayloadBytes)
	}

	s := strings.TrimSpace(string(raw))
	if len(s) == 0 {
		return nil, errors.New("empty LEEF payload")
	}

	leefIdx := strings.Index(s, "LEEF:")
	if leefIdx == -1 {
		return nil, errors.New("missing LEEF: prefix")
	}
	leefPayload := s[leefIdx:]

	parts, remaining, err := splitLEEFHeader(leefPayload)
	if err != nil {
		return nil, fmt.Errorf("invalid LEEF header: %w", err)
	}

	// parts[0]: LEEF:1.0 or LEEF:2.0
	version := strings.TrimPrefix(parts[0], "LEEF:")
	if version != "1.0" && version != "2.0" {
		return nil, fmt.Errorf("unsupported LEEF version: %s (expected 1.0 or 2.0)", version)
	}

	vendor := parts[1]
	product := parts[2]
	prodVersion := parts[3]
	eventID := parts[4]

	delimiter := "\t"
	attributesStr := remaining

	if version == "2.0" {
		if len(parts) >= 6 {
			delimSpec := parts[5]
			d, err := parseLEEFDelimiter(delimSpec)
			if err != nil {
				return nil, fmt.Errorf("invalid LEEF 2.0 delimiter %q: %w", delimSpec, err)
			}
			delimiter = d
		}
	}

	headers := map[string]any{
		"leef_version":    version,
		"vendor":          vendor,
		"product":         product,
		"product_version": prodVersion,
		"event_id":        eventID,
	}

	fields := make(map[string]any)
	fields["vendor"] = vendor
	fields["product"] = product
	fields["product_version"] = prodVersion
	fields["event_id"] = eventID

	// Parse attributes according to delimiter
	attrs := parseLEEFAttributes(attributesStr, delimiter)
	if len(attrs) > MaxFields {
		return nil, fmt.Errorf("field count limit exceeded: %d > %d", len(attrs), MaxFields)
	}

	for k, v := range attrs {
		fields[k] = v
	}

	rec := &model.DecodedRecord{
		RecordOrdinal: 0,
		Format:        "leef",
		Headers:       headers,
		Fields:        fields,
		RawPayload:    raw,
	}

	return []*model.DecodedRecord{rec}, nil
}

// splitLEEFHeader splits header components:
// For 1.0: 5 fields (Version|Vendor|Product|Version|EventID|)
// For 2.0: up to 6 fields (Version|Vendor|Product|Version|EventID|Delimiter|)
func splitLEEFHeader(s string) ([]string, string, error) {
	var parts []string
	var cur strings.Builder
	escaped := false

	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escaped {
			if ch != '|' && ch != '\\' {
				cur.WriteByte('\\')
			}
			cur.WriteByte(ch)
			escaped = false
			continue
		}

		if ch == '\\' {
			escaped = true
			continue
		}

		if ch == '|' {
			parts = append(parts, cur.String())
			cur.Reset()

			// Check if we reached the end of header
			if len(parts) >= 1 && strings.HasPrefix(parts[0], "LEEF:1.0") {
				if len(parts) == 5 {
					return parts, s[i+1:], nil
				}
			} else if len(parts) >= 1 && strings.HasPrefix(parts[0], "LEEF:2.0") {
				if len(parts) == 6 {
					return parts, s[i+1:], nil
				}
			}
			continue
		}

		cur.WriteByte(ch)
	}

	// In LEEF 2.0, if only 5 parts exist and no trailing delimiter pipe was used:
	if len(parts) == 5 && strings.HasPrefix(parts[0], "LEEF:2.0") {
		return parts, cur.String(), nil
	}

	return nil, "", fmt.Errorf("incomplete LEEF header: expected at least 5 pipe-delimited fields, found %d", len(parts))
}

func parseLEEFDelimiter(delimSpec string) (string, error) {
	if delimSpec == "" {
		return "\t", nil
	}

	// Hexadecimal delimiter, e.g. x5E, 0x5E, x09, 0x09
	lower := strings.ToLower(delimSpec)
	if strings.HasPrefix(lower, "0x") || strings.HasPrefix(lower, "x") {
		hexPart := strings.TrimPrefix(strings.TrimPrefix(lower, "0x"), "x")
		if len(hexPart) == 1 {
			hexPart = "0" + hexPart
		}
		b, err := hex.DecodeString(hexPart)
		if err != nil || len(b) != 1 {
			// Fallback try strconv
			val, sErr := strconv.ParseUint(hexPart, 16, 8)
			if sErr != nil {
				return "", fmt.Errorf("malformed hex delimiter %q", delimSpec)
			}
			return string([]byte{byte(val)}), nil
		}
		return string(b), nil
	}

	// Single character literal delimiter
	if len(delimSpec) == 1 {
		return delimSpec, nil
	}

	// If escaped e.g. \t
	if delimSpec == "\\t" {
		return "\t", nil
	}

	return delimSpec[:1], nil
}

// parseLEEFAttributes splits attributes by delimiter and extracts key=value pairs.
// Values can contain spaces, equal signs, and escaped characters.
func parseLEEFAttributes(attrStr, delimiter string) map[string]any {
	result := make(map[string]any)
	if len(attrStr) == 0 {
		return result
	}

	// Split by delimiter respecting escaped delimiter
	tokens := splitWithEscapes(attrStr, delimiter)

	for _, token := range tokens {
		trimmed := strings.TrimSpace(token)
		if len(trimmed) == 0 {
			continue
		}

		eqIdx := strings.IndexByte(trimmed, '=')
		if eqIdx <= 0 {
			continue
		}

		key := strings.TrimSpace(trimmed[:eqIdx])
		if len(key) == 0 {
			continue
		}

		rawVal := trimmed[eqIdx+1:]
		val := unescapeLEEFValue(rawVal, delimiter)

		// Deterministic duplicate-key preservation
		if existing, exists := result[key]; exists {
			switch sl := existing.(type) {
			case []any:
				result[key] = append(sl, val)
			default:
				result[key] = []any{sl, val}
			}
		} else {
			result[key] = val
		}
	}

	return result
}

func splitWithEscapes(s, delimiter string) []string {
	if delimiter == "" {
		delimiter = "\t"
	}

	delimByte := delimiter[0]
	var tokens []string
	var cur strings.Builder
	escaped := false

	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escaped {
			if ch != delimByte && ch != '\\' {
				cur.WriteByte('\\')
			}
			cur.WriteByte(ch)
			escaped = false
			continue
		}

		if ch == '\\' {
			escaped = true
			continue
		}

		if ch == delimByte {
			tokens = append(tokens, cur.String())
			cur.Reset()
			continue
		}

		cur.WriteByte(ch)
	}

	if cur.Len() > 0 || len(tokens) > 0 {
		tokens = append(tokens, cur.String())
	}

	return tokens
}

func unescapeLEEFValue(s, delimiter string) string {
	if !strings.Contains(s, "\\") {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	escaped := false

	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escaped {
			switch ch {
			case '\\':
				b.WriteByte('\\')
			case 't':
				b.WriteByte('\t')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			default:
				if len(delimiter) > 0 && ch == delimiter[0] {
					b.WriteByte(ch)
				} else {
					b.WriteByte('\\')
					b.WriteByte(ch)
				}
			}
			escaped = false
			continue
		}

		if ch == '\\' {
			escaped = true
			continue
		}

		b.WriteByte(ch)
	}

	if escaped {
		b.WriteByte('\\')
	}

	return b.String()
}
