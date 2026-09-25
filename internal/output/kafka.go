package output

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"sync"
	"time"
	"worm/internal/model"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/plain"
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
	Username    string        `json:"-"`
	Password    string        `json:"-"`
	TLSConfig   *tls.Config   `json:"-"`
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

// BrokerKafkaProducer is a synchronous acknowledged Kafka producer.
type BrokerKafkaProducer struct{ client *kgo.Client }

func NewBrokerKafkaProducer(cfg KafkaOutputConfig) (*BrokerKafkaProducer, error) {
	if len(cfg.Brokers) == 0 {
		return nil, fmt.Errorf("kafka output requires brokers")
	}
	opts := []kgo.Opt{kgo.SeedBrokers(cfg.Brokers...)}
	if cfg.Username != "" {
		opts = append(opts, kgo.SASL(plain.Auth{User: cfg.Username, Pass: cfg.Password}.AsMechanism()))
	}
	if cfg.TLSConfig != nil {
		opts = append(opts, kgo.DialTLSConfig(cfg.TLSConfig))
	}
	switch cfg.Acks {
	case "0":
		opts = append(opts, kgo.RequiredAcks(kgo.NoAck()))
	case "1":
		opts = append(opts, kgo.RequiredAcks(kgo.LeaderAck()))
	default:
		opts = append(opts, kgo.RequiredAcks(kgo.AllISRAcks()))
	}
	if !cfg.Idempotent {
		opts = append(opts, kgo.DisableIdempotentWrite())
	}
	switch cfg.Compression {
	case "none":
		opts = append(opts, kgo.ProducerBatchCompression(kgo.NoCompression()))
	case "gzip":
		opts = append(opts, kgo.ProducerBatchCompression(kgo.GzipCompression()))
	case "snappy":
		opts = append(opts, kgo.ProducerBatchCompression(kgo.SnappyCompression()))
	case "zstd":
		opts = append(opts, kgo.ProducerBatchCompression(kgo.ZstdCompression()))
	}
	if cfg.BatchBytes > 0 {
		opts = append(opts, kgo.ProducerBatchMaxBytes(int32(cfg.BatchBytes)))
	}
	if cfg.Linger > 0 {
		opts = append(opts, kgo.ProducerLinger(cfg.Linger))
	}
	client, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, err
	}
	return &BrokerKafkaProducer{client: client}, nil
}
func (b *BrokerKafkaProducer) Produce(ctx context.Context, msg *KafkaMessage) error {
	return b.client.ProduceSync(ctx, &kgo.Record{Topic: msg.Topic, Key: msg.Key, Value: msg.Value, Timestamp: msg.Timestamp}).FirstErr()
}
func (b *BrokerKafkaProducer) Flush(ctx context.Context) error { return b.client.Flush(ctx) }
func (b *BrokerKafkaProducer) Close() error                    { b.client.Close(); return nil }

// MemoryKafkaProducer is an explicit in-memory test double.
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

// NewKafkaSink creates a Kafka sink with an explicit client.
func NewKafkaSink(cfg KafkaOutputConfig, client KafkaProducerClient) *KafkaSink {
	if client == nil {
		panic("nil Kafka producer: use NewBrokerKafkaSink for production or an explicit test double")
	}
	if cfg.Topic == "" {
		cfg.Topic = "worm.normalized.v1"
	}
	return &KafkaSink{
		cfg:    cfg,
		client: client,
	}
}

func NewBrokerKafkaSink(cfg KafkaOutputConfig) (*KafkaSink, error) {
	client, err := NewBrokerKafkaProducer(cfg)
	if err != nil {
		return nil, err
	}
	return NewKafkaSink(cfg, client), nil
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

func (s *KafkaSink) Flush() error { return s.client.Flush(context.Background()) }

func (s *KafkaSink) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return s.client.Close()
}
