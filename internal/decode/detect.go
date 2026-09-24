package decode

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"worm/internal/model"
)

// ErrAmbiguousFormat indicates that multiple format decoders matched with identical top confidence.
var ErrAmbiguousFormat = errors.New("ambiguous format detection: multiple decoders matched with equal top confidence")

// CandidateScore records an evaluation result for a candidate decoder.
type CandidateScore struct {
	Name       string
	Decoder    Decoder
	Confidence float64
}

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

// DetectRanked evaluates raw bytes across all registered decoders and returns
// candidate scores sorted by confidence in descending order.
func (r *Registry) DetectRanked(raw []byte) []CandidateScore {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var candidates []CandidateScore
	for _, d := range r.decoders {
		conf := d.Detect(raw)
		if conf >= 0.50 {
			candidates = append(candidates, CandidateScore{
				Name:       d.Name(),
				Decoder:    d,
				Confidence: conf,
			})
		}
	}

	// Sort candidates: highest confidence first.
	// For equal confidence, structured decoders (syslog, json, cef, csv) precede generic "text".
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Confidence != candidates[j].Confidence {
			return candidates[i].Confidence > candidates[j].Confidence
		}
		if candidates[i].Name == "text" && candidates[j].Name != "text" {
			return false
		}
		if candidates[j].Name == "text" && candidates[i].Name != "text" {
			return true
		}
		// Syslog envelope has outer precedence
		if candidates[i].Name == "syslog" {
			return true
		}
		return candidates[i].Name < candidates[j].Name
	})

	return candidates
}

// Detect evaluates raw bytes across all registered decoders and returns
// the winning decoder with the highest confidence score above the threshold.
func (r *Registry) Detect(raw []byte) (Decoder, float64) {
	candidates := r.DetectRanked(raw)
	if len(candidates) == 0 {
		return nil, 0.0
	}
	return candidates[0].Decoder, candidates[0].Confidence
}

// DetectAndDecode auto-detects the format and parses the raw bytes.
func (r *Registry) DetectAndDecode(raw []byte) (Decoder, []*model.DecodedRecord, error) {
	candidates := r.DetectRanked(raw)
	if len(candidates) == 0 {
		return nil, nil, fmt.Errorf("no decoder recognized raw byte format (confidence below 0.50)")
	}

	best := candidates[0]

	// Check for true ambiguity between multiple top non-text decoders with equal score
	if len(candidates) > 1 && candidates[0].Confidence == candidates[1].Confidence {
		if candidates[0].Name != "text" && candidates[1].Name != "text" && candidates[0].Name != "syslog" {
			return nil, nil, fmt.Errorf("%w: candidates %s and %s both scored %0.2f",
				ErrAmbiguousFormat, candidates[0].Name, candidates[1].Name, candidates[0].Confidence)
		}
	}

	records, err := best.Decoder.Decode(raw)
	if err != nil {
		return best.Decoder, nil, fmt.Errorf("%s decoder failed: %w", best.Name, err)
	}

	return best.Decoder, records, nil
}
