package normalize

import (
	"errors"
	"fmt"
	"net"
	"time"
	"worm/internal/model"
)

// Validator enforces OCSF taxonomy contracts, required field presence,
// and type conformance on normalized events before sink emission.
type Validator struct{}

// NewValidator returns a new Validator instance.
func NewValidator() *Validator {
	return &Validator{}
}

// Validate validates an event against WORM governance and OCSF schema rules.
func (v *Validator) Validate(norm *model.NormalizedEvent) error {
	if norm == nil {
		return errors.New("normalized event is nil")
	}

	// 1. Mandatory WORM envelope provenance fields
	if norm.Worm.EventID == "" {
		return errors.New("missing mandatory worm.event_id")
	}
	if norm.Worm.RawID == "" {
		return errors.New("missing mandatory worm.raw_id")
	}
	if norm.Worm.RawSHA256 == "" {
		return errors.New("missing mandatory worm.raw_sha256")
	}

	// 2. Mandatory OCSF root fields
	if norm.OCSF == nil {
		return errors.New("missing OCSF event dictionary")
	}

	// Event timestamp is required in OCSF (must be epoch ms > 0)
	rawTime, hasTime := norm.OCSF["time"]
	if !hasTime || rawTime == nil {
		return errors.New("missing mandatory OCSF 'event.time' timestamp")
	}
	switch t := rawTime.(type) {
	case int64:
		if t <= 0 {
			return fmt.Errorf("invalid OCSF time timestamp: %d (must be > 0)", t)
		}
	case int:
		if t <= 0 {
			return fmt.Errorf("invalid OCSF time timestamp: %d (must be > 0)", t)
		}
	case float64:
		if t <= 0 {
			return fmt.Errorf("invalid OCSF time timestamp: %f (must be > 0)", t)
		}
	default:
		return fmt.Errorf("invalid OCSF time type %T (must be numeric epoch ms)", rawTime)
	}

	// 3. Endpoint IP and port conformance checks
	if srcEp, ok := norm.OCSF["src_endpoint"].(map[string]any); ok {
		if err := validateEndpoint("src_endpoint", srcEp); err != nil {
			return err
		}
	}
	if dstEp, ok := norm.OCSF["dst_endpoint"].(map[string]any); ok {
		if err := validateEndpoint("dst_endpoint", dstEp); err != nil {
			return err
		}
	}

	// 4. Severity validation (if present)
	if sev, exists := norm.OCSF["severity_id"]; exists && sev != nil {
		switch s := sev.(type) {
		case int64:
			if s < 0 || s > 10 {
				return fmt.Errorf("severity_id %d out of valid bounds (0..10)", s)
			}
		case int:
			if s < 0 || s > 10 {
				return fmt.Errorf("severity_id %d out of valid bounds (0..10)", s)
			}
		}
	}

	// 5. Audit trail stamp
	norm.Worm.ProcessingHistory = append(norm.Worm.ProcessingHistory, model.ProcessingStep{
		Stage:     "validate",
		Timestamp: time.Now().UTC(),
		Result:    "ok",
	})

	return nil
}

func validateEndpoint(name string, ep map[string]any) error {
	if rawIP, exists := ep["ip"]; exists && rawIP != nil {
		ipStr, ok := rawIP.(string)
		if !ok || net.ParseIP(ipStr) == nil {
			return fmt.Errorf("invalid IP address in %s: %v", name, rawIP)
		}
	}

	if rawPort, exists := ep["port"]; exists && rawPort != nil {
		var portNum int64
		switch p := rawPort.(type) {
		case int64:
			portNum = p
		case int:
			portNum = int64(p)
		case float64:
			portNum = int64(p)
		default:
			return fmt.Errorf("invalid port type in %s: %T", name, rawPort)
		}
		if portNum < 0 || portNum > 65535 {
			return fmt.Errorf("port %d out of valid range (0..65535) in %s", portNum, name)
		}
	}
	return nil
}
