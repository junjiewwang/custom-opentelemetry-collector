// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package storedmodel

import "strings"

// SanitizeKey normalizes an OTel attribute key for ES storage by compressing
// segment depth to ≤ 2 to prevent dynamic mapping conflicts.
//
// ES dynamic:true interprets dots (.) in field names as nested path separators.
// This causes mapper_parsing_exception when a dotted key in one document creates
// an intermediate object that a different document's attribute conflicts with.
//
// Example conflict:
//
//	Document A: "peer.service.source" → ES creates peer(ojb) → service(obj) → source(field)
//	Document B: "peer.service"        → ES tries peer.service = string, but peer.service is already an object
//
// Algorithm: keep the first dot, replace all subsequent dots with underscores.
//
//	"kind"               → "kind"
//	"http.method"         → "http.method"          (2 segments: unchanged)
//	"peer.service"        → "peer.service"         (2 segments: unchanged)
//	"peer.service.source" → "peer.service_source"  (3 segments: compress to 2)
//	"a.b.c.d"            → "a.b_c_d"              (4 segments: compress to 2)
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
	secondDotAbs := firstDot + 1 + secondDot   // absolute position of second dot
	prefix := key[:secondDotAbs]               // "peer.service"
	suffix := key[secondDotAbs+1:]             // "source" or "source.extra"
	return prefix + "_" + strings.ReplaceAll(suffix, ".", "_")
}
