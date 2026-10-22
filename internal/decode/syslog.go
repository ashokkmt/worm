package decode

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"worm/internal/model"
)

var rfc3164Header = regexp.MustCompile(`^([A-Z][a-z]{2})\s+([0-9]{1,2})\s+(\d{2}:\d{2}:\d{2})\s+(\S+)(?:\s+(.*))?$`)

// SyslogDecoder parses RFC 5424 and RFC 3164 (BSD) syslog messages.
type SyslogDecoder struct{}

func NewSyslogDecoder() *SyslogDecoder {
	return &SyslogDecoder{}
}

func (s *SyslogDecoder) Name() string {
	return "syslog"
}

// Detect checks for syslog priority framing `<PRI>`.
func (s *SyslogDecoder) Detect(raw []byte) float64 {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	if len(trimmed) < 4 || trimmed[0] != '<' {
		return 0.0
	}

	gtIdx := bytes.IndexByte(trimmed, '>')
	if gtIdx < 2 || gtIdx > 5 {
		return 0.0
	}

	priStr := string(trimmed[1:gtIdx])
	pri, err := strconv.Atoi(priStr)
	if err != nil || pri < 0 || pri > 191 {
		return 0.0
	}

	rest := trimmed[gtIdx+1:]
	if len(rest) > 2 && rest[0] == '1' && rest[1] == ' ' {
		return 0.98 // RFC 5424 `<PRI>1 `
	}

	// RFC 3164 usually has month e.g. `<PRI>Sep ` or `<PRI>Jan `
	months := []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
	for _, m := range months {
		if strings.HasPrefix(string(rest), m) {
			return 0.95 // RFC 3164
		}
	}

	return 0.85
}

// Decode parses the syslog header and payload into a DecodedRecord.
func (s *SyslogDecoder) Decode(raw []byte) ([]*model.DecodedRecord, error) {
	if len(raw) > MaxPayloadBytes {
		return nil, fmt.Errorf("syslog payload %d bytes exceeds maximum limit of %d", len(raw), MaxPayloadBytes)
	}

	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty syslog message")
	}

	if trimmed[0] != '<' {
		return nil, fmt.Errorf("malformed syslog: missing leading '<'")
	}

	gtIdx := bytes.IndexByte(trimmed, '>')
	if gtIdx < 2 {
		return nil, fmt.Errorf("malformed syslog: missing or empty priority '<>'")
	}
	if gtIdx > 5 {
		return nil, fmt.Errorf("malformed syslog: priority tag exceeds 5 bytes")
	}

	priStr := string(trimmed[1:gtIdx])
	pri, err := strconv.Atoi(priStr)
	if err != nil {
		return nil, fmt.Errorf("malformed syslog: non-numeric priority %q", priStr)
	}
	if pri < 0 || pri > 191 {
		return nil, fmt.Errorf("malformed syslog: priority %d out of valid range (0-191)", pri)
	}

	facility := pri / 8
	severity := pri % 8

	rest := string(trimmed[gtIdx+1:])
	headers := map[string]any{
		"syslog_pri":      pri,
		"syslog_facility": facility,
		"syslog_severity": severity,
	}
	fields := make(map[string]any)

	var msgBody string
	var formatVariant string

	if strings.HasPrefix(rest, "1 ") {
		// RFC 5424: <PRI>1 TIMESTAMP HOSTNAME APP-NAME PROCID MSGID [STRUCTURED-DATA] MSG
		formatVariant = "syslog_rfc5424"
		parts := strings.SplitN(rest, " ", 7)
		if len(parts) < 6 {
			return nil, fmt.Errorf("malformed RFC 5424 syslog: incomplete header elements")
		}

		headers["syslog_version"] = 1
		headers["syslog_time"] = parts[1]
		headers["syslog_host"] = parts[2]
		headers["syslog_app"] = parts[3]
		headers["syslog_procid"] = parts[4]
		headers["syslog_msgid"] = parts[5]

		if len(parts) >= 7 {
			sdAndMsg := parts[6]
			if strings.HasPrefix(sdAndMsg, "- ") {
				headers["syslog_structured_data"] = "-"
				msgBody = strings.TrimPrefix(sdAndMsg, "- ")
			} else if sdAndMsg == "-" {
				headers["syslog_structured_data"] = "-"
				msgBody = ""
			} else if strings.HasPrefix(sdAndMsg, "[") {
				sd, message, err := splitRFC5424StructuredData(sdAndMsg)
				if err != nil {
					return nil, err
				}
				headers["syslog_structured_data"] = sd
				msgBody = message
			} else {
				msgBody = sdAndMsg
			}
		}
	} else {
		// RFC 3164 (BSD): <PRI>Mmm dd hh:mm:ss HOSTNAME TAG[PID]: MSG
		formatVariant = "syslog_rfc3164"
		// Example: Sep 20 10:20:01 srv-prod-01 sshd[14523]: Accepted publickey...
		parts := rfc3164Header.FindStringSubmatch(rest)
		if len(parts) == 6 {
			headers["syslog_time"] = fmt.Sprintf("%s %s %s", parts[1], parts[2], parts[3])
			remaining := parts[5]
			headers["syslog_host"] = parts[4]

			if remaining != "" {
				appAndBody := strings.SplitN(remaining, ": ", 2)
				if len(appAndBody) == 2 {
					appTag := appAndBody[0]
					if pOpen := strings.Index(appTag, "["); pOpen != -1 && strings.HasSuffix(appTag, "]") {
						headers["syslog_app"] = appTag[:pOpen]
						headers["syslog_pid"] = appTag[pOpen+1 : len(appTag)-1]
					} else {
						headers["syslog_app"] = appTag
					}
					msgBody = appAndBody[1]
				} else {
					msgBody = remaining
				}
			}
		} else {
			msgBody = rest
		}
	}

	if strings.Count(msgBody, ",") >= 5 && !balancedQuotes(msgBody) {
		return nil, fmt.Errorf("malformed syslog payload: unbalanced quoted field")
	}

	// Layered Syslog payload inspection (Section 4.1)
	trimmedBody := strings.TrimSpace(msgBody)
	if strings.HasPrefix(trimmedBody, "CEF:") {
		cefDec := NewCEFDecoder()
		if innerRecs, err := cefDec.Decode([]byte(trimmedBody)); err == nil && len(innerRecs) > 0 {
			return wrapInnerRecords(innerRecs, headers, raw, formatVariant), nil
		}
	} else if strings.HasPrefix(trimmedBody, "LEEF:") {
		leefDec := NewLEEFDecoder()
		if innerRecs, err := leefDec.Decode([]byte(trimmedBody)); err == nil && len(innerRecs) > 0 {
			return wrapInnerRecords(innerRecs, headers, raw, formatVariant), nil
		}
	} else if (strings.HasPrefix(trimmedBody, "{") && strings.HasSuffix(trimmedBody, "}")) ||
		(strings.HasPrefix(trimmedBody, "[") && strings.HasSuffix(trimmedBody, "]")) {
		jsonDec := NewJSONDecoder()
		if innerRecs, err := jsonDec.Decode([]byte(trimmedBody)); err == nil && len(innerRecs) > 0 {
			return wrapInnerRecords(innerRecs, headers, raw, formatVariant), nil
		}
	} else if strings.HasPrefix(trimmedBody, "<") && strings.HasSuffix(trimmedBody, ">") && !strings.HasPrefix(trimmedBody, "<!") {
		xmlDec := NewXMLDecoder()
		if innerRecs, err := xmlDec.Decode([]byte(trimmedBody)); err == nil && len(innerRecs) > 0 {
			return wrapInnerRecords(innerRecs, headers, raw, formatVariant), nil
		}
	}

	fields["message"] = msgBody

	// Extract key=value pairs from the message body if present (common in firewalls e.g. PAN-OS)
	kvPairs, err := parseKeyValueTokens(msgBody)
	if err != nil {
		return nil, err
	}
	for k, v := range kvPairs {
		fields[k] = v
	}

	// Copy headers into fields for convenience
	for k, v := range headers {
		fields[k] = v
	}

	record := &model.DecodedRecord{
		RecordOrdinal: 0,
		Format:        formatVariant,
		Headers:       headers,
		Fields:        fields,
		RawPayload:    raw,
	}

	return []*model.DecodedRecord{record}, nil
}

func splitRFC5424StructuredData(s string) (string, string, error) {
	pos := 0
	for pos < len(s) && s[pos] == '[' {
		pos++
		quoted, escaped, closed := false, false, false
		for pos < len(s) {
			ch := s[pos]
			pos++
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				quoted = !quoted
				continue
			}
			if ch == ']' && !quoted {
				closed = true
				break
			}
		}
		if !closed || quoted || escaped {
			return "", "", fmt.Errorf("malformed RFC 5424 structured data")
		}
		if pos == len(s) || s[pos] != '[' {
			return s[:pos], strings.TrimSpace(s[pos:]), nil
		}
	}
	if pos == 0 {
		return "-", s, nil
	}
	return s[:pos], strings.TrimSpace(s[pos:]), nil
}

func wrapInnerRecords(innerRecs []*model.DecodedRecord, syslogHeaders map[string]any, raw []byte, outerVariant string) []*model.DecodedRecord {
	for _, rec := range innerRecs {
		rec.RawPayload = raw
		rec.Headers["syslog_envelope"] = outerVariant
		for k, v := range syslogHeaders {
			if _, exists := rec.Headers[k]; !exists {
				rec.Headers[k] = v
			}
			if _, exists := rec.Fields[k]; !exists {
				rec.Fields[k] = v
			}
		}
	}
	return innerRecs
}

// parseKeyValueTokens extracts key=value tokens, preserving quoted values with spaces.
func parseKeyValueTokens(s string) (map[string]any, error) {
	result := make(map[string]any)
	for i := 0; i < len(s); {
		for i < len(s) && isSpace(s[i]) {
			i++
		}
		start := i
		for i < len(s) && s[i] != '=' && !isSpace(s[i]) {
			i++
		}
		if i == len(s) || s[i] != '=' || i == start {
			for i < len(s) && !isSpace(s[i]) {
				i++
			}
			continue
		}
		key := s[start:i]
		i++
		if i < len(s) && (s[i] == '"' || s[i] == '\'') {
			quote := s[i]
			i++
			var value strings.Builder
			closed := false
			for i < len(s) {
				if s[i] == '\\' && i+1 < len(s) {
					if s[i+1] == quote || s[i+1] == '\\' {
						value.WriteByte(s[i+1])
						i += 2
						continue
					}
					value.WriteByte(s[i])
					i++
					continue
				}
				if s[i] == quote {
					i++
					closed = true
					break
				}
				value.WriteByte(s[i])
				i++
			}
			if !closed {
				return nil, fmt.Errorf("malformed key=value payload: unterminated quoted value for %q", key)
			}
			result[key] = value.String()
		} else {
			startValue := i
			for i < len(s) && !isSpace(s[i]) {
				i++
			}
			if startValue < i {
				result[key] = s[startValue:i]
			}
		}
	}
	return result, nil
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\r' || b == '\n' }

func balancedQuotes(s string) bool {
	quoted := false
	for i := 0; i < len(s); i++ {
		if quoted && s[i] == '\\' && i+1 < len(s) {
			i++
			continue
		}
		if s[i] != '"' {
			continue
		}
		if quoted && i+1 < len(s) && s[i+1] == '"' {
			i++
			continue
		}
		quoted = !quoted
	}
	return !quoted
}
