// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package query

// IndexPattern returns the ES index pattern for the given prefix and appID.
// When appID is non-empty, returns an app-scoped pattern; otherwise a global wildcard.
//
//	prefix="otel-traces", appID=""     → "otel-traces-*"
//	prefix="otel-traces", appID="app1" → "otel-traces-app1-*"
func IndexPattern(prefix, appID string) string {
	if appID != "" {
		return prefix + "-" + appID + "-*"
	}
	return prefix + "-*"
}
