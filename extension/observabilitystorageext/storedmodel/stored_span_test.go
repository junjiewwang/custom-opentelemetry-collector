// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package storedmodel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/collector/pdata/pcommon"
)

func TestToParentID(t *testing.T) {
	tests := []struct {
		name     string
		parentID pcommon.SpanID
		want     string
	}{
		{
			name:     "zero span ID (root span) returns empty",
			parentID: pcommon.NewSpanIDEmpty(),
			want:     "",
		},
		{
			name:     "non-zero span ID returns hex string",
			parentID: pcommon.SpanID([8]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}),
			want:     "0102030405060708",
		},
		{
			name:     "span ID with leading zeros returns full hex",
			parentID: pcommon.SpanID([8]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01}),
			want:     "0000000000000001",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toParentID(tt.parentID)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestToParentID_ZeroSpanIDString(t *testing.T) {
	// pcommon.SpanID zero value's String() returns "" (not "0000000000000000").
	// toParentID correctly identifies it as root via the s == "" check.
	zeroID := pcommon.NewSpanIDEmpty()
	assert.Equal(t, "", zeroID.String(), "zero SpanID String() returns empty")
	assert.Equal(t, "", toParentID(zeroID), "zero SpanID should be treated as root (empty parent)")
}
