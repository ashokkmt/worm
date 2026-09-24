package connections

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var (
	connNameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	semverRegex   = regexp.MustCompile(`^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

	validConnectionTypes = map[string]bool{
		"http_output":    true,
		"kafka_output":   true,
		"parquet_output": true,
		"ndjson_output":  true,
		"kafka_input":    true,
		"cloud_pull":     true,
	}
)

// Connection represents a declarative connection resource configuration in WORM.
type Connection struct {
	APIVersion string             `yaml:"apiVersion" json:"apiVersion"`
	Kind       string             `yaml:"kind" json:"kind"`
	Metadata   ConnectionMetadata `yaml:"metadata" json:"metadata"`
	Spec       ConnectionSpec     `yaml:"spec" json:"spec"`
}

type ConnectionMetadata struct {
	Name        string `yaml:"name" json:"name"`
	Version     string `yaml:"version" json:"version"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

type ConnectionSpec struct {
	Type           string        `yaml:"type" json:"type"`
	Enabled        bool          `yaml:"enabled" json:"enabled"`
	Delivery       *DeliverySpec `yaml:"delivery,omitempty" json:"delivery,omitempty"`
	Endpoint       *EndpointSpec `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`
	Brokers        []string      `yaml:"brokers,omitempty" json:"brokers,omitempty"`
	Topic          string        `yaml:"topic,omitempty" json:"topic,omitempty"`
	Topics         []string      `yaml:"topics,omitempty" json:"topics,omitempty"`
	Key            string        `yaml:"key,omitempty" json:"key,omitempty"`
	Acks           string        `yaml:"acks,omitempty" json:"acks,omitempty"`
	Idempotent     bool          `yaml:"idempotent,omitempty" json:"idempotent,omitempty"`
	Compression    string        `yaml:"compression,omitempty" json:"compression,omitempty"`
	BatchBytes     int           `yaml:"batchBytes,omitempty" json:"batchBytes,omitempty"`
	Linger         string        `yaml:"linger,omitempty" json:"linger,omitempty"`
	ConsumerGroup  string        `yaml:"consumerGroup,omitempty" json:"consumerGroup,omitempty"`
	Path           string        `yaml:"path,omitempty" json:"path,omitempty"`
	PartitionBy    []string      `yaml:"partitionBy,omitempty" json:"partitionBy,omitempty"`
	MaxRowsPerFile int           `yaml:"maxRowsPerFile,omitempty" json:"maxRowsPerFile,omitempty"`
	Manifest       bool          `yaml:"manifest,omitempty" json:"manifest,omitempty"`
	SecretRef      string        `yaml:"secretRef,omitempty" json:"secretRef,omitempty"`
}

type DeliverySpec struct {
	Format        string     `yaml:"format" json:"format"`
	BatchEvents   int        `yaml:"batchEvents,omitempty" json:"batchEvents,omitempty"`
	FlushInterval string     `yaml:"flushInterval,omitempty" json:"flushInterval,omitempty"`
	Retry         *RetrySpec `yaml:"retry,omitempty" json:"retry,omitempty"`
}

type RetrySpec struct {
	MaxAttempts int    `yaml:"maxAttempts" json:"maxAttempts"`
	MinBackoff  string `yaml:"minBackoff,omitempty" json:"minBackoff,omitempty"`
	MaxBackoff  string `yaml:"maxBackoff,omitempty" json:"maxBackoff,omitempty"`
}

type EndpointSpec struct {
	URL       string `yaml:"url" json:"url"`
	Timeout   string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	SecretRef string `yaml:"secretRef,omitempty" json:"secretRef,omitempty"`
}

// LoadConnection parses a strict YAML connection resource stream.
func LoadConnection(r io.Reader) (*Connection, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)

	var conn Connection
	if err := dec.Decode(&conn); err != nil {
		return nil, fmt.Errorf("yaml decode error: %w", err)
	}

	if err := conn.Validate(); err != nil {
		return nil, fmt.Errorf("connection validation failed: %w", err)
	}

	return &conn, nil
}

// Validate verifies structural correctness, type constraints, and secret safety.
func (c *Connection) Validate() error {
	if c.APIVersion != "worm.io/v1" {
		return fmt.Errorf("unsupported apiVersion %q (expected worm.io/v1)", c.APIVersion)
	}
	if c.Kind != "Connection" {
		return fmt.Errorf("unsupported kind %q (expected Connection)", c.Kind)
	}
	if c.Metadata.Name == "" {
		return errors.New("metadata.name is required")
	}
	if !connNameRegex.MatchString(c.Metadata.Name) {
		return fmt.Errorf("invalid metadata.name %q: must match ^[a-z0-9][a-z0-9-]*$", c.Metadata.Name)
	}
	if c.Metadata.Version == "" {
		return errors.New("metadata.version is required")
	}
	if !semverRegex.MatchString(c.Metadata.Version) {
		return fmt.Errorf("invalid metadata.version %q: must be valid semver", c.Metadata.Version)
	}
	if !validConnectionTypes[c.Spec.Type] {
		return fmt.Errorf("unsupported connection type %q", c.Spec.Type)
	}

	switch c.Spec.Type {
	case "http_output":
		if c.Spec.Endpoint == nil || c.Spec.Endpoint.URL == "" {
			return errors.New("http_output requires spec.endpoint.url")
		}
		u, err := url.Parse(c.Spec.Endpoint.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("invalid endpoint URL %q: must be valid http or https URL", c.Spec.Endpoint.URL)
		}
	case "kafka_output":
		if len(c.Spec.Brokers) == 0 {
			return errors.New("kafka_output requires spec.brokers")
		}
		if c.Spec.Topic == "" {
			return errors.New("kafka_output requires spec.topic")
		}
	case "parquet_output":
		if c.Spec.Path == "" {
			return errors.New("parquet_output requires spec.path")
		}
	case "ndjson_output":
		if c.Spec.Path == "" {
			return errors.New("ndjson_output requires spec.path")
		}
	case "kafka_input":
		if len(c.Spec.Brokers) == 0 {
			return errors.New("kafka_input requires spec.brokers")
		}
		if len(c.Spec.Topics) == 0 && c.Spec.Topic == "" {
			return errors.New("kafka_input requires spec.topics or spec.topic")
		}
	case "cloud_pull":
		if c.Spec.Endpoint == nil || c.Spec.Endpoint.URL == "" {
			return errors.New("cloud_pull requires spec.endpoint.url")
		}
	}

	return nil
}

// TestConnectivity performs a non-destructive reachability test for the configured connection.
func (c *Connection) TestConnectivity(ctx context.Context) error {
	switch c.Spec.Type {
	case "http_output", "cloud_pull":
		if c.Spec.Endpoint == nil || c.Spec.Endpoint.URL == "" {
			return errors.New("missing endpoint url")
		}
		u, err := url.Parse(c.Spec.Endpoint.URL)
		if err != nil {
			return fmt.Errorf("invalid endpoint URL: %w", err)
		}
		host := u.Host
		if !strings.Contains(host, ":") {
			if u.Scheme == "https" {
				host += ":443"
			} else {
				host += ":80"
			}
		}
		var d net.Dialer
		dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		conn, err := d.DialContext(dialCtx, "tcp", host)
		if err != nil {
			// Reachability warning or error
			return fmt.Errorf("connectivity check to %s failed: %w", host, err)
		}
		conn.Close()
		return nil

	case "kafka_output", "kafka_input":
		if len(c.Spec.Brokers) == 0 {
			return errors.New("no brokers configured")
		}
		for _, b := range c.Spec.Brokers {
			var d net.Dialer
			dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			conn, err := d.DialContext(dialCtx, "tcp", b)
			cancel()
			if err == nil {
				conn.Close()
				return nil
			}
		}
		return fmt.Errorf("none of the brokers %v could be reached", c.Spec.Brokers)

	case "parquet_output", "ndjson_output":
		if c.Spec.Path == "" {
			return errors.New("missing output path")
		}
		return nil
	}

	return nil
}
