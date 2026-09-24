package decode

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"worm/internal/model"
)

// cefExtensionRegex matches key= patterns in CEF extension strings.
// Keys can be alphanumeric plus underscore, hyphen, and dot.
var cefExtensionRegex = regexp.MustCompile(`(?:^|\s+)([a-zA-Z0-9_\.\-]+)=`)

// CEFDecoder implements the Decoder interface for ArcSight Common Event Format (CEF).
// CEF syntax: CEF:Version|Device Vendor|Device Product|Device Version|Device Event Class ID|Name|Severity|Extension
type CEFDecoder struct{}

// NewCEFDecoder returns a new CEFDecoder instance.
func NewCEFDecoder() *CEFDecoder {
	return &CEFDecoder{}
}

// Name returns the canonical format name.
func (d *CEFDecoder) Name() string {
	return "cef"
}

// Detect checks if the payload appears to be CEF formatted.
func (d *CEFDecoder) Detect(raw []byte) float64 {
	s := strings.TrimSpace(string(raw))
	if strings.HasPrefix(s, "CEF:") {
		return 0.99
	}
	// Also detect if wrapped inside a syslog message
	if strings.Contains(s, "CEF:0|") || strings.Contains(s, "CEF:1|") {
		return 0.85
	}
	return 0.0
}

// Decode parses a raw CEF payload into a DecodedRecord.
func (d *CEFDecoder) Decode(raw []byte) ([]*model.DecodedRecord, error) {
	if len(raw) > MaxPayloadBytes {
		return nil, fmt.Errorf("CEF payload %d bytes exceeds maximum limit of %d", len(raw), MaxPayloadBytes)
	}

	s := strings.TrimSpace(string(raw))
	if len(s) == 0 {
		return nil, errors.New("empty CEF payload")
	}

	// Locate CEF: prefix (handling any leading syslog framing if present)
	cefIdx := strings.Index(s, "CEF:")
	if cefIdx == -1 {
		return nil, errors.New("missing CEF: prefix")
	}
	cefPayload := s[cefIdx:]

	// Split header and extension.
	// Header contains 7 pipe-separated fields after "CEF:":
	// [0]: Version
	// [1]: Device Vendor
	// [2]: Device Product
	// [3]: Device Version
	// [4]: Device Event Class ID
	// [5]: Name
	// [6]: Severity
	// Everything after the 7th pipe is the Extension string.
	parts, extension, err := splitCEFHeader(cefPayload)
	if err != nil {
		return nil, fmt.Errorf("invalid CEF header: %w", err)
	}

	headers := make(map[string]any)
	fields := make(map[string]any)

	// Parse Version
	versionStr := strings.TrimPrefix(parts[0], "CEF:")
	if v, err := strconv.Atoi(versionStr); err == nil {
		headers["cef_version"] = v
	} else {
		headers["cef_version"] = versionStr
	}

	headers["device_vendor"] = parts[1]
	headers["device_product"] = parts[2]
	headers["device_version"] = parts[3]
	headers["device_event_class_id"] = parts[4]
	headers["name"] = parts[5]
	headers["severity"] = parts[6]

	// Also expose standard header fields in Fields for parser mapping
	fields["device_vendor"] = parts[1]
	fields["device_product"] = parts[2]
	fields["device_version"] = parts[3]
	fields["device_event_class_id"] = parts[4]
	fields["name"] = parts[5]
	fields["severity"] = parts[6]

	// Parse extension key-value pairs
	extMap := parseCEFExtensions(extension)
	for k, v := range extMap {
		fields[k] = v
	}

	rec := &model.DecodedRecord{
		RecordOrdinal: 0,
		Format:        "cef",
		Headers:       headers,
		Fields:        fields,
		RawPayload:    raw,
	}

	return []*model.DecodedRecord{rec}, nil
}

// splitCEFHeader splits the CEF header respecting backslash escapes (\| and \\).
// It returns the 7 header parts and the raw extension string.
func splitCEFHeader(s string) ([]string, string, error) {
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
			if len(parts) == 7 {
				// The remaining string is the extension
				extension := ""
				if i+1 < len(s) {
					extension = s[i+1:]
				}
				return parts, extension, nil
			}
			continue
		}

		cur.WriteByte(ch)
	}

	return nil, "", fmt.Errorf("expected at least 7 pipe separators in CEF header, found %d", len(parts))
}

// parseCEFExtensions extracts key=value pairs where values may contain spaces or escaped characters.
func parseCEFExtensions(ext string) map[string]string {
	result := make(map[string]string)
	matches := cefExtensionRegex.FindAllStringSubmatchIndex(ext, -1)
	if len(matches) == 0 {
		return result
	}

	for i := 0; i < len(matches); i++ {
		// Group 1 is the key
		keyStart := matches[i][2]
		keyEnd := matches[i][3]
		key := ext[keyStart:keyEnd]

		// Value starts after the '='
		valStart := matches[i][1]

		// Value ends where the next match starts, or at end of string
		var valEnd int
		if i+1 < len(matches) {
			valEnd = matches[i+1][0]
		} else {
			valEnd = len(ext)
		}

		val := strings.TrimSpace(ext[valStart:valEnd])
		// Unescape CEF value escapes: \= -> =, \\ -> \, \n -> newline, \r -> carriage return
		val = unescapeCEFValue(val)
		result[key] = val
	}

	return result
}

func unescapeCEFValue(s string) string {
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
			case '=':
				b.WriteByte('=')
			case '\\':
				b.WriteByte('\\')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			default:
				b.WriteByte('\\')
				b.WriteByte(ch)
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
