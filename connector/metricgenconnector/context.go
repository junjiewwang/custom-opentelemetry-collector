// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package metricgenconnector

import (
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// extractServiceName reads the service name from a resource.
func extractServiceName(resource pcommon.Resource) string {
	if v, ok := resource.Attributes().Get("service.name"); ok {
		return v.Str()
	}
	return ""
}

// extractAppID reads the app identifier from a resource.
func extractAppID(resource pcommon.Resource) string {
	for _, key := range []string{"app_id", "app.id"} {
		if v, ok := resource.Attributes().Get(key); ok && v.Str() != "" {
			return v.Str()
		}
	}
	return ""
}

// spanDuration returns the span duration in milliseconds.
func spanDuration(span ptrace.Span) float64 {
	start := span.StartTimestamp().AsTime()
	end := span.EndTimestamp().AsTime()
	return float64(end.Sub(start).Nanoseconds()) / 1e6
}
