// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"net/url"
	"strings"
)

var sensitiveQueryKeys = map[string]bool{
	"appname":              true,
	"token":                true,
	"authorization":        true,
	"api_key":              true,
	"apikey":               true,
	"password":             true,
	"secret":               true,
	"clientconnectionid":   true,
	"client_connection_id": true,
}

// sanitizeQueryForLog returns a log-safe representation of a WS query string.
// Control-plane tokens may be carried in appName/token query parameters, so
// those values are replaced with a placeholder while harmless routing fields
// (method, id, service) stay visible for diagnostics.
func sanitizeQueryForLog(rawQuery string) string {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "[unparsable query]"
	}
	parts := make([]string, 0, len(values))
	for key, vals := range values {
		if sensitiveQueryKeys[strings.ToLower(key)] {
			parts = append(parts, key+"=<redacted>")
			continue
		}
		for _, v := range vals {
			parts = append(parts, key+"="+v)
		}
	}
	return strings.Join(parts, "&")
}

// sanitizeRequestURIForLog sanitizes the query part of a full request URI.
func sanitizeRequestURIForLog(requestURI string) string {
	idx := strings.IndexByte(requestURI, '?')
	if idx < 0 {
		return requestURI
	}
	return requestURI[:idx+1] + sanitizeQueryForLog(requestURI[idx+1:])
}
