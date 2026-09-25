package decode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"worm/internal/model"
)

// JSONDecoder parses single JSON objects, bounded JSON arrays, and NDJSON streams.
type JSONDecoder struct{}

func NewJSONDecoder() *JSONDecoder {
	return &JSONDecoder{}
}

func (j *JSONDecoder) Name() string {
	return "json"
}

// Detect checks if the byte slice begins with JSON object, array, or NDJSON framing.
func (j *JSONDecoder) Detect(raw []byte) float64 {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return 0.0
	}

	if trimmed[0] == '{' {
		// Valid single object or first line of NDJSON
		return 0.95
	}
	if trimmed[0] == '[' {
		if len(trimmed) > 1 && trimmed[len(trimmed)-1] == ']' {
			inner := bytes.TrimSpace(trimmed[1:])
			if len(inner) > 0 {
				first := inner[0]
				if first == '{' || first == '"' || first == '[' || first == ']' ||
					(first >= '0' && first <= '9') || first == '-' ||
					first == 't' || first == 'f' || first == 'n' {
					return 0.90
				}
			}
		}
		return 0.0
	}

	return 0.0
}

// Decode splits JSON documents into one or more DecodedRecord child events.
func (j *JSONDecoder) Decode(raw []byte) ([]*model.DecodedRecord, error) {
	if len(raw) > MaxPayloadBytes {
		return nil, fmt.Errorf("JSON payload %d bytes exceeds maximum limit of %d", len(raw), MaxPayloadBytes)
	}

	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty JSON payload")
	}

	if err := validateJSONDepth(trimmed); err != nil {
		return nil, err
	}

	// 1. JSON Array Handling: [...]
	if trimmed[0] == '[' {
		var rawArray []json.RawMessage
		if err := json.Unmarshal(trimmed, &rawArray); err != nil {
			return nil, fmt.Errorf("malformed JSON array: %w", err)
		}

		if len(rawArray) > MaxChildren {
			return nil, fmt.Errorf("JSON array exceeds bounded maximum of %d elements (got %d)",
				MaxChildren, len(rawArray))
		}

		records := make([]*model.DecodedRecord, 0, len(rawArray))
		for i, itemBytes := range rawArray {
			var fields map[string]any
			if err := json.Unmarshal(itemBytes, &fields); err != nil {
				return nil, fmt.Errorf("malformed JSON object at array index %d: %w", i, err)
			}
			if len(fields) > MaxFields {
				return nil, fmt.Errorf("JSON object at index %d exceeds maximum fields limit %d (got %d)", i, MaxFields, len(fields))
			}

			records = append(records, &model.DecodedRecord{
				RecordOrdinal: i,
				Format:        "json_array",
				Headers:       map[string]any{"batch_index": i, "batch_total": len(rawArray)},
				Fields:        fields,
				RawPayload:    itemBytes,
			})
		}
		return records, nil
	}

	// 2. Single JSON Object (handles single-line or multi-line formatted JSON)
	var singleFields map[string]any
	if err := json.Unmarshal(trimmed, &singleFields); err == nil {
		if len(singleFields) > MaxFields {
			return nil, fmt.Errorf("JSON object exceeds maximum fields limit %d (got %d)", MaxFields, len(singleFields))
		}
		record := &model.DecodedRecord{
			RecordOrdinal: 0,
			Format:        "json",
			Headers:       map[string]any{},
			Fields:        singleFields,
			RawPayload:    trimmed,
		}
		return []*model.DecodedRecord{record}, nil
	}

	// 3. Multi-line NDJSON
	if bytes.Contains(trimmed, []byte("\n")) {
		var records []*model.DecodedRecord
		scanner := bufio.NewScanner(bytes.NewReader(trimmed))
		ordinal := 0

		for scanner.Scan() {
			if len(records) >= MaxChildren {
				return nil, fmt.Errorf("NDJSON exceeds maximum allowed child records %d", MaxChildren)
			}

			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				continue
			}

			var fields map[string]any
			if err := json.Unmarshal(line, &fields); err != nil {
				return nil, fmt.Errorf("malformed NDJSON line at ordinal %d: %w", ordinal, err)
			}
			if len(fields) > MaxFields {
				return nil, fmt.Errorf("NDJSON object at ordinal %d exceeds maximum fields limit %d (got %d)", ordinal, MaxFields, len(fields))
			}

			lineBytes := make([]byte, len(line))
			copy(lineBytes, line)

			records = append(records, &model.DecodedRecord{
				RecordOrdinal: ordinal,
				Format:        "ndjson",
				Headers:       map[string]any{"record_ordinal": ordinal},
				Fields:        fields,
				RawPayload:    lineBytes,
			})
			ordinal++
		}

		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("NDJSON scanner error: %w", err)
		}

		if len(records) > 0 {
			return records, nil
		}
	}

	return nil, fmt.Errorf("malformed JSON object: %w", json.Unmarshal(trimmed, &singleFields))
}

func validateJSONDepth(raw []byte) error {
	depth := 0
	inString := false
	escaped := false
	for _, b := range raw {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if b == '\\' {
				escaped = true
			} else if b == '"' {
				inString = false
			}
			continue
		}
		if b == '"' {
			inString = true
			continue
		}
		if b == '{' || b == '[' {
			depth++
			if depth > MaxDepth {
				return fmt.Errorf("JSON nesting exceeds maximum depth %d", MaxDepth)
			}
		}
		if b == '}' || b == ']' {
			depth--
		}
	}
	return nil
}
