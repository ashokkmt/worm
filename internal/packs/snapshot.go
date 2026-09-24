package packs

import (
	"crypto/sha256"
	"encoding/hex"
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
	Digest() string
}

// Snapshot implements ParserSnapshot.
type Snapshot struct {
	version string
	digest  string
	packs   map[string]*ParserPack
	ordered []*ParserPack
}

// NewSnapshot builds an immutable snapshot from a slice of validated packs.
// It rejects duplicate pack names and computes a deterministic SHA-256 digest.
func NewSnapshot(version string, packs []*ParserPack) (*Snapshot, error) {
	packMap := make(map[string]*ParserPack, len(packs))
	ordered := make([]*ParserPack, 0, len(packs))
	h := sha256.New()
	fmt.Fprintf(h, "version:%s\n", version)

	for _, p := range packs {
		if p == nil {
			return nil, errors.New("cannot create snapshot with nil parser pack")
		}
		if _, exists := packMap[p.Metadata.Name]; exists {
			return nil, fmt.Errorf("duplicate parser pack name %q in snapshot", p.Metadata.Name)
		}
		packMap[p.Metadata.Name] = p
		ordered = append(ordered, p)
		fmt.Fprintf(h, "pack:%s:%s:%s:%s\n", p.Metadata.Name, p.Metadata.Version, p.Spec.SourceCategory, p.Spec.Format)
	}

	return &Snapshot{
		version: version,
		digest:  hex.EncodeToString(h.Sum(nil)),
		packs:   packMap,
		ordered: ordered,
	}, nil
}

// MustNewSnapshot builds a snapshot or panics on error (convenient for fixtures/tests).
func MustNewSnapshot(version string, packs []*ParserPack) *Snapshot {
	snap, err := NewSnapshot(version, packs)
	if err != nil {
		panic(err)
	}
	return snap
}

// Digest returns the SHA-256 digest of this snapshot's pack configuration.
func (s *Snapshot) Digest() string {
	return s.digest
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
