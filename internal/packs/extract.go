package packs

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
	"worm/internal/model"
)

// Common timestamp layouts for automatic fallback detection
var commonTimeLayouts = []string{
	time.RFC3339,
	time.RFC3339Nano,
	"2006-01-02T15:04:05-07:00",
	"2006-01-02T15:04:05Z",
	"2006-01-02 15:04:05",
	"02/Jan/2006:15:04:05 -0700", // Combined Log Format (CLF)
	"Jan 02 15:04:05",           // RFC 3164 BSD Syslog
	"Jan  2 15:04:05",
}

// ExtractAndConvert extracts declared fields from a DecodedRecord and coerces them
// into their strongly-typed representations. It also isolates unmapped fields if requested
// and collects warnings for non-fatal optional conversion failures.
func ExtractAndConvert(pack *ParserPack, rec *model.DecodedRecord) (map[string]any, map[string]any, []string, error) {
	if pack == nil || rec == nil {
		return nil, nil, nil, fmt.Errorf("nil pack or decoded record")
	}

	extracted := make(map[string]any)
	consumedSourceFields := make(map[string]bool)
	var warnings []string

	for fieldName, rule := range pFields(pack) {
		val, found := lookupField(rec, rule.From)
		if !found || val == nil {
			if rule.Required {
				return nil, nil, nil, fmt.Errorf("required field %q (from %q) is missing", fieldName, rule.From)
			}
			continue
		}

		// Mark source field as consumed
		consumedSourceFields[rule.From] = true
		consumedSourceFields[strings.TrimPrefix(rule.From, "payload.")] = true

		convertedVal, err := castType(val, rule.Type, rule.Layout)
		if err != nil {
			if rule.Required {
				return nil, nil, nil, fmt.Errorf("field %q conversion to %s failed: %w", fieldName, rule.Type, err)
			}
			// For non-required fields, keep original value and record a warning
			warnings = append(warnings, fmt.Sprintf("field %q: conversion to %s failed (%v); preserving original value", fieldName, rule.Type, err))
			convertedVal = val
		}

		extracted[fieldName] = convertedVal
	}

	// Preserve unmapped fields
	unmapped := make(map[string]any)
	if pack.Spec.PreserveUnmapped {
		for k, v := range rec.Fields {
			if !consumedSourceFields[k] {
				unmapped[k] = v
			}
		}
	}

	return extracted, unmapped, warnings, nil
}

func pFields(pack *ParserPack) map[string]FieldRule {
	if pack.Spec.Fields == nil {
		return map[string]FieldRule{}
	}
	return pack.Spec.Fields
}

func castType(val any, targetType, layout string) (any, error) {
	strVal := strings.TrimSpace(fmt.Sprintf("%v", val))

	switch targetType {
	case "ip":
		ip := net.ParseIP(strVal)
		if ip == nil {
			return nil, fmt.Errorf("invalid IP address %q", strVal)
		}
		return ip.String(), nil

	case "integer":
		switch v := val.(type) {
		case int:
			return int64(v), nil
		case int64:
			return v, nil
		case float64:
			return int64(v), nil
		case float32:
			return int64(v), nil
		}
		i, err := strconv.ParseInt(strVal, 10, 64)
		if err != nil {
			// Try parsing as float and converting to int
			if f, ferr := strconv.ParseFloat(strVal, 64); ferr == nil {
				return int64(f), nil
			}
			return nil, fmt.Errorf("cannot parse integer from %q", strVal)
		}
		return i, nil

	case "float":
		switch v := val.(type) {
		case float64:
			return v, nil
		case float32:
			return float64(v), nil
		case int:
			return float64(v), nil
		case int64:
			return float64(v), nil
		}
		f, err := strconv.ParseFloat(strVal, 64)
		if err != nil {
			return nil, fmt.Errorf("cannot parse float from %q", strVal)
		}
		return f, nil

	case "timestamp":
		return parseTimestamp(strVal, layout)

	case "string":
		return strVal, nil

	case "boolean":
		switch v := val.(type) {
		case bool:
			return v, nil
		}
		b, err := strconv.ParseBool(strVal)
		if err != nil {
			return nil, fmt.Errorf("cannot parse boolean from %q", strVal)
		}
		return b, nil

	default:
		return val, nil
	}
}

func parseTimestamp(strVal, layout string) (int64, error) {
	// First check if string is already numeric unix timestamp
	if n, err := strconv.ParseInt(strVal, 10, 64); err == nil {
		if n > 1e11 { // already milliseconds
			return n, nil
		}
		return n * 1000, nil // seconds to milliseconds
	}

	// If explicit layout provided, try it first
	if layout != "" {
		t, err := time.Parse(layout, strVal)
		if err == nil {
			return t.UnixMilli(), nil
		}
	}

	// Try standard fallback layouts
	for _, l := range commonTimeLayouts {
		t, err := time.Parse(l, strVal)
		if err == nil {
			if t.Year() == 0 {
				// E.g. BSD syslog "Sep 20 10:20:01" has no year; use current year
				currentYear := time.Now().Year()
				t = t.AddDate(currentYear, 0, 0)
			}
			return t.UnixMilli(), nil
		}
	}

	return 0, fmt.Errorf("unable to parse timestamp %q", strVal)
}
