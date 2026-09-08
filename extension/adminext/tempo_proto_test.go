// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
	v1trace "go.opentelemetry.io/proto/otlp/trace/v1"
)

// TestConvertTraceToProtobuf_AlwaysSetsStatus is a regression test for the bug
// where spans with UNSET status were emitted with Status=nil, which made
// Grafana 12.0.1's tempo trace_transform.go dereference a nil Status and 500
// ("invalid memory address or nil pointer dereference"). UNSET status must be
// emitted as an explicit Status{code=UNSET} rather than omitted.
func TestConvertTraceToProtobuf_AlwaysSetsStatus(t *testing.T) {
	trace := &observabilitystorageext.Trace{
		TraceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Spans: []observabilitystorageext.Span{
			{
				TraceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				SpanID:  "bbbbbbbbbbbbbbbb",
				Name:    "unset-status-span",
				Kind:    observabilitystorageext.SpanKindClient,
				// Status left at zero value → Code == "".
			},
		},
	}

	bytes, err := convertTraceToProtobuf(trace)
	require.NoError(t, err)

	// convertTraceToProtobuf returns raw TracesData (the TraceByIDResponse
	// envelope is added separately by wrapAsTraceByIDResponse in the handler).
	var td v1trace.TracesData
	require.NoError(t, proto.Unmarshal(bytes, &td))
	require.Len(t, td.ResourceSpans, 1)
	require.Len(t, td.ResourceSpans[0].ScopeSpans, 1)
	require.Len(t, td.ResourceSpans[0].ScopeSpans[0].Spans, 1)

	span := td.ResourceSpans[0].ScopeSpans[0].Spans[0]
	require.NotNil(t, span.Status, "span.Status must be non-nil even for UNSET status")
	assert.Equal(t, v1trace.Status_STATUS_CODE_UNSET, span.Status.Code)
}
