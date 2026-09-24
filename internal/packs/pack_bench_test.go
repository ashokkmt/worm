package packs

import (
	"fmt"
	"testing"
	"worm/internal/model"
)

func createBenchmarkPack(i int) *ParserPack {
	return &ParserPack{
		APIVersion: "worm.io/v1",
		Kind:       "ParserPack",
		Metadata: PackMetadata{
			Name:    fmt.Sprintf("bench-pack-%04d", i),
			Version: "1.0.0",
		},
		Spec: PackSpec{
			SourceCategory: "network_device",
			Format:         "syslog",
			Match: MatchRule{
				FieldEquals: map[string]string{
					"app_name": fmt.Sprintf("app-%04d", i),
				},
			},
			Fields: map[string]FieldRule{
				"src_ip": {From: "src", Type: "ip"},
				"dst_ip": {From: "dst", Type: "ip"},
				"action": {From: "act", Type: "string"},
			},
			Map: map[string]string{
				"src_ip": "event.src_endpoint.ip",
				"dst_ip": "event.dst_endpoint.ip",
				"action": "event.activity_name",
			},
		},
	}
}

func BenchmarkPackMatch(b *testing.B) {
	counts := []int{1, 10, 100, 1000}

	for _, count := range counts {
		packList := make([]*ParserPack, count)
		for i := 0; i < count; i++ {
			packList[i] = createBenchmarkPack(i)
		}

		snap, err := NewSnapshot(fmt.Sprintf("v-%d", count), packList)
		if err != nil {
			b.Fatalf("failed to create snapshot: %v", err)
		}

		// Hit the target at the end (worst case for linear search)
		targetIndex := count - 1
		rec := &model.DecodedRecord{
			Format: "syslog_rfc5424",
			Fields: map[string]any{
				"app_name": fmt.Sprintf("app-%04d", targetIndex),
				"src":      "192.168.1.10",
				"dst":      "10.0.0.1",
				"act":      "deny",
			},
			RawPayload: []byte(fmt.Sprintf("app_name=app-%04d src=192.168.1.10 dst=10.0.0.1 act=deny", targetIndex)),
		}

		b.Run(fmt.Sprintf("%d_Packs", count), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				p, err := snap.Match(rec)
				if err != nil || p == nil {
					b.Fatalf("expected match, got err: %v", err)
				}
			}
		})
	}
}

func BenchmarkNestedExtraction(b *testing.B) {
	deepMap := map[string]any{
		"event": map[string]any{
			"network": map[string]any{
				"client": map[string]any{
					"ip":   "10.0.4.15",
					"port": 54321,
				},
				"server": map[string]any{
					"ip":   "172.16.1.1",
					"port": 443,
				},
			},
			"action": "allow",
		},
	}

	b.Run("DirectKey", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _ = LookupNested(deepMap, "event")
		}
	})

	b.Run("Nested4Levels", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _ = LookupNested(deepMap, "event.network.client.ip")
		}
	})
}
