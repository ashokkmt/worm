package decode

import (
	"bytes"
	"fmt"
	"testing"
)

func BenchmarkDetect(b *testing.B) {
	registry := DefaultRegistry()

	fixtures := map[string][]byte{
		"JSON":   []byte(`{"timestamp":"2026-09-24T12:00:00Z","level":"info","event":"login","user_id":"u-1234","ip":"192.168.1.10","status":"success"}`),
		"Syslog": []byte(`<14>1 2026-09-24T12:00:00Z pa-fw-01 pan_traffic 1234 - [meta sequence="1"] connection opened to port 443`),
		"CEF":    []byte(`CEF:0|CrowdStrike|Falcon|7.0.0|1001|Process Rollup|5|src=10.0.1.5 dst=172.16.0.1 cs1=malicious.exe`),
		"LEEF":   []byte(`LEEF:2.0|IBM|QRadar|7.4|1002|^|src=10.0.0.1^dst=10.0.0.2^usrName=admin^action=login`),
		"XML":    []byte(`<?xml version="1.0" encoding="UTF-8"?><event timestamp="2026-09-24T12:00:00Z"><id>1001</id><status>ACTIVE</status></event>`),
		"CSV":    []byte("timestamp,actor,action,resource,result\n2026-09-24T12:00:00Z,admin,ALTER_TABLE,users,SUCCESS\n"),
	}

	for name, data := range fixtures {
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = registry.DetectRanked(data)
			}
		})
	}
}

func BenchmarkDecode_JSON(b *testing.B) {
	sizes := []struct {
		name string
		gen  func() []byte
	}{
		{
			name: "256B",
			gen: func() []byte {
				return []byte(`{"timestamp":"2026-09-24T12:00:00Z","level":"error","event":"db_query_timeout","service":"billing","duration_ms":3001,"query_id":"q-8842","retry_count":2,"client_ip":"10.0.4.12","status":"fail","error":"context deadline exceeded"}`)
			},
		},
		{
			name: "1KiB",
			gen: func() []byte {
				base := `{"timestamp":"2026-09-24T12:00:00Z","level":"info","event":"api_call","service":"gateway","client":{"ip":"192.168.1.100","port":54321,"agent":"Go-http-client/1.1","tls_version":"TLSv1.3"},"request":{"method":"POST","uri":"/v1/checkout","headers":{"content_type":"application/json","accept":"*/*","authorization":"Bearer [REDACTED]"},"body_digest":"sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},"response":{"status":200,"bytes_written":1420,"duration_ms":42},"trace":{"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","span_id":"00f067aa0ba902b7"},"metadata":{`
				for i := 0; i < 15; i++ {
					base += fmt.Sprintf(`"custom_key_%d":"custom_value_%d",`, i, i)
				}
				base += `"end":true}}`
				return []byte(base)
			},
		},
		{
			name: "8KiB",
			gen: func() []byte {
				var buf bytes.Buffer
				buf.WriteString(`{"timestamp":"2026-09-24T12:00:00Z","event":"heavy_audit_dump","payload":"`)
				for buf.Len() < 8000 {
					buf.WriteString("abcdefghijklmnopqrstuvwxyz0123456789!@#$%^&*()_+-=[]{}|;':,./<>?~`")
				}
				buf.WriteString(`","status":"ok"}`)
				return buf.Bytes()
			},
		},
	}

	decoder := NewJSONDecoder()
	for _, sz := range sizes {
		data := sz.gen()
		b.Run(sz.name, func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, err := decoder.Decode(data)
				if err != nil {
					b.Fatalf("Decode failed: %v", err)
				}
			}
		})
	}
}

func BenchmarkDecode_Syslog(b *testing.B) {
	rfc5424 := []byte(`<14>1 2026-09-24T12:00:00.123456Z pa-fw-01 pan_traffic 1234 - [meta sequence="1"] session opened to 10.0.0.1:443 proto=6`)
	rfc3164 := []byte(`<34>Oct 11 22:14:15 mymachine su: 'su root' failed for lonvick on /dev/pts/8`)
	layeredCEF := []byte(`<134>1 2026-09-24T12:00:00Z sensor01 falcon - - - CEF:0|CrowdStrike|Falcon|7.0.0|1001|Process Rollup|5|src=10.0.1.5 dst=172.16.0.1 cs1=malicious.exe`)
	layeredLEEF := []byte(`<134>1 2026-09-24T12:00:00Z sensor01 qradar - - - LEEF:2.0|IBM|QRadar|7.4|1002|^|src=10.0.0.1^dst=10.0.0.2^usrName=admin`)

	decoder := NewSyslogDecoder()

	b.Run("RFC5424", func(b *testing.B) {
		b.SetBytes(int64(len(rfc5424)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := decoder.Decode(rfc5424)
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("RFC3164", func(b *testing.B) {
		b.SetBytes(int64(len(rfc3164)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := decoder.Decode(rfc3164)
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("Layered_CEF", func(b *testing.B) {
		b.SetBytes(int64(len(layeredCEF)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := decoder.Decode(layeredCEF)
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("Layered_LEEF", func(b *testing.B) {
		b.SetBytes(int64(len(layeredLEEF)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := decoder.Decode(layeredLEEF)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkDecode_LEEF(b *testing.B) {
	leef10 := []byte("LEEF:1.0|IBM|QRadar|7.4|1001|\tsrc=10.0.0.1\tdst=10.0.0.2\tusrName=admin\taction=login\tproto=TCP")
	leef20 := []byte("LEEF:2.0|Vendor|Product|1.0|AuthFailure|^|src=192.168.1.50^dst=10.0.0.1^usrName=alice^reason=bad_credentials^attemptCount=3")
	leefHex := []byte("LEEF:2.0|Vendor|Product|1.0|AuthSuccess|0x5E|src=192.168.1.50^dst=10.0.0.1^usrName=alice^reason=token_valid")

	decoder := NewLEEFDecoder()

	b.Run("LEEF_1_0_Tab", func(b *testing.B) {
		b.SetBytes(int64(len(leef10)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := decoder.Decode(leef10)
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("LEEF_2_0_LiteralDelim", func(b *testing.B) {
		b.SetBytes(int64(len(leef20)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := decoder.Decode(leef20)
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("LEEF_2_0_HexDelim", func(b *testing.B) {
		b.SetBytes(int64(len(leefHex)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := decoder.Decode(leefHex)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkDecode_XML(b *testing.B) {
	singleDoc := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<event id="evt-100" timestamp="2026-09-24T12:00:00Z" xmlns="urn:worm:network:v1">
  <device>
    <hostname>pa-fw-01</hostname>
    <model>PA-5250</model>
  </device>
  <connection action="deny" proto="tcp">
    <src ip="192.168.1.50" port="43210"/>
    <dst ip="10.0.0.1" port="22"/>
  </connection>
</event>`)

	var batch10 bytes.Buffer
	batch10.WriteString(`<?xml version="1.0" encoding="UTF-8"?><events xmlns="urn:worm:perimeter:v1">`)
	for i := 0; i < 10; i++ {
		batch10.WriteString(fmt.Sprintf(`<event id="evt-%d"><src>10.0.0.%d</src><dst>172.16.0.%d</dst><action>allow</action></event>`, i, i, i))
	}
	batch10.WriteString(`</events>`)
	batch10Bytes := batch10.Bytes()

	var batch100 bytes.Buffer
	batch100.WriteString(`<?xml version="1.0" encoding="UTF-8"?><events xmlns="urn:worm:perimeter:v1">`)
	for i := 0; i < 100; i++ {
		batch100.WriteString(fmt.Sprintf(`<event id="evt-%d"><src>10.0.0.%d</src><dst>172.16.0.%d</dst><action>allow</action></event>`, i, i, i))
	}
	batch100.WriteString(`</events>`)
	batch100Bytes := batch100.Bytes()

	decoder := NewXMLDecoder()

	b.Run("SingleDocument", func(b *testing.B) {
		b.SetBytes(int64(len(singleDoc)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := decoder.Decode(singleDoc)
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("Batch10Elements", func(b *testing.B) {
		b.SetBytes(int64(len(batch10Bytes)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := decoder.Decode(batch10Bytes)
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("Batch100Elements", func(b *testing.B) {
		b.SetBytes(int64(len(batch100Bytes)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := decoder.Decode(batch100Bytes)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkDecode_CEF(b *testing.B) {
	cefData := []byte(`CEF:0|CrowdStrike|Falcon|7.0.0|1001|Process Rollup|5|src=10.0.1.5 dst=172.16.0.1 cs1=malicious.exe cs2=/bin/sh cn1=443 act=block`)
	decoder := NewCEFDecoder()

	b.SetBytes(int64(len(cefData)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := decoder.Decode(cefData)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecode_CSV(b *testing.B) {
	csvData := []byte("timestamp,actor,action,resource,result\n2026-09-24T12:00:00Z,admin,ALTER_TABLE,users,SUCCESS\n")
	decoder := NewCSVDecoder()

	b.SetBytes(int64(len(csvData)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := decoder.Decode(csvData)
		if err != nil {
			b.Fatal(err)
		}
	}
}
