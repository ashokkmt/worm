package decode

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
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

// detectDelimiter determines the most likely delimiter from candidate delimiters.
func (c *CSVDecoder) detectDelimiter(line []byte) rune {
	if c.Comma != 0 && c.Comma != ',' {
		return c.Comma
	}
	candidates := []rune{',', '\t', ';', '|'}
	bestDelim := ','
	bestCount := 0

	for _, d := range candidates {
		cnt := bytes.Count(line, []byte(string(d)))
		if cnt > bestCount {
			bestCount = cnt
			bestDelim = d
		}
	}
	return bestDelim
}

// Detect checks if the content exhibits consistent delimited tabular structure.
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
	delim := c.detectDelimiter(lines[0])

	c1 := bytes.Count(lines[0], []byte(string(delim)))
	if c1 == 0 {
		return 0.0
	}

	if len(lines) >= 2 {
		c2 := bytes.Count(lines[1], []byte(string(delim)))
		if c1 == c2 {
			return 0.85
		}
	} else if len(lines) == 1 && c1 >= 1 {
		reader := csv.NewReader(bytes.NewReader(trimmed))
		reader.Comma = delim
		if row, err := reader.Read(); err == nil && csvHeaderRow(row) {
			return 0.55
		}
		// A single headerless data row is common in exported flow/audit files.
		// Avoid claiming ordinary prose with a comma as CSV.
		if c1 >= 10 || csvTimestampPrefix.Match(trimmed) {
			return 0.55
		}
	}

	return 0.0
}

// Decode splits CSV rows into individual child DecodedRecords mapped to header columns.
func (c *CSVDecoder) Decode(raw []byte) ([]*model.DecodedRecord, error) {
	if len(raw) > MaxPayloadBytes {
		return nil, fmt.Errorf("CSV payload %d bytes exceeds maximum limit of %d", len(raw), MaxPayloadBytes)
	}

	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty CSV payload")
	}

	lines := bytes.SplitN(trimmed, []byte("\n"), 2)
	delim := c.detectDelimiter(lines[0])

	reader := csv.NewReader(bytes.NewReader(trimmed))
	reader.Comma = delim
	reader.TrimLeadingSpace = true
	reader.ReuseRecord = false

	// Treat an obviously data-shaped first row (for example a timestamp followed
	// by values) as a headerless record. Headerless columns receive stable names.
	rawHeaders, err := reader.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("empty CSV document")
		}
		return nil, fmt.Errorf("malformed CSV header: %w", err)
	}

	if len(rawHeaders) > MaxFields {
		return nil, fmt.Errorf("CSV column count %d exceeds maximum limit of %d", len(rawHeaders), MaxFields)
	}

	hasHeader := csvHeaderRow(rawHeaders)
	headers := make([]string, len(rawHeaders))
	for i, h := range rawHeaders {
		if hasHeader {
			headers[i] = strings.TrimSpace(h)
		} else {
			headers[i] = fmt.Sprintf("column_%d", i)
		}
	}

	var records []*model.DecodedRecord
	rowIdx := 0

	for {
		var row []string
		if rowIdx == 0 && !hasHeader {
			row = rawHeaders
		} else {
			row, err = reader.Read()
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return nil, fmt.Errorf("malformed CSV row %d: %w", rowIdx+1, err)
			}
		}

		if len(records) >= MaxChildren {
			return nil, fmt.Errorf("CSV record count exceeded maximum allowed limit of %d child records", MaxChildren)
		}

		fields := make(map[string]any, len(headers))
		for colIdx, colVal := range row {
			if colIdx < len(headers) {
				fields[headers[colIdx]] = colVal
			} else {
				fields[fmt.Sprintf("column_%d", colIdx)] = colVal
			}
		}

		rawRowLine := strings.Join(row, string(delim))

		records = append(records, &model.DecodedRecord{
			RecordOrdinal: rowIdx,
			Format:        "csv",
			Headers: map[string]any{
				"csv_row":      rowIdx + 1,
				"column_count": len(headers),
				"delimiter":    string(delim),
			},
			Fields:     fields,
			RawPayload: []byte(rawRowLine),
		})
		rowIdx++
	}

	return records, nil
}

var csvHeaderToken = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_. -]*$`)
var csvTimestampPrefix = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[ T]`)

func csvHeaderRow(row []string) bool {
	if len(row) == 0 {
		return false
	}
	for _, cell := range row {
		if !csvHeaderToken.MatchString(strings.TrimSpace(cell)) {
			return false
		}
	}
	return true
}
