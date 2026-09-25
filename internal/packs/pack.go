package packs

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"worm/internal/model"
	"worm/internal/ocsf"

	"gopkg.in/yaml.v3"
)

// KnownSourceCategories defines valid categories in WORM.
var KnownSourceCategories = map[string]bool{
	"network_device": true,
	"server":         true,
	"os":             true,
	"endpoint":       true,
	"application":    true,
	"database":       true,
	"cloud":          true,
	"container":      true,
	"iam":            true,
	"iot":            true,
	"other":          true,
}

// KnownFormats defines valid format decoders.
var KnownFormats = map[string]bool{
	"syslog": true,
	"json":   true,
	"csv":    true,
	"cef":    true,
	"xml":    true,
	"leef":   true,
	"text":   true,
	"*":      true,
}

// ParserPack represents a declarative YAML source parsing definition.
type ParserPack struct {
	APIVersion string       `yaml:"apiVersion"`
	Kind       string       `yaml:"kind"`
	Metadata   PackMetadata `yaml:"metadata"`
	Spec       PackSpec     `yaml:"spec"`

	// compiled internal state
	compiledRegex *regexp.Regexp
}

// PackMetadata contains pack identification and versioning.
type PackMetadata struct {
	Name        string `yaml:"name"`
	Version     string `yaml:"version"`
	Description string `yaml:"description,omitempty"`
	Author      string `yaml:"author,omitempty"`
}

// DecodeOptions defines optional format-specific decoding parameters.
type DecodeOptions struct {
	XML *XMLDecodeOptions `yaml:"xml,omitempty"`
	CSV *CSVDecodeOptions `yaml:"csv,omitempty"`
}

// XMLDecodeOptions configures XML record extraction and namespace resolution.
type XMLDecodeOptions struct {
	RecordPath       string            `yaml:"recordPath,omitempty"`
	AttributePrefix  string            `yaml:"attributePrefix,omitempty"`
	TextKey          string            `yaml:"textKey,omitempty"`
	NamespaceAliases map[string]string `yaml:"namespaceAliases,omitempty"`
}

// CSVDecodeOptions configures CSV parsing parameters.
type CSVDecodeOptions struct {
	Delimiter string `yaml:"delimiter,omitempty"`
	HasHeader bool   `yaml:"hasHeader,omitempty"`
}

// PackSpec defines source categorization, format, match rules, and field extractions.
type PackSpec struct {
	SourceCategory   string               `yaml:"sourceCategory"`
	Format           string               `yaml:"format"`
	Match            MatchRule            `yaml:"match"`
	Decode           *DecodeOptions       `yaml:"decode,omitempty"`
	Fields           map[string]FieldRule `yaml:"fields"`
	Map              map[string]string    `yaml:"map"`
	PreserveUnmapped bool                 `yaml:"preserveUnmapped"`
	Fixtures         []string             `yaml:"fixtures,omitempty"`
}

// MatchRule defines criteria for assigning a decoded record to this parser pack.
type MatchRule struct {
	Contains    string            `yaml:"contains,omitempty"`
	Regex       string            `yaml:"regex,omitempty"`
	FieldEquals map[string]string `yaml:"fieldEquals,omitempty"`
}

// FieldRule defines the extraction path and type casting for a source field.
type FieldRule struct {
	From     string `yaml:"from"`
	Type     string `yaml:"type"` // "ip", "integer", "float", "timestamp", "string", "boolean"
	Required bool   `yaml:"required,omitempty"`
	Layout   string `yaml:"layout,omitempty"`
}

// LoadPack parses a YAML stream into a ParserPack, rejecting unknown keys.
func LoadPack(r io.Reader) (*ParserPack, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)

	var pack ParserPack
	if err := dec.Decode(&pack); err != nil {
		return nil, fmt.Errorf("yaml decode error: %w", err)
	}

	if err := pack.Validate(); err != nil {
		return nil, fmt.Errorf("pack validation failed: %w", err)
	}

	return &pack, nil
}

var (
	packNameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	semverRegex   = regexp.MustCompile(`^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)
)

// Validate verifies structural correctness and compiles internal expressions.
func (p *ParserPack) Validate() error {
	if p.APIVersion != "worm.io/v1" {
		return fmt.Errorf("unsupported apiVersion %q (expected worm.io/v1)", p.APIVersion)
	}
	if p.Kind != "LogSource" {
		return fmt.Errorf("unsupported kind %q (expected LogSource)", p.Kind)
	}
	if p.Metadata.Name == "" {
		return errors.New("metadata.name is required")
	}
	if !packNameRegex.MatchString(p.Metadata.Name) {
		return fmt.Errorf("invalid metadata.name %q: must match ^[a-z0-9][a-z0-9-]*$", p.Metadata.Name)
	}
	if p.Metadata.Version == "" {
		return errors.New("metadata.version is required")
	}
	if !semverRegex.MatchString(p.Metadata.Version) {
		return fmt.Errorf("invalid metadata.version %q: must be valid semver (e.g. 1.0.0)", p.Metadata.Version)
	}
	if !KnownSourceCategories[p.Spec.SourceCategory] {
		return fmt.Errorf("unknown sourceCategory: %q", p.Spec.SourceCategory)
	}
	if !KnownFormats[p.Spec.Format] {
		return fmt.Errorf("unknown format: %q", p.Spec.Format)
	}

	// Match rules must have at least one criteria
	hasMatch := p.Spec.Match.Contains != "" ||
		p.Spec.Match.Regex != "" ||
		len(p.Spec.Match.FieldEquals) > 0
	if !hasMatch {
		return errors.New("spec.match must declare at least one of: contains, regex, or fieldEquals")
	}

	if p.Spec.Match.Regex != "" {
		re, err := regexp.Compile(p.Spec.Match.Regex)
		if err != nil {
			return fmt.Errorf("invalid match.regex: %w", err)
		}
		p.compiledRegex = re
	}

	for name, f := range p.Spec.Fields {
		if f.From == "" {
			return fmt.Errorf("field %q missing 'from' path", name)
		}
		switch f.Type {
		case "ip", "integer", "float", "timestamp", "string", "boolean":
			// valid
		default:
			return fmt.Errorf("field %q has unsupported type %q", name, f.Type)
		}
	}

	for src, dst := range p.Spec.Map {
		if !strings.HasPrefix(dst, "event.") {
			return fmt.Errorf("spec.map target %q must start with 'event.'", dst)
		}
		if !ocsf.TargetAllowed(strings.TrimPrefix(dst, "event.")) {
			return fmt.Errorf("spec.map target %q is not governed by pinned OCSF %s", dst, ocsf.Version)
		}
		if _, ok := p.Spec.Fields[src]; !ok {
			return fmt.Errorf("spec.map source %q is not defined in spec.fields", src)
		}
	}

	return nil
}

// Matches checks whether a DecodedRecord satisfies this pack's format and match rules.
func (p *ParserPack) Matches(rec *model.DecodedRecord) bool {
	if rec == nil {
		return false
	}

	// 1. Format check
	if p.Spec.Format != "" && p.Spec.Format != "*" {
		if !formatMatches(p.Spec.Format, rec.Format) {
			return false
		}
	}

	// 2. Contains check
	if p.Spec.Match.Contains != "" {
		needle := p.Spec.Match.Contains
		rawStr := string(rec.RawPayload)
		matched := strings.Contains(rawStr, needle)
		if !matched {
			// Also check if any decoded field value contains it
			for _, v := range rec.Fields {
				if strings.Contains(fmt.Sprintf("%v", v), needle) {
					matched = true
					break
				}
			}
		}
		if !matched {
			return false
		}
	}

	// 3. FieldEquals check
	if len(p.Spec.Match.FieldEquals) > 0 {
		for k, expectedVal := range p.Spec.Match.FieldEquals {
			actualVal, found := lookupField(rec, k)
			if !found {
				return false
			}
			if fmt.Sprintf("%v", actualVal) != expectedVal {
				return false
			}
		}
	}

	// 4. Regex check
	if p.compiledRegex != nil {
		targetText := string(rec.RawPayload)
		if msg, ok := rec.Fields["message"].(string); ok && msg != "" {
			targetText = msg
		}
		if !p.compiledRegex.MatchString(targetText) {
			return false
		}
	}

	return true
}

// formatMatches checks if the decoded record's specific format matches the pack's declared format family.
func formatMatches(packFormat, recFormat string) bool {
	if packFormat == recFormat {
		return true
	}
	switch packFormat {
	case "syslog":
		return strings.HasPrefix(recFormat, "syslog")
	case "text":
		return strings.HasPrefix(recFormat, "text")
	case "json":
		return strings.HasPrefix(recFormat, "json")
	case "csv":
		return strings.HasPrefix(recFormat, "csv")
	case "cef":
		return strings.HasPrefix(recFormat, "cef")
	case "xml":
		return strings.HasPrefix(recFormat, "xml")
	case "leef":
		return strings.HasPrefix(recFormat, "leef")
	}
	return false
}

// LookupNested traverses nested map structures using dot-separated paths (e.g. "user.id" or "meta.host").
func LookupNested(m map[string]any, path string) (any, bool) {
	if m == nil || path == "" {
		return nil, false
	}
	// 1. Direct match first (for keys that contain dots)
	if v, ok := m[path]; ok {
		return v, true
	}
	// 2. Traversal by dot separation
	parts := strings.Split(path, ".")
	var current any = m
	for _, part := range parts {
		switch node := current.(type) {
		case map[string]any:
			val, ok := node[part]
			if !ok {
				return nil, false
			}
			current = val
		case map[any]any:
			val, ok := node[part]
			if !ok {
				return nil, false
			}
			current = val
		default:
			return nil, false
		}
	}
	return current, true
}

// LookupField resolves a dot-separated path within a DecodedRecord's Fields or Headers.
// Supports nested maps, payload prefixes, and direct keys.
func LookupField(rec *model.DecodedRecord, key string) (any, bool) {
	if rec == nil {
		return nil, false
	}
	trimmed := strings.TrimPrefix(key, "payload.")
	if v, ok := LookupNested(rec.Fields, key); ok {
		return v, true
	}
	if v, ok := LookupNested(rec.Fields, trimmed); ok {
		return v, true
	}
	if v, ok := LookupNested(rec.Headers, key); ok {
		return v, true
	}
	if v, ok := LookupNested(rec.Headers, trimmed); ok {
		return v, true
	}
	return nil, false
}

// lookupField is an internal alias for LookupField.
func lookupField(rec *model.DecodedRecord, key string) (any, bool) {
	return LookupField(rec, key)
}
