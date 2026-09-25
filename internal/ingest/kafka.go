package ingest

import (
	"context"
	"crypto/tls"
	"fmt"
	"sync"
	"time"
	"worm/internal/model"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/plain"
)

// KafkaInputConfig defines parameters for consuming raw datagrams from an Apache Kafka topic.
type KafkaInputConfig struct {
	Name          string        `json:"name"`
	Brokers       []string      `json:"brokers"`
	Topics        []string      `json:"topics"`
	ConsumerGroup string        `json:"consumer_group"`
	PollTimeout   time.Duration `json:"poll_timeout"`
	Username      string        `json:"-"`
	Password      string        `json:"-"`
	TLSConfig     *tls.Config   `json:"-"`
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

// BrokerKafkaConsumer is a real Apache Kafka consumer with auto-commit disabled.
type BrokerKafkaConsumer struct{ client *kgo.Client }

func NewBrokerKafkaConsumer(cfg KafkaInputConfig) (*BrokerKafkaConsumer, error) {
	if len(cfg.Brokers) == 0 || len(cfg.Topics) == 0 || cfg.ConsumerGroup == "" {
		return nil, fmt.Errorf("kafka input requires brokers, topics, and consumer group")
	}
	opts := []kgo.Opt{kgo.SeedBrokers(cfg.Brokers...), kgo.ConsumeTopics(cfg.Topics...), kgo.ConsumerGroup(cfg.ConsumerGroup), kgo.DisableAutoCommit(), kgo.BlockRebalanceOnPoll()}
	if cfg.Username != "" {
		opts = append(opts, kgo.SASL(plain.Auth{User: cfg.Username, Pass: cfg.Password}.AsMechanism()))
	}
	if cfg.TLSConfig != nil {
		opts = append(opts, kgo.DialTLSConfig(cfg.TLSConfig))
	}
	client, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, err
	}
	return &BrokerKafkaConsumer{client: client}, nil
}

func (b *BrokerKafkaConsumer) Poll(ctx context.Context, timeout time.Duration) ([]*KafkaConsumerRecord, error) {
	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	fetches := b.client.PollFetches(pollCtx)
	if errs := fetches.Errors(); len(errs) > 0 {
		if pollCtx.Err() != nil && ctx.Err() == nil {
			return nil, nil
		}
		return nil, fmt.Errorf("kafka fetch: %v", errs)
	}
	result := make([]*KafkaConsumerRecord, 0, fetches.NumRecords())
	fetches.EachRecord(func(r *kgo.Record) {
		result = append(result, &KafkaConsumerRecord{Topic: r.Topic, Partition: int(r.Partition), Offset: r.Offset, Key: append([]byte(nil), r.Key...), Value: append([]byte(nil), r.Value...), Timestamp: r.Timestamp})
	})
	return result, nil
}
func (b *BrokerKafkaConsumer) CommitOffset(ctx context.Context, topic string, partition int, offset int64) error {
	return b.client.CommitRecords(ctx, &kgo.Record{Topic: topic, Partition: int32(partition), Offset: offset})
}
func (b *BrokerKafkaConsumer) Close() error { b.client.Close(); return nil }

// MemoryKafkaConsumer is an explicit thread-safe test double for Kafka ingestion.
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

// NewKafkaAdapter creates an adapter. Production callers must pass a broker client;
// tests may pass MemoryKafkaConsumer explicitly.
func NewKafkaAdapter(cfg KafkaInputConfig, client KafkaConsumerClient) *KafkaAdapter {
	if cfg.PollTimeout <= 0 {
		cfg.PollTimeout = 250 * time.Millisecond
	}
	if client == nil {
		panic("nil Kafka consumer: use NewBrokerKafkaAdapter for production or an explicit test double")
	}
	return &KafkaAdapter{
		cfg:    cfg,
		client: client,
	}
}

func NewBrokerKafkaAdapter(cfg KafkaInputConfig) (*KafkaAdapter, error) {
	if len(cfg.Topics) == 0 {
		return nil, fmt.Errorf("kafka input requires at least one topic")
	}
	client, err := NewBrokerKafkaConsumer(cfg)
	if err != nil {
		return nil, err
	}
	return NewKafkaAdapter(cfg, client), nil
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
				RawBytes: msg.Value, Transport: "kafka_input", SourceIP: "kafka://" + msg.Topic,
				ReceivedAt: time.Now().UTC(), Ack: ackChan,
				Metadata: model.ReceiveMetadata{ConnectionID: a.cfg.Name, KafkaTopic: msg.Topic, KafkaPartition: msg.Partition, KafkaOffset: msg.Offset, KafkaKey: string(msg.Key)},
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
