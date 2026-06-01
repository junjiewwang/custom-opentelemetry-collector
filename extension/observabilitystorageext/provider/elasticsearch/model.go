// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"strings"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

// esTimestampFormat is the time format compatible with Elasticsearch's date type.
// ES supports up to millisecond precision in strict_date_optional_time format.
const esTimestampFormat = "2006-01-02T15:04:05.000Z"

// formatTimestamp converts a time.Time to an ES-compatible timestamp string (UTC, millisecond precision).
func formatTimestamp(t time.Time) string {
	return t.UTC().Format(esTimestampFormat)
}

// extractResourceAttributes extracts all resource attributes into a flat map.
func extractResourceAttributes(res pcommon.Resource) map[string]any {
	return attributesToMap(res.Attributes())
}

// getServiceName extracts the service name from trace resource attributes.
func getServiceName(res pcommon.Resource) string {
	if val, ok := res.Attributes().Get("service.name"); ok {
		return val.AsString()
	}
	return "unknown"
}

// getServiceNameFromResource extracts service name from metric resource.
func getServiceNameFromResource(res pcommon.Resource) string {
	if val, ok := res.Attributes().Get("service.name"); ok {
		return val.AsString()
	}
	return "unknown"
}

// getServiceNameFromResourceLogs extracts service name from log resource.
func getServiceNameFromResourceLogs(res pcommon.Resource) string {
	if val, ok := res.Attributes().Get("service.name"); ok {
		return val.AsString()
	}
	return "unknown"
}

// getAppID extracts the app ID from resource attributes.
// Checks "app_id" first, then "app.id". Returns empty string if not found.
func getAppID(res pcommon.Resource) string {
	if val, ok := res.Attributes().Get("app_id"); ok {
		if id := val.AsString(); id != "" {
			return sanitizeAppID(id)
		}
	}
	if val, ok := res.Attributes().Get("app.id"); ok {
		if id := val.AsString(); id != "" {
			return sanitizeAppID(id)
		}
	}
	return ""
}

// sanitizeAppID makes an app ID safe for use in ES index names.
// ES index names must be lowercase, cannot contain spaces or special characters
// like \, /, *, ?, ", <, >, |, #, comma.
func sanitizeAppID(id string) string {
	id = strings.ToLower(id)
	replacer := strings.NewReplacer(
		" ", "-",
		"/", "-",
		"\\", "-",
		"*", "-",
		"?", "-",
		"\"", "",
		"<", "",
		">", "",
		"|", "-",
		"#", "-",
		",", "-",
	)
	return replacer.Replace(id)
}

// attributesToMap converts pcommon.Map to a Go map[string]any.
func attributesToMap(attrs pcommon.Map) map[string]any {
	if attrs.Len() == 0 {
		return nil
	}
	result := make(map[string]any, attrs.Len())
	attrs.Range(func(k string, v pcommon.Value) bool {
		result[k] = valueToAny(v)
		return true
	})
	return result
}

// valueToAny converts a pcommon.Value to a native Go type.
func valueToAny(v pcommon.Value) any {
	switch v.Type() {
	case pcommon.ValueTypeStr:
		return v.Str()
	case pcommon.ValueTypeInt:
		return v.Int()
	case pcommon.ValueTypeDouble:
		return v.Double()
	case pcommon.ValueTypeBool:
		return v.Bool()
	case pcommon.ValueTypeMap:
		return attributesToMap(v.Map())
	case pcommon.ValueTypeSlice:
		slice := v.Slice()
		result := make([]any, slice.Len())
		for i := 0; i < slice.Len(); i++ {
			result[i] = valueToAny(slice.At(i))
		}
		return result
	case pcommon.ValueTypeBytes:
		return v.Bytes().AsRaw()
	default:
		return v.AsString()
	}
}


