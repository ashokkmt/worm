package ingest

import (
	"context"
	"fmt"
	"sync"
	"time"
	"worm/internal/model"
)

// KafkaInputConfig defines parameters for consuming raw datagrams from an Apache Kafka topic.
type KafkaInputConfig struct {
	Name          string        `json:"name"`
	Brokers       []string      `json:"brokers"`
	Topics        []string      `json:"topics"`
	ConsumerGroup string        `json:"consumer_group"`
	PollTimeout   time.Duration `json:"poll_timeout"`
}

// KafkaConsumerRecord represents a record fetched from Kafka.
type KafkaConsumerRecord struct {
	Topic     string
	Partition int
	Offset    int64
	Key       []byte
	Value     []byte
	Timestamp time.Time
}

// KafkaConsumerClient provides an abstraction for Kafka consumer polling and offset committing.
type KafkaConsumerClient interface {
	Poll(ctx context.Context, timeout time.Duration) ([]*KafkaConsumerRecord, error)
	CommitOffset(ctx context.Context, topic string, partition int, offset int64) error
	Close() error
}

// MemoryKafkaConsumer is a thread-safe simulator for testing Kafka ingestion.
type MemoryKafkaConsumer struct {
	mu        sync.Mutex
	queue     []*KafkaConsumerRecord
	committed map[string]int64
	closed    bool
}

func NewMemoryKafkaConsumer(initial []*KafkaConsumerRecord) *MemoryKafkaConsumer {
	return &MemoryKafkaConsumer{
		queue:     initial,
		committed: make(map[string]int64),
	}
}

func (m *MemoryKafkaConsumer) Poll(ctx context.Context, timeout time.Duration) ([]*KafkaConsumerRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.queue) == 0 {
		return nil, nil
	}

	batch := m.queue
	m.queue = nil
	return batch, nil
}

func (m *MemoryKafkaConsumer) CommitOffset(ctx context.Context, topic string, partition int, offset int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := fmt.Sprintf("%s-%d", topic, partition)
	m.committed[key] = offset
	return nil
}

func (m *MemoryKafkaConsumer) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *MemoryKafkaConsumer) Enqueue(rec *KafkaConsumerRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queue = append(m.queue, rec)
}

func (m *MemoryKafkaConsumer) GetCommitted(topic string, partition int) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.committed[fmt.Sprintf("%s-%d", topic, partition)]
}

// KafkaAdapter implements Adapter for ingesting raw log events from Kafka topics.
type KafkaAdapter struct {
	cfg    KafkaInputConfig
	client KafkaConsumerClient
	mu     sync.Mutex
	closed bool
}

// NewKafkaAdapter creates a new KafkaAdapter. If client is nil, it uses an in-memory queue.
func NewKafkaAdapter(cfg KafkaInputConfig, client KafkaConsumerClient) *KafkaAdapter {
	if cfg.PollTimeout <= 0 {
		cfg.PollTimeout = 250 * time.Millisecond
	}
	if client == nil {
		client = NewMemoryKafkaConsumer(nil)
	}
	return &KafkaAdapter{
		cfg:    cfg,
		client: client,
	}
}

func (a *KafkaAdapter) Name() string {
	if a.cfg.Name != "" {
		return "kafka-input-" + a.cfg.Name
	}
	return "kafka-input"
}

func (a *KafkaAdapter) Start(ctx context.Context, out chan<- model.IngestedRecord) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		a.mu.Lock()
		closed := a.closed
		a.mu.Unlock()
		if closed {
			return nil
		}

		records, err := a.client.Poll(ctx, a.cfg.PollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}

		for _, msg := range records {
			ackChan := make(chan error, 1)
			ingested := model.IngestedRecord{
				RawBytes:   msg.Value,
				Transport:  "kafka_input",
				SourceIP:   "kafka://" + msg.Topic,
				ReceivedAt: time.Now().UTC(),
				Ack:        ackChan,
			}

			select {
			case out <- ingested:
				select {
				case commitErr := <-ackChan:
					if commitErr != nil {
						// Raw store durability failed: do not commit offset
						continue
					}
					// Durably stored: commit Kafka offset
					_ = a.client.CommitOffset(ctx, msg.Topic, msg.Partition, msg.Offset)
				case <-ctx.Done():
					return ctx.Err()
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		if len(records) == 0 {
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func (a *KafkaAdapter) Stop() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	return a.client.Close()
}
