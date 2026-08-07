// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"encoding/json"
	"testing"
)

// ES echoes a terms bucket key in the field's own type, so a numeric field
// yields `"key": 200`. Decoding into a plain string failed the whole response,
// which is why /label/{name}/values was empty for every numeric OTel attribute.
func TestParseTermsAgg_NonStringKeys(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"string keys", `{"buckets":[{"key":"a"},{"key":"b"}]}`, []string{"a", "b"}},
		{"integer keys", `{"buckets":[{"key":200},{"key":401}]}`, []string{"200", "401"}},
		{"float key", `{"buckets":[{"key":1.5}]}`, []string{"1.5"}},
		{"bool key", `{"buckets":[{"key":true}]}`, []string{"true"}},
		{"mixed", `{"buckets":[{"key":"ok"},{"key":500}]}`, []string{"ok", "500"}},
		{"empty", `{"buckets":[]}`, []string{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseTermsAgg(json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestParseTermsAgg_KeepsDocCountWithNumericKey(t *testing.T) {
	buckets, err := ParseTermsAggWithCount(json.RawMessage(`{"buckets":[{"key":200,"doc_count":42}]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(buckets) != 1 || buckets[0].Key != "200" || buckets[0].DocCount != 42 {
		t.Fatalf("got %+v", buckets)
	}
}

func TestParseTermsAgg_RejectsNonScalarKey(t *testing.T) {
	if _, err := ParseTermsAgg(json.RawMessage(`{"buckets":[{"key":{"nested":1}}]}`)); err == nil {
		t.Fatal("expected an error for an object bucket key")
	}
}
