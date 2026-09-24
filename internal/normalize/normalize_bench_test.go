package normalize_test

import (
	"path/filepath"
	"testing"
	"time"

	"worm/internal/model"
	"worm/internal/normalize"
	"worm/internal/packs"
)

func BenchmarkNormalizeAndValidate(b *testing.B) {
	root := findProjectRoot(&testing.T{})
	snap, err := packs.LoadDir(filepath.Join(root, "packs"))
	if err != nil {
		b.Fatalf("failed to load packs: %v", err)
	}

	normalizer := normalize.NewNormalizer()
	validator := normalize.NewValidator()

	paymentPack, ok := snap.GetPack("app-payment-gateway")
	if !ok {
		b.Fatal("app-payment-gateway pack not found")
	}

	rawPayload := []byte(`{"timestamp":"2026-09-24T12:00:00Z","severity":"info","transaction_id":"txn_9948271","amount":142.50,"currency":"USD","user_id":"usr_882194","merchant_id":"mch_00129","status":"COMPLETED","processing_time":48}`)
	paymentRecord := &model.DecodedRecord{
		Format: "json",
		Fields: map[string]any{
			"timestamp":       "2026-09-24T12:00:00Z",
			"severity":        "info",
			"transaction_id":  "txn_9948271",
			"amount":          142.50,
			"currency":        "USD",
			"user_id":         "usr_882194",
			"merchant_id":     "mch_00129",
			"status":          "COMPLETED",
			"processing_time": int64(48),
		},
		RawPayload: rawPayload,
	}

	rawEvt := &model.RawEvent{
		RawID:      "worm-raw-bench-001",
		RawSHA256:  "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		ByteCount:  len(rawPayload),
		Transport:  "http_post",
		SourceIP:   "10.0.1.25",
		ReceivedAt: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC),
		Payload:    rawPayload,
		Status:     model.StatusAccepted,
	}

	steps := []model.ProcessingStep{
		{Stage: "raw_commit", Timestamp: time.Now().UTC(), Result: "ok"},
		{Stage: "format_detect", Timestamp: time.Now().UTC(), Result: "json"},
	}

	b.Run("Normalize_JSON_Payment", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := normalizer.Normalize(rawEvt, paymentRecord, paymentPack, steps)
			if err != nil {
				b.Fatalf("normalize failed: %v", err)
			}
		}
	})

	normEvent, err := normalizer.Normalize(rawEvt, paymentRecord, paymentPack, steps)
	if err != nil {
		b.Fatalf("normalize setup failed: %v", err)
	}

	b.Run("Validate_NormalizedEvent", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			err := validator.Validate(normEvent)
			if err != nil {
				b.Fatalf("validation failed: %v", err)
			}
		}
	})

	b.Run("Full_Normalize_Plus_Validate", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			ev, err := normalizer.Normalize(rawEvt, paymentRecord, paymentPack, steps)
			if err != nil {
				b.Fatal(err)
			}
			if err := validator.Validate(ev); err != nil {
				b.Fatal(err)
			}
		}
	})
}
