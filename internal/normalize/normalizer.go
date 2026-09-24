package normalize

import (
	"crypto/rand"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"worm/internal/model"
	"worm/internal/packs"
)

// Normalizer standardizes decoded records into canonical OCSF taxonomy events
// and stamps the immutable WORM governance envelope.
type Normalizer struct {
	coreVersion   string
	schemaVersion string
	eventSeq      atomic.Uint64
}

// NewNormalizer creates a new Normalizer instance.
func NewNormalizer() *Normalizer {
	return &Normalizer{
		coreVersion:   "0.1.0",
		schemaVersion: "1.3.0",
	}
}

// Normalize applies the semantic mapping defined by the matched parser pack,
// constructs the OCSF hierarchical event tree, preserves unmapped fields,
// and stamps the complete WORM envelope.
func (n *Normalizer) Normalize(
	raw *model.RawEvent,
	decoded *model.DecodedRecord,
	pack *packs.ParserPack,
	history []model.ProcessingStep,
) (*model.NormalizedEvent, error) {
	if raw == nil || decoded == nil || pack == nil {
		return nil, fmt.Errorf("raw event, decoded record, and parser pack must not be nil")
	}

	// 1. Extract and cast fields according to the pack's field specifications
	extracted, unmapped, warnings, err := packs.ExtractAndConvert(pack, decoded)
	if err != nil {
		return nil, fmt.Errorf("field extraction failed: %w", err)
	}

	// 2. Build hierarchical OCSF event dictionary from pack mapping rules
	ocsfEvent := make(map[string]any)
	for srcKey, targetPath := range pack.Spec.Map {
		val, exists := extracted[srcKey]
		if !exists || val == nil {
			continue
		}

		// Target paths in YAML are formatted as "event.src_endpoint.ip"
		cleanedPath := strings.TrimPrefix(targetPath, "event.")
		setNestedValue(ocsfEvent, cleanedPath, val)
	}

	// Default event.time to raw receive time if not explicitly provided by source payload
	if t, ok := ocsfEvent["time"]; !ok || t == nil {
		if !raw.ReceivedAt.IsZero() {
			ocsfEvent["time"] = raw.ReceivedAt.UnixMilli()
		} else {
			ocsfEvent["time"] = time.Now().UTC().UnixMilli()
		}
	}

	// 3. Preserve unmapped vendor attributes
	if pack.Spec.PreserveUnmapped && len(unmapped) > 0 {
		ocsfEvent["unmapped"] = unmapped
	}

	// 4. Populate standard OCSF taxonomy defaults if not explicitly set
	enrichTaxonomyDefaults(ocsfEvent, pack.Spec.SourceCategory)

	// 5. Determine source identifier
	sourceID := raw.SourceIP
	if host, ok := decoded.Headers["syslog_host"].(string); ok && host != "" && host != "-" {
		sourceID = host
	} else if host, ok := decoded.Fields["syslog_host"].(string); ok && host != "" && host != "-" {
		sourceID = host
	} else if host, ok := decoded.Fields["host"].(string); ok && host != "" {
		sourceID = host
	} else if host, ok := decoded.Fields["server"].(string); ok && host != "" {
		sourceID = host
	}
	if sourceID == "" {
		sourceID = "unknown"
	}

	// 6. Generate unique event ID: worm-evt-YYYYMMDD-<seq>-<rand>
	seq := n.eventSeq.Add(1)
	var randBytes [4]byte
	_, _ = rand.Read(randBytes[:])
	eventID := fmt.Sprintf("worm-evt-%s-%06d-%x", time.Now().Format("20060102"), seq, randBytes)

	// 7. Clone and append processing history
	var auditTrail []model.ProcessingStep
	if len(history) > 0 {
		auditTrail = make([]model.ProcessingStep, len(history))
		copy(auditTrail, history)
	}
	auditTrail = append(auditTrail, model.ProcessingStep{
		Stage:     "normalize",
		Timestamp: time.Now().UTC(),
		Result:    "ok",
	})

	// 8. Construct WORM envelope
	envelope := model.WormEnvelope{
		EventID:           eventID,
		RawID:             raw.RawID,
		RawSHA256:         raw.RawSHA256,
		RawBytes:          raw.ByteCount,
		RecordOrdinal:     decoded.RecordOrdinal,
		SourceCategory:    pack.Spec.SourceCategory,
		SourceID:          sourceID,
		ParserPack:        pack.Metadata.Name,
		ParserVersion:     pack.Metadata.Version,
		CoreVersion:       n.coreVersion,
		SchemaVersion:     n.schemaVersion,
		ReceivedTime:      raw.ReceivedAt,
		Status:            "normalized",
		Warnings:          warnings,
		ProcessingHistory: auditTrail,
	}
	if envelope.Warnings == nil {
		envelope.Warnings = []string{}
	}

	return &model.NormalizedEvent{
		Worm: envelope,
		OCSF: ocsfEvent,
	}, nil
}

// setNestedValue navigates/creates intermediate maps for dot-separated keys (e.g. "src_endpoint.ip").
func setNestedValue(m map[string]any, path string, val any) {
	parts := strings.Split(path, ".")
	curr := m
	for i := 0; i < len(parts)-1; i++ {
		key := parts[i]
		sub, exists := curr[key]
		if !exists {
			subMap := make(map[string]any)
			curr[key] = subMap
			curr = subMap
		} else if subMap, ok := sub.(map[string]any); ok {
			curr = subMap
		} else {
			// Collision: overwrite with map to preserve hierarchical structure
			subMap := make(map[string]any)
			curr[key] = subMap
			curr = subMap
		}
	}
	curr[parts[len(parts)-1]] = val
}

// enrichTaxonomyDefaults provides OCSF standard class/category defaults based on source category.
// Covers all 11 supported WORM source categories.
func enrichTaxonomyDefaults(event map[string]any, category string) {
	switch category {
	case "network_device":
		if _, exists := event["category_name"]; !exists {
			event["category_name"] = "Network Activity"
		}
		if _, exists := event["class_name"]; !exists {
			event["class_name"] = "Network Activity"
		}
		if _, exists := event["type_name"]; !exists {
			event["type_name"] = "Network Activity: Traffic"
		}
	case "server":
		if _, exists := event["category_name"]; !exists {
			event["category_name"] = "Application Activity"
		}
		if _, exists := event["class_name"]; !exists {
			event["class_name"] = "HTTP Activity"
		}
		if _, exists := event["type_name"]; !exists {
			event["type_name"] = "HTTP Activity: HTTP Request"
		}
	case "os":
		if _, exists := event["category_name"]; !exists {
			event["category_name"] = "Authentication"
		}
		if _, exists := event["class_name"]; !exists {
			event["class_name"] = "Authentication"
		}
		if _, exists := event["type_name"]; !exists {
			event["type_name"] = "Authentication: Logon"
		}
	case "endpoint":
		if _, exists := event["category_name"]; !exists {
			event["category_name"] = "Findings"
		}
		if _, exists := event["class_name"]; !exists {
			event["class_name"] = "Security Finding"
		}
		if _, exists := event["type_name"]; !exists {
			event["type_name"] = "Security Finding: Alert"
		}
	case "application":
		if _, exists := event["category_name"]; !exists {
			event["category_name"] = "Application Activity"
		}
		if _, exists := event["class_name"]; !exists {
			event["class_name"] = "Application Activity"
		}
		if _, exists := event["type_name"]; !exists {
			event["type_name"] = "Application Activity: Generic"
		}
	case "database":
		if _, exists := event["category_name"]; !exists {
			event["category_name"] = "System Activity"
		}
		if _, exists := event["class_name"]; !exists {
			event["class_name"] = "Database Activity"
		}
		if _, exists := event["type_name"]; !exists {
			event["type_name"] = "Database Activity: Query"
		}
	case "cloud":
		if _, exists := event["category_name"]; !exists {
			event["category_name"] = "Cloud Activity"
		}
		if _, exists := event["class_name"]; !exists {
			event["class_name"] = "API Activity"
		}
		if _, exists := event["type_name"]; !exists {
			event["type_name"] = "Cloud Activity: API Call"
		}
	case "container":
		if _, exists := event["category_name"]; !exists {
			event["category_name"] = "System Activity"
		}
		if _, exists := event["class_name"]; !exists {
			event["class_name"] = "Container Lifecycle"
		}
		if _, exists := event["type_name"]; !exists {
			event["type_name"] = "Container Activity: Lifecycle"
		}
	case "iam":
		if _, exists := event["category_name"]; !exists {
			event["category_name"] = "Identity & Access Management"
		}
		if _, exists := event["class_name"]; !exists {
			event["class_name"] = "Account Change"
		}
		if _, exists := event["type_name"]; !exists {
			event["type_name"] = "IAM: Account Modification"
		}
	case "iot":
		if _, exists := event["category_name"]; !exists {
			event["category_name"] = "Device Activity"
		}
		if _, exists := event["class_name"]; !exists {
			event["class_name"] = "Device Status"
		}
		if _, exists := event["type_name"]; !exists {
			event["type_name"] = "Device Activity: Status Report"
		}
	case "other":
		if _, exists := event["category_name"]; !exists {
			event["category_name"] = "Other Activity"
		}
		if _, exists := event["class_name"]; !exists {
			event["class_name"] = "Base Event"
		}
		if _, exists := event["type_name"]; !exists {
			event["type_name"] = "Other: Generic"
		}
	}
}
