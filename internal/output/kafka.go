package output

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
	"worm/internal/model"
)

// KafkaOutputConfig configures delivery of normalized events to an Apache Kafka topic.
type KafkaOutputConfig struct {
	Name        string        `json:"name"`
	Brokers     []string      `json:"brokers"`
	Topic       string        `json:"topic"`
	KeyField    string        `json:"key_field"`
	Acks        string        `json:"acks"` // "all", "1", "0"
	Idempotent  bool          `json:"idempotent"`
	Compression string        `json:"compression"` // "none", "gzip", "snappy", "zstd"
	BatchBytes  int           `json:"batch_bytes"`
	Linger      time.Duration `json:"linger"`
}

// KafkaMessage represents a record produced to Kafka.
type KafkaMessage struct {
	Topic     string    `json:"topic"`
	Key       []byte    `json:"key"`
	Value     []byte    `json:"value"`
	Timestamp time.Time `json:"timestamp"`
	Offset    int64     `json:"offset"`
}

// KafkaProducerClient is an interface that allows standard or mockable broker transport.
type KafkaProducerClient interface {
	Produce(ctx context.Context, msg *KafkaMessage) error
	Flush(ctx context.Context) error
	Close() error
}

// MemoryKafkaProducer is a thread-safe in-memory Kafka producer used for local testing and simulation.
type MemoryKafkaProducer struct {
	mu       sync.Mutex
	messages []*KafkaMessage
	offset   int64
}

func NewMemoryKafkaProducer() *MemoryKafkaProducer {
	return &MemoryKafkaProducer{
		messages: make([]*KafkaMessage, 0),
	}
}

func (m *MemoryKafkaProducer) Produce(ctx context.Context, msg *KafkaMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.offset++
	msg.Offset = m.offset
	m.messages = append(m.messages, msg)
	return nil
}

func (m *MemoryKafkaProducer) Flush(ctx context.Context) error {
	return nil
}

func (m *MemoryKafkaProducer) Close() error {
	return nil
}

func (m *MemoryKafkaProducer) Messages() []*KafkaMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]*KafkaMessage, len(m.messages))
	copy(copied, m.messages)
	return copied
}

// KafkaSink implements OutputSink for publishing normalized events to Kafka.
type KafkaSink struct {
	cfg    KafkaOutputConfig
	client KafkaProducerClient
	mu     sync.Mutex
	closed bool
}

// NewKafkaSink creates a KafkaSink instance. If client is nil, it defaults to MemoryKafkaProducer.
func NewKafkaSink(cfg KafkaOutputConfig, client KafkaProducerClient) *KafkaSink {
	if client == nil {
		client = NewMemoryKafkaProducer()
	}
	if cfg.Topic == "" {
		cfg.Topic = "worm.normalized.v1"
	}
	return &KafkaSink{
		cfg:    cfg,
		client: client,
	}
}

func (s *KafkaSink) Emit(ctx context.Context, event *model.NormalizedEvent) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("kafka sink is closed")
	}
	s.mu.Unlock()

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal normalized event: %w", err)
	}

	key := []byte(event.Worm.RawID)

	msg := &KafkaMessage{
		Topic:     s.cfg.Topic,
		Key:       key,
		Value:     data,
		Timestamp: time.Now().UTC(),
	}

	return s.client.Produce(ctx, msg)
}

func (s *KafkaSink) Flush(ctx context.Context) error {
	return s.client.Flush(ctx)
}

func (s *KafkaSink) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return s.client.Close()
}
