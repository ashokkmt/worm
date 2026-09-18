package decode

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strings"
	"worm/internal/model"
)

// CSVDecoder parses delimited tabular files, extracting headers and splitting rows into child events.
type CSVDecoder struct {
	Comma rune
}

func NewCSVDecoder() *CSVDecoder {
	return &CSVDecoder{Comma: ','}
}

func (c *CSVDecoder) Name() string {
	return "csv"
}

// Detect checks if the content exhibits consistent comma-delimited tabular structure.
func (c *CSVDecoder) Detect(raw []byte) float64 {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return 0.0
	}

	// Reject if obvious JSON or Syslog or CEF
	firstChar := trimmed[0]
	if firstChar == '{' || firstChar == '[' || firstChar == '<' {
		return 0.0
	}
	if bytes.HasPrefix(trimmed, []byte("CEF:")) {
		return 0.0
	}

	lines := bytes.SplitN(trimmed, []byte("\n"), 3)
	if len(lines) < 2 {
		return 0.0
	}

	c1 := bytes.Count(lines[0], []byte{','})
	c2 := bytes.Count(lines[1], []byte{','})

	if c1 >= 1 && c1 == c2 {
		return 0.85
	}

	return 0.0
}

// Decode splits CSV rows into individual child DecodedRecords mapped to header columns.
func (c *CSVDecoder) Decode(raw []byte) ([]*model.DecodedRecord, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty CSV payload")
	}

	reader := csv.NewReader(bytes.NewReader(trimmed))
	reader.Comma = c.Comma
	reader.TrimLeadingSpace = true

	allRows, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("malformed CSV: %w", err)
	}

	if len(allRows) == 0 {
		return nil, fmt.Errorf("empty CSV document")
	}

	// First row represents column headers
	rawHeaders := allRows[0]
	headers := make([]string, len(rawHeaders))
	for i, h := range rawHeaders {
		headers[i] = strings.TrimSpace(h)
	}

	if len(allRows) == 1 {
		// Only headers, zero data rows
		return []*model.DecodedRecord{}, nil
	}

	records := make([]*model.DecodedRecord, 0, len(allRows)-1)
	for rowIdx, row := range allRows[1:] {
		fields := make(map[string]any, len(headers))
		for colIdx, colVal := range row {
			if colIdx < len(headers) {
				fields[headers[colIdx]] = colVal
			} else {
				fields[fmt.Sprintf("column_%d", colIdx)] = colVal
			}
		}

		rawRowLine := strings.Join(row, string(c.Comma))

		records = append(records, &model.DecodedRecord{
			RecordOrdinal: rowIdx,
			Format:        "csv",
			Headers: map[string]any{
				"csv_row":      rowIdx + 1,
				"column_count": len(headers),
			},
			Fields:     fields,
			RawPayload: []byte(rawRowLine),
		})
	}

	return records, nil
}
