// Package ocsf pins the offline WORM-supported OCSF 1.3.0 contract.
package ocsf

import "fmt"

const Version = "1.3.0"

var targets = map[string]string{
	"time": "number", "category_name": "string", "class_name": "string", "message": "string", "action": "string", "activity_name": "string", "status": "string", "severity_id": "number",
	"src_endpoint.ip": "string", "src_endpoint.port": "number", "dst_endpoint.ip": "string", "dst_endpoint.port": "number", "actor.user.name": "string", "actor.user.uid": "string", "device.hostname": "string", "finding_info.title": "string", "http_request.http_method": "string", "http_request.url.path": "string", "http_request.user_agent": "string", "http_request.version": "string", "http_response.status_code": "number", "service.name": "string", "traffic.bytes_in": "number", "traffic.bytes_out": "number", "dst_endpoint.hostname": "string", "transaction.id": "string", "transaction.amount": "number", "transaction.currency": "string",
}

var taxonomy = map[string][2]string{
	"network_device": {"Network Activity", "Network Activity"}, "server": {"Application Activity", "HTTP Activity"}, "os": {"Authentication", "Authentication"}, "endpoint": {"Findings", "Security Finding"}, "application": {"Application Activity", "Application Activity"}, "database": {"System Activity", "Database Activity"}, "cloud": {"Cloud Activity", "API Activity"}, "container": {"System Activity", "Container Lifecycle"}, "iam": {"Identity & Access Management", "Account Change"}, "iot": {"Device Activity", "Device Status"}, "other": {"Other Activity", "Base Event"},
}

func TargetAllowed(path string) bool { _, ok := targets[path]; return ok }
func ValidateTaxonomy(source, category, class string) error {
	want, ok := taxonomy[source]
	if !ok {
		return fmt.Errorf("unsupported source category %q", source)
	}
	if category != want[0] || class != want[1] {
		return fmt.Errorf("OCSF 1.3.0 taxonomy mismatch for %s: want %q/%q, got %q/%q", source, want[0], want[1], category, class)
	}
	return nil
}
func FieldType(path string) string { return targets[path] }
