package connections

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"worm/internal/ingest"
	"worm/internal/output"
)

type Secret struct {
	Token    string `json:"token"`
	Username string `json:"username"`
	Password string `json:"password"`
}
type SecretResolver struct{ Dir string }

func (r SecretResolver) Resolve(ref string) (Secret, error) {
	if ref == "" {
		return Secret{}, nil
	}
	key := "WORM_SECRET_" + strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(ref))
	raw := os.Getenv(key)
	if raw == "" && r.Dir != "" {
		b, err := os.ReadFile(filepath.Join(r.Dir, filepath.Base(ref)))
		if err == nil {
			raw = strings.TrimSpace(string(b))
		}
	}
	if raw == "" {
		return Secret{}, fmt.Errorf("secretRef %q not found in %s or %s", ref, key, r.Dir)
	}
	var v Secret
	if strings.HasPrefix(raw, "{") {
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return Secret{}, fmt.Errorf("decode secret %q: %w", ref, err)
		}
	} else if i := strings.IndexByte(raw, ':'); i > 0 {
		v.Username = raw[:i]
		v.Password = raw[i+1:]
	} else {
		v.Token = raw
	}
	return v, nil
}
func duration(v string, fallback time.Duration) (time.Duration, error) {
	if v == "" {
		return fallback, nil
	}
	d, e := time.ParseDuration(v)
	if e != nil {
		return 0, e
	}
	return d, nil
}
func tlsFiles(certFile, keyFile, caFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	c := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	if caFile != "" {
		b, e := os.ReadFile(caFile)
		if e != nil {
			return nil, e
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(b) {
			return nil, fmt.Errorf("no certificates in %s", caFile)
		}
		c.ClientCAs = pool
		c.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return c, nil
}
func connectionSecretRef(c *Connection) string {
	if c.Spec.SecretRef != "" {
		return c.Spec.SecretRef
	}
	if c.Spec.Endpoint != nil {
		return c.Spec.Endpoint.SecretRef
	}
	return ""
}
func kafkaSecret(s Secret) (string, string) {
	if s.Username != "" {
		return s.Username, s.Password
	}
	return "", ""
}

func CompileSource(c *Connection, resolver SecretResolver) (ingest.IngestAdapter, error) {
	sec, err := resolver.Resolve(connectionSecretRef(c))
	if err != nil {
		return nil, err
	}
	switch c.Spec.Type {
	case "cloud_pull":
		d, e := duration(c.Spec.Endpoint.Interval, 10*time.Second)
		if e != nil {
			return nil, e
		}
		to, e := duration(c.Spec.Endpoint.Timeout, 15*time.Second)
		if e != nil {
			return nil, e
		}
		token := sec.Token
		return ingest.NewCloudPoller(ingest.PollerConfig{Name: c.Metadata.Name, EndpointURL: c.Spec.Endpoint.URL, Interval: d, BatchSize: c.Spec.Endpoint.BatchSize, WatermarkPath: c.Spec.Endpoint.WatermarkPath, AuthHeader: c.Spec.Endpoint.AuthHeader, AuthToken: token, RequestTimeout: to}), nil
	case "kafka_input":
		topics := c.Spec.Topics
		if len(topics) == 0 {
			topics = []string{c.Spec.Topic}
		}
		u, p := kafkaSecret(sec)
		return ingest.NewBrokerKafkaAdapter(ingest.KafkaInputConfig{Name: c.Metadata.Name, Brokers: c.Spec.Brokers, Topics: topics, ConsumerGroup: c.Spec.ConsumerGroup, Username: u, Password: p})
	case "syslog_tls":
		cfg, e := tlsFiles(c.Spec.TLSCertFile, c.Spec.TLSKeyFile, c.Spec.ClientCAFile)
		if e != nil {
			return nil, e
		}
		return ingest.NewSyslogTLSListener(c.Spec.Listen, cfg), nil
	default:
		return nil, fmt.Errorf("unsupported source type %q", c.Spec.Type)
	}
}
func CompileSink(c *Connection, resolver SecretResolver) (output.OutputSink, error) {
	sec, err := resolver.Resolve(connectionSecretRef(c))
	if err != nil {
		return nil, err
	}
	switch c.Spec.Type {
	case "ndjson_output":
		return output.NewNDJSONSink(c.Spec.Path)
	case "parquet_output":
		return output.NewParquetSink(output.ParquetOutputConfig{Name: c.Metadata.Name, Path: c.Spec.Path, PartitionBy: c.Spec.PartitionBy, MaxRowsPerFile: c.Spec.MaxRowsPerFile, Compression: c.Spec.Compression, Manifest: c.Spec.Manifest}), nil
	case "kafka_output":
		linger, e := duration(c.Spec.Linger, 0)
		if e != nil {
			return nil, e
		}
		u, p := kafkaSecret(sec)
		return output.NewBrokerKafkaSink(output.KafkaOutputConfig{Name: c.Metadata.Name, Brokers: c.Spec.Brokers, Topic: c.Spec.Topic, KeyField: c.Spec.Key, Acks: c.Spec.Acks, Idempotent: c.Spec.Idempotent, Compression: c.Spec.Compression, BatchBytes: c.Spec.BatchBytes, Linger: linger, Username: u, Password: p})
	case "http_output":
		to, e := duration(c.Spec.Endpoint.Timeout, 10*time.Second)
		if e != nil {
			return nil, e
		}
		flush := time.Second
		batch := 100
		format := "ndjson"
		attempts := 3
		min, max := 100*time.Millisecond, 10*time.Second
		if c.Spec.Delivery != nil {
			format = c.Spec.Delivery.Format
			batch = c.Spec.Delivery.BatchEvents
			flush, e = duration(c.Spec.Delivery.FlushInterval, time.Second)
			if e != nil {
				return nil, e
			}
			if c.Spec.Delivery.Retry != nil {
				attempts = c.Spec.Delivery.Retry.MaxAttempts
				min, e = duration(c.Spec.Delivery.Retry.MinBackoff, min)
				if e != nil {
					return nil, e
				}
				max, e = duration(c.Spec.Delivery.Retry.MaxBackoff, max)
				if e != nil {
					return nil, e
				}
			}
		}
		token := sec.Token
		return output.NewHTTPSIEMSink(output.HTTPOutputConfig{Name: c.Metadata.Name, URL: c.Spec.Endpoint.URL, Format: format, BatchEvents: batch, FlushInterval: flush, Timeout: to, AuthHeader: c.Spec.Endpoint.AuthHeader, AuthToken: token, MaxRetries: attempts, MinBackoff: min, MaxBackoff: max}), nil
	default:
		return nil, fmt.Errorf("unsupported sink type %q", c.Spec.Type)
	}
}

type Runtime struct {
	Ingest         *ingest.Manager
	Output         *output.DynamicSink
	StaticAdapters []ingest.IngestAdapter
	StaticSinks    map[string]output.OutputSink
	Secrets        SecretResolver
}

func (r *Runtime) Reconcile(conns []*Connection) error {
	adapters := append([]ingest.IngestAdapter(nil), r.StaticAdapters...)
	sinks := map[string]output.OutputSink{}
	var dynamicAdapters []ingest.IngestAdapter
	var dynamicSinks []output.OutputSink
	cleanupCandidates := func() {
		for _, a := range dynamicAdapters {
			_ = a.Stop()
		}
		for _, s := range dynamicSinks {
			_ = s.Close()
		}
	}
	for k, v := range r.StaticSinks {
		sinks[k] = v
	}
	for _, c := range conns {
		if !c.Spec.Enabled {
			continue
		}
		if c.Kind == "Source" {
			a, e := CompileSource(c, r.Secrets)
			if e != nil {
				cleanupCandidates()
				return fmt.Errorf("compile source %s: %w", c.Metadata.Name, e)
			}
			adapters = append(adapters, a)
			dynamicAdapters = append(dynamicAdapters, a)
		} else {
			s, e := CompileSink(c, r.Secrets)
			if e != nil {
				cleanupCandidates()
				return fmt.Errorf("compile sink %s: %w", c.Metadata.Name, e)
			}
			sinks[c.Metadata.Name] = s
			dynamicSinks = append(dynamicSinks, s)
		}
	}
	if err := r.Ingest.Reconcile(adapters); err != nil {
		cleanupCandidates()
		return err
	}
	return r.Output.Replace(sinks)
}
func (r *Runtime) Check(ctx context.Context, c *Connection) error { return c.TestConnectivity(ctx) }
