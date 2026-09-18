package packs

import (
	"errors"
	"fmt"
	"worm/internal/model"
)

var (
	// ErrNoMatch is returned when a decoded record matches zero active parser packs.
	ErrNoMatch = errors.New("no matching parser pack found (unknown_source)")

	// ErrAmbiguousMatch is returned when a decoded record matches more than one active parser pack.
	ErrAmbiguousMatch = errors.New("multiple parser packs matched (ambiguous_source)")
)

// ParserSnapshot represents an immutable runtime collection of active parser packs.
type ParserSnapshot interface {
	Match(decoded *model.DecodedRecord) (*ParserPack, error)
	GetPack(name string) (*ParserPack, bool)
	ListPacks() []*ParserPack
	Version() string
}

// Snapshot implements ParserSnapshot.
type Snapshot struct {
	version string
	packs   map[string]*ParserPack
	ordered []*ParserPack
}

// NewSnapshot builds an immutable snapshot from a slice of validated packs.
func NewSnapshot(version string, packs []*ParserPack) *Snapshot {
	packMap := make(map[string]*ParserPack, len(packs))
	ordered := make([]*ParserPack, len(packs))
	for i, p := range packs {
		packMap[p.Metadata.Name] = p
		ordered[i] = p
	}
	return &Snapshot{
		version: version,
		packs:   packMap,
		ordered: ordered,
	}
}

// Match evaluates all active packs against a decoded record.
// If exactly 1 pack matches, it is returned.
// If 0 packs match, ErrNoMatch is returned.
// If >1 packs match, ErrAmbiguousMatch is returned.
func (s *Snapshot) Match(decoded *model.DecodedRecord) (*ParserPack, error) {
	var matched []*ParserPack
	for _, p := range s.ordered {
		if p.Matches(decoded) {
			matched = append(matched, p)
		}
	}

	if len(matched) == 0 {
		return nil, ErrNoMatch
	}
	if len(matched) > 1 {
		names := make([]string, len(matched))
		for i, p := range matched {
			names[i] = p.Metadata.Name
		}
		return nil, fmt.Errorf("%w: %v", ErrAmbiguousMatch, names)
	}

	return matched[0], nil
}

// GetPack returns a pack by name.
func (s *Snapshot) GetPack(name string) (*ParserPack, bool) {
	p, ok := s.packs[name]
	return p, ok
}

// ListPacks returns all packs in this snapshot.
func (s *Snapshot) ListPacks() []*ParserPack {
	result := make([]*ParserPack, len(s.ordered))
	copy(result, s.ordered)
	return result
}

// Version returns the snapshot version string.
func (s *Snapshot) Version() string {
	return s.version
}
