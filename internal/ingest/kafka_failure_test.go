package ingest

import (
	"context"
	"errors"
	"testing"
	"time"

	"worm/internal/model"
)

type failingCommitClient struct{ committed []int64 }

func (c *failingCommitClient) Poll(context.Context, time.Duration) ([]*KafkaConsumerRecord, error) {
	return nil, nil
}
func (c *failingCommitClient) CommitOffset(_ context.Context, _ string, _ int, offset int64) error {
	c.committed = append(c.committed, offset)
	return nil
}
func (*failingCommitClient) AllowRebalance() {}
func (*failingCommitClient) Close() error    { return nil }

func TestKafkaBatchStopsAfterRawCommitFailure(t *testing.T) {
	client := &failingCommitClient{}
	adapter := NewKafkaAdapter(KafkaInputConfig{}, client)
	out := make(chan model.IngestedRecord, 2)
	records := []*KafkaConsumerRecord{
		{Topic: "raw", Partition: 0, Offset: 10, Value: []byte("first")},
		{Topic: "raw", Partition: 0, Offset: 11, Value: []byte("second")},
	}
	done := make(chan error, 1)
	go func() { done <- adapter.processBatch(context.Background(), out, records) }()
	first := <-out
	first.Ack <- errors.New("storage failure")
	if err := <-done; err == nil {
		t.Fatal("expected failed raw commit to stop the Kafka batch")
	}
	if len(out) != 0 {
		t.Fatal("later same-partition record was forwarded after commit failure")
	}
	if len(client.committed) != 0 {
		t.Fatalf("committed offsets after failure = %v", client.committed)
	}
}
