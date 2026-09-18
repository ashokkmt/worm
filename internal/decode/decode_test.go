package decode

import (
	"os"
	"path/filepath"
	"testing"
)

func loadFixture(t *testing.T, relPath string) []byte {
	t.Helper()
	// Try relative to current package or relative to module root
	paths := []string{
		filepath.Join("../../", relPath),
		relPath,
	}
	for _, p := range paths {
		if data, err := os.ReadFile(p); err == nil {
			return data
		}
	}
	t.Fatalf("failed to find fixture %s", relPath)
	return nil
}

func TestSyslogDecoder_RFC5424(t *testing.T) {
	fixture := loadFixture(t, "testdata/formats/syslog/valid_rfc5424.txt")
	decoder := NewSyslogDecoder()

	conf := decoder.Detect(fixture)
	if conf < 0.90 {
		t.Errorf("expected RFC 5424 confidence >= 0.90, got %f", conf)
	}

	records, err := decoder.Decode(fixture)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}

	rec := records[0]
	if rec.Format != "syslog_rfc5424" {
		t.Errorf("expected format syslog_rfc5424, got %s", rec.Format)
	}
	if rec.Headers["syslog_pri"] != 14 {
		t.Errorf("expected PRI 14, got %v", rec.Headers["syslog_pri"])
	}
	if rec.Headers["syslog_facility"] != 1 {
		t.Errorf("expected facility 1, got %v", rec.Headers["syslog_facility"])
	}
	if rec.Headers["syslog_severity"] != 6 {
		t.Errorf("expected severity 6, got %v", rec.Headers["syslog_severity"])
	}
	if rec.Headers["syslog_host"] != "pa-fw-01" {
		t.Errorf("expected host pa-fw-01, got %v", rec.Headers["syslog_host"])
	}
	if rec.Headers["syslog_app"] != "paloalto" {
		t.Errorf("expected app paloalto, got %v", rec.Headers["syslog_app"])
	}
	if rec.Headers["syslog_msgid"] != "TRAFFIC" {
		t.Errorf("expected msgid TRAFFIC, got %v", rec.Headers["syslog_msgid"])
	}

	// Verify extracted key-value fields
	if rec.Fields["vendor_product"] != "PAN-OS" {
		t.Errorf("expected vendor_product=PAN-OS, got %v", rec.Fields["vendor_product"])
	}
	if rec.Fields["action"] != "allow" {
		t.Errorf("expected action=allow, got %v", rec.Fields["action"])
	}
	if rec.Fields["src"] != "10.0.1.50" {
		t.Errorf("expected src=10.0.1.50, got %v", rec.Fields["src"])
	}
	if rec.Fields["dport"] != "443" {
		t.Errorf("expected dport=443, got %v", rec.Fields["dport"])
	}
}

func TestSyslogDecoder_RFC3164(t *testing.T) {
	fixture := loadFixture(t, "testdata/formats/syslog/valid_rfc3164.txt")
	decoder := NewSyslogDecoder()

	conf := decoder.Detect(fixture)
	if conf < 0.90 {
		t.Errorf("expected RFC 3164 confidence >= 0.90, got %f", conf)
	}

	records, err := decoder.Decode(fixture)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}

	rec := records[0]
	if rec.Format != "syslog_rfc3164" {
		t.Errorf("expected format syslog_rfc3164, got %s", rec.Format)
	}
	if rec.Headers["syslog_pri"] != 86 {
		t.Errorf("expected PRI 86, got %v", rec.Headers["syslog_pri"])
	}
	if rec.Headers["syslog_facility"] != 10 {
		t.Errorf("expected facility 10, got %v", rec.Headers["syslog_facility"])
	}
	if rec.Headers["syslog_severity"] != 6 {
		t.Errorf("expected severity 6, got %v", rec.Headers["syslog_severity"])
	}
	if rec.Headers["syslog_host"] != "srv-prod-01" {
		t.Errorf("expected host srv-prod-01, got %v", rec.Headers["syslog_host"])
	}
	if rec.Headers["syslog_app"] != "sshd" {
		t.Errorf("expected app sshd, got %v", rec.Headers["syslog_app"])
	}
	if rec.Headers["syslog_pid"] != "14523" {
		t.Errorf("expected pid 14523, got %v", rec.Headers["syslog_pid"])
	}
}

func TestSyslogDecoder_Malformed(t *testing.T) {
	fixture := loadFixture(t, "testdata/formats/syslog/malformed_truncated.txt")
	decoder := NewSyslogDecoder()

	_, err := decoder.Decode(fixture)
	if err == nil {
		t.Errorf("expected error decoding malformed syslog, got nil")
	}
}

func TestJSONDecoder_Single(t *testing.T) {
	fixture := loadFixture(t, "testdata/formats/json/valid_single.json")
	decoder := NewJSONDecoder()

	conf := decoder.Detect(fixture)
	if conf < 0.90 {
		t.Errorf("expected confidence >= 0.90, got %f", conf)
	}

	records, err := decoder.Decode(fixture)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}

	rec := records[0]
	if rec.Fields["service"] != "payment-gateway" {
		t.Errorf("expected service payment-gateway, got %v", rec.Fields["service"])
	}
	if rec.Fields["amount"] != 15000.0 {
		t.Errorf("expected amount 15000.0, got %v", rec.Fields["amount"])
	}
	if rec.Fields["error_code"] != "PG_TIMEOUT" {
		t.Errorf("expected error_code PG_TIMEOUT, got %v", rec.Fields["error_code"])
	}
}

func TestJSONDecoder_NDJSON(t *testing.T) {
	fixture := loadFixture(t, "testdata/formats/json/valid_ndjson.json")
	decoder := NewJSONDecoder()

	records, err := decoder.Decode(fixture)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	if len(records) != 2 {
		t.Fatalf("expected 2 records from NDJSON, got %d", len(records))
	}

	if records[0].RecordOrdinal != 0 || records[0].Fields["user"] != "alice" {
		t.Errorf("unexpected record 0: %v", records[0])
	}
	if records[1].RecordOrdinal != 1 || records[1].Fields["user"] != "bob" {
		t.Errorf("unexpected record 1: %v", records[1])
	}
}

func TestJSONDecoder_Array(t *testing.T) {
	fixture := loadFixture(t, "testdata/formats/json/valid_array.json")
	decoder := NewJSONDecoder()

	conf := decoder.Detect(fixture)
	if conf < 0.85 {
		t.Errorf("expected confidence >= 0.85 for JSON array, got %f", conf)
	}

	records, err := decoder.Decode(fixture)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	if len(records) != 2 {
		t.Fatalf("expected 2 child records from JSON array, got %d", len(records))
	}

	if records[0].RecordOrdinal != 0 || records[0].Fields["host"] != "db-01" {
		t.Errorf("unexpected array child 0: %v", records[0])
	}
	if records[1].RecordOrdinal != 1 || records[1].Fields["host"] != "db-02" {
		t.Errorf("unexpected array child 1: %v", records[1])
	}
}

func TestJSONDecoder_Malformed(t *testing.T) {
	fixture := loadFixture(t, "testdata/formats/json/malformed_unclosed.json")
	decoder := NewJSONDecoder()

	_, err := decoder.Decode(fixture)
	if err == nil {
		t.Errorf("expected error decoding unclosed JSON, got nil")
	}
}

func TestCSVDecoder_Valid(t *testing.T) {
	fixture := loadFixture(t, "testdata/formats/csv/valid_headers.csv")
	decoder := NewCSVDecoder()

	conf := decoder.Detect(fixture)
	if conf < 0.80 {
		t.Errorf("expected confidence >= 0.80, got %f", conf)
	}

	records, err := decoder.Decode(fixture)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	// 1 header row + 3 data rows -> 3 child records
	if len(records) != 3 {
		t.Fatalf("expected 3 child records from CSV, got %d", len(records))
	}

	r0 := records[0]
	if r0.RecordOrdinal != 0 {
		t.Errorf("expected record ordinal 0, got %d", r0.RecordOrdinal)
	}
	if r0.Fields["source_ip"] != "10.0.1.50" {
		t.Errorf("expected source_ip 10.0.1.50, got %v", r0.Fields["source_ip"])
	}
	if r0.Fields["port"] != "443" {
		t.Errorf("expected port 443, got %v", r0.Fields["port"])
	}
	if r0.Fields["action"] != "allow" {
		t.Errorf("expected action allow, got %v", r0.Fields["action"])
	}

	r2 := records[2]
	if r2.RecordOrdinal != 2 {
		t.Errorf("expected record ordinal 2, got %d", r2.RecordOrdinal)
	}
	if r2.Fields["action"] != "drop" {
		t.Errorf("expected action drop, got %v", r2.Fields["action"])
	}
}

func TestCSVDecoder_Malformed(t *testing.T) {
	fixture := loadFixture(t, "testdata/formats/csv/malformed_unquoted.csv")
	decoder := NewCSVDecoder()

	_, err := decoder.Decode(fixture)
	if err == nil {
		t.Errorf("expected error decoding malformed unquoted CSV, got nil")
	}
}

func TestRegistryAutoDetection(t *testing.T) {
	registry := DefaultRegistry()

	tests := []struct {
		name         string
		fixturePath  string
		expectedName string
		expectedRows int
	}{
		{"RFC5424", "testdata/formats/syslog/valid_rfc5424.txt", "syslog", 1},
		{"RFC3164", "testdata/formats/syslog/valid_rfc3164.txt", "syslog", 1},
		{"JSON Single", "testdata/formats/json/valid_single.json", "json", 1},
		{"NDJSON", "testdata/formats/json/valid_ndjson.json", "json", 2},
		{"JSON Array", "testdata/formats/json/valid_array.json", "json", 2},
		{"CSV", "testdata/formats/csv/valid_headers.csv", "csv", 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := loadFixture(t, tc.fixturePath)
			decoder, records, err := registry.DetectAndDecode(raw)
			if err != nil {
				t.Fatalf("DetectAndDecode failed for %s: %v", tc.name, err)
			}
			if decoder.Name() != tc.expectedName {
				t.Errorf("expected decoder %s, got %s", tc.expectedName, decoder.Name())
			}
			if len(records) != tc.expectedRows {
				t.Errorf("expected %d records, got %d", tc.expectedRows, len(records))
			}
		})
	}
}
