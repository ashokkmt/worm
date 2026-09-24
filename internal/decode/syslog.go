package decode

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"worm/internal/model"
)

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
			msgBody = parts[6]
		}
	} else {
		// RFC 3164 (BSD): <PRI>Mmm dd hh:mm:ss HOSTNAME TAG[PID]: MSG
		formatVariant = "syslog_rfc3164"
		// Example: Sep 20 10:20:01 srv-prod-01 sshd[14523]: Accepted publickey...
		parts := strings.SplitN(rest, " ", 4)
		if len(parts) >= 4 {
			// Month Day Time
			headers["syslog_time"] = fmt.Sprintf("%s %s %s", parts[0], parts[1], parts[2])
			remaining := parts[3]

			hostAndMsg := strings.SplitN(remaining, " ", 2)
			headers["syslog_host"] = hostAndMsg[0]

			if len(hostAndMsg) > 1 {
				appAndBody := strings.SplitN(hostAndMsg[1], ": ", 2)
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
					msgBody = hostAndMsg[1]
				}
			}
		} else {
			msgBody = rest
		}
	}

	fields["message"] = msgBody

	// Extract key=value pairs from the message body if present (common in firewalls e.g. PAN-OS)
	kvPairs := parseKeyValueTokens(msgBody)
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

// parseKeyValueTokens extracts tokens in format `key=value` or `key="quoted value"`.
func parseKeyValueTokens(s string) map[string]any {
	result := make(map[string]any)
	tokens := strings.Fields(s)

	for _, token := range tokens {
		eqIdx := strings.IndexByte(token, '=')
		if eqIdx > 0 && eqIdx < len(token)-1 {
			key := token[:eqIdx]
			val := strings.Trim(token[eqIdx+1:], "\"")
			result[key] = val
		}
	}
	return result
}
