package decode

import (
	"fmt"
	"sync"
	"worm/internal/model"
)

// Registry manages the collection of active format decoders.
type Registry struct {
	mu       sync.RWMutex
	decoders []Decoder
}

// NewRegistry creates an empty decoder registry.
func NewRegistry() *Registry {
	return &Registry{decoders: make([]Decoder, 0)}
}

// DefaultRegistry creates a registry populated with the standard MVP decoders.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register(NewSyslogDecoder())
	r.Register(NewJSONDecoder())
	r.Register(NewCSVDecoder())
	r.Register(NewCEFDecoder())
	r.Register(NewTextDecoder())
	return r
}

// Register adds a format decoder to the registry.
func (r *Registry) Register(d Decoder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decoders = append(r.decoders, d)
}

// Detect evaluates raw bytes across all registered decoders and returns
// the decoder with the highest confidence score above the threshold.
func (r *Registry) Detect(raw []byte) (Decoder, float64) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var bestDecoder Decoder
	var highestConfidence float64

	for _, d := range r.decoders {
		conf := d.Detect(raw)
		if conf > highestConfidence {
			highestConfidence = conf
			bestDecoder = d
		}
	}

	// 0.50 minimum confidence threshold
	if highestConfidence < 0.50 {
		return nil, 0.0
	}

	return bestDecoder, highestConfidence
}

// DetectAndDecode auto-detects the format and parses the raw bytes.
func (r *Registry) DetectAndDecode(raw []byte) (Decoder, []*model.DecodedRecord, error) {
	d, conf := r.Detect(raw)
	if d == nil || conf < 0.50 {
		return nil, nil, fmt.Errorf("no decoder recognized raw byte format (confidence below 0.50)")
	}

	records, err := d.Decode(raw)
	if err != nil {
		return d, nil, fmt.Errorf("%s decoder failed: %w", d.Name(), err)
	}

	return d, records, nil
}
