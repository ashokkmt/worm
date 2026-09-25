package integration_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"worm/internal/ingest"
	"worm/internal/model"
	"worm/internal/output"
)

// This opt-in test uses a real Kafka-compatible broker. It is skipped in the
// hermetic unit suite and is the protocol-level acceptance gate for air-gapped
// integration environments.
func TestKafkaRoundTrip(t *testing.T) {
	rawBrokers := os.Getenv("WORM_KAFKA_BROKERS")
	if rawBrokers == "" {
		t.Skip("set WORM_KAFKA_BROKERS to run the real-broker round trip")
	}
	brokers := strings.Split(rawBrokers, ",")
	topic := os.Getenv("WORM_KAFKA_TOPIC")
	if topic == "" {
		topic = "worm-integration"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adapter, err := ingest.NewBrokerKafkaAdapter(ingest.KafkaInputConfig{Name: "integration-kafka", Brokers: brokers, Topics: []string{topic}, ConsumerGroup: fmt.Sprintf("worm-it-%d", time.Now().UnixNano())})
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan model.IngestedRecord, 1)
	errCh := make(chan error, 1)
	go func() { errCh <- adapter.Start(ctx, out) }()
	sink, err := output.NewBrokerKafkaSink(output.KafkaOutputConfig{Name: "integration-producer", Brokers: brokers, Topic: topic, Acks: "all", Idempotent: true})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	event := &model.NormalizedEvent{Worm: model.WormEnvelope{EventID: fmt.Sprintf("worm-it-%d", time.Now().UnixNano())}, OCSF: map[string]any{"class_name": "Base Event"}}
	if err := sink.Emit(ctx, event); err != nil {
		t.Fatal(err)
	}
	select {
	case rec := <-out:
		if rec.Metadata.KafkaTopic != topic || rec.Metadata.KafkaOffset < 0 {
			t.Fatalf("missing Kafka coordinates: %+v", rec.Metadata)
		}
		rec.Ack <- nil
	case err := <-errCh:
		t.Fatalf("consumer stopped before round trip: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_ = adapter.Stop()
}
