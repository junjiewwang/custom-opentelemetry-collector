// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package observabilitystorageext

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
)

// TestStoredSpanToPublic_NormalizesKindAndStatus is a regression test for the
// bug where ES stores short-form enum strings ("Client", "Unset") but
// StoredSpanToPublic cast them verbatim (SpanKind("Client")), which never
// matched the uppercase OTel enum constants ("SPAN_KIND_CLIENT"). The downstream
// spanKindToInt/statusCodeToInt switches then fell through to the 0/unspecified
// branch and the `json:"kind,omitempty"` tag dropped the field — Grafana's span
// tree showed no span.kind.
func TestStoredSpanToPublic_NormalizesKindAndStatus(t *testing.T) {
	ss := storedmodel.StoredSpan{
		TraceID: "trace",
		SpanID:  "span",
		Name:    "SELECT x",
		Kind:    "Client", // ES short form
		Status: storedmodel.StoredStatus{
			Code: "Unset", // ES short form
		},
	}
	got := StoredSpanToPublic(ss)
	assert.Equal(t, SpanKindClient, got.Kind)
	assert.Equal(t, StatusCodeUnset, got.Status.Code)
}

// TestStoredSpanToPublic_KindAlreadyEnum is a control: an already-normalized
// uppercase enum must pass through unchanged (idempotent).
func TestStoredSpanToPublic_KindAlreadyEnum(t *testing.T) {
	ss := storedmodel.StoredSpan{
		Kind: "SPAN_KIND_SERVER",
		Status: storedmodel.StoredStatus{
			Code: "STATUS_CODE_OK",
		},
	}
	got := StoredSpanToPublic(ss)
	assert.Equal(t, SpanKindServer, got.Kind)
	assert.Equal(t, StatusCodeOk, got.Status.Code)
}
