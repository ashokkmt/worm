package decode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"worm/internal/model"
)

const (
	maxJSONArrayElements = 10000
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
		// JSON Array
		return 0.90
	}

	return 0.0
}

// Decode splits JSON documents into one or more DecodedRecord child events.
func (j *JSONDecoder) Decode(raw []byte) ([]*model.DecodedRecord, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty JSON payload")
	}

	// 1. JSON Array Handling: [...]
	if trimmed[0] == '[' {
		var rawArray []json.RawMessage
		if err := json.Unmarshal(trimmed, &rawArray); err != nil {
			return nil, fmt.Errorf("malformed JSON array: %w", err)
		}

		if len(rawArray) > maxJSONArrayElements {
			return nil, fmt.Errorf("JSON array exceeds bounded maximum of %d elements (got %d)",
				maxJSONArrayElements, len(rawArray))
		}

		records := make([]*model.DecodedRecord, 0, len(rawArray))
		for i, itemBytes := range rawArray {
			var fields map[string]any
			if err := json.Unmarshal(itemBytes, &fields); err != nil {
				return nil, fmt.Errorf("malformed JSON object at array index %d: %w", i, err)
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

	// 2. Multi-line NDJSON vs Single Object
	// Check if there are multiple lines that start with '{'
	if bytes.Contains(trimmed, []byte("\n")) {
		var records []*model.DecodedRecord
		scanner := bufio.NewScanner(bytes.NewReader(trimmed))
		ordinal := 0

		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				continue
			}

			var fields map[string]any
			if err := json.Unmarshal(line, &fields); err != nil {
				// If a multi-line document has non-JSON lines, fail parsing
				return nil, fmt.Errorf("malformed NDJSON line at ordinal %d: %w", ordinal, err)
			}

			// Copy line bytes
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

	// 3. Single JSON Object
	var fields map[string]any
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return nil, fmt.Errorf("malformed JSON object: %w", err)
	}

	record := &model.DecodedRecord{
		RecordOrdinal: 0,
		Format:        "json",
		Headers:       map[string]any{},
		Fields:        fields,
		RawPayload:    trimmed,
	}

	return []*model.DecodedRecord{record}, nil
}
