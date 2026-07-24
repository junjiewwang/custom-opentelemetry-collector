// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package storedmodel

import "strings"

// SanitizeKey normalizes an OTel attribute key for ES storage by compressing
// segment depth to ≤ 2 to prevent dynamic mapping conflicts.
//
// Used for traces/logs where 2-segment OTel keys (http.method) are intentional.
// For metric labels, use SanitizeMetricKey instead (replaces all dots).
func SanitizeKey(key string) string {
	// 0-1 segment: no dot, return as-is.
	firstDot := strings.IndexByte(key, '.')
	if firstDot < 0 {
		return key
	}
	// 2 segments: only one dot, return as-is.
	secondDot := strings.IndexByte(key[firstDot+1:], '.')
	if secondDot < 0 {
		return key
	}
	// ≥3 segments: keep the first dot, replace subsequent dots with underscores.
	secondDotAbs := firstDot + 1 + secondDot
	prefix := key[:secondDotAbs]
	suffix := key[secondDotAbs+1:]
	return prefix + "_" + strings.ReplaceAll(suffix, ".", "_")
}

// SanitizeMetricKey normalizes a metric label key for ES storage by replacing
// ALL dots with underscores. Unlike SanitizeKey (which preserves 2-segment
// dotted keys like http.method for OTel trace convention), metric labels
// must have zero dots because ES dynamic:true interprets dots as object nesting.
//
// This prevents conflicts between scalar and nested values of the same label:
//
//	Doc A: server = "my-svc"        → labels.server = "my-svc"
//	Doc B: server.address = "10.0"  → labels.server_address = "10.0" (no nesting)
//
//	"server"             → "server"
//	"server.address"     → "server_address"
//	"http.method"         → "http_method"
//	"peer.service.source" → "peer_service_source"
func SanitizeMetricKey(key string) string {
	return strings.ReplaceAll(SanitizeKey(key), ".", "_")
}
