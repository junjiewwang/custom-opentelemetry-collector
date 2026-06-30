// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package observabilitystorageext

import (
	"go.opentelemetry.io/collector/pdata/ptrace"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
)

// convertOTLPTraces converts ptrace.Traces to []StoredSpan.
// This is called at the extension layer so Provider implementations
// never need to import ptrace or understand OTLP proto.
func convertOTLPTraces(td ptrace.Traces) []StoredSpan {
	resourceSpans := td.ResourceSpans()
	var spans []StoredSpan
	for i := 0; i < resourceSpans.Len(); i++ {
		rs := resourceSpans.At(i)
		resource := rs.Resource()
		scopeSpans := rs.ScopeSpans()
		for j := 0; j < scopeSpans.Len(); j++ {
			ss := scopeSpans.At(j)
			sp := ss.Spans()
			for k := 0; k < sp.Len(); k++ {
				span := storedmodel.ConvertOTLPSpan(sp.At(k), ss, resource)
				spans = append(spans, span)
			}
		}
	}
	return spans
}
