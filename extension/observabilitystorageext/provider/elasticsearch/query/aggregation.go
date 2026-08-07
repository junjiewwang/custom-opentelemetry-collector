// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"encoding/json"
	"fmt"
)

// TermsAggBucket represents a single bucket from an ES terms aggregation.
type TermsAggBucket struct {
	Key      string `json:"key"`
	DocCount int64  `json:"doc_count"`
}

// UnmarshalJSON reads a bucket key of any scalar type as a string.
//
// ES echoes the key in the field's own type, so aggregating a numeric field —
// a dynamically mapped OTel attribute such as http.response.status_code or
// server.port — yields `"key": 200`, not `"key": "200"`. Decoding straight into
// a string field failed the entire response, so /label/{name}/values returned
// an empty list for every numeric label and the breakdown value picker was
// blank for them.
func (b *TermsAggBucket) UnmarshalJSON(data []byte) error {
	var raw struct {
		Key      json.RawMessage `json:"key"`
		DocCount int64           `json:"doc_count"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	b.DocCount = raw.DocCount

	if len(raw.Key) == 0 {
		b.Key = ""
		return nil
	}
	var s string
	if err := json.Unmarshal(raw.Key, &s); err == nil {
		b.Key = s
		return nil
	}
	// Numbers and booleans: the literal JSON text is the value we want.
	// Objects and arrays are not legal bucket keys.
	if raw.Key[0] == '{' || raw.Key[0] == '[' {
		return fmt.Errorf("terms bucket key is not a scalar: %s", raw.Key)
	}
	b.Key = string(raw.Key)
	return nil
}

// ParseTermsAgg extracts string keys from a raw terms aggregation response.
// Returns nil if the raw message is empty or missing.
//
// Usage:
//
//	raw, ok := resp.Aggregations["services"]
//	if !ok { return nil, nil }
//	keys, err := query.ParseTermsAgg(raw)
func ParseTermsAgg(raw json.RawMessage) ([]string, error) {
	var agg struct {
		Buckets []TermsAggBucket `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &agg); err != nil {
		return nil, fmt.Errorf("parse terms aggregation: %w", err)
	}
	keys := make([]string, 0, len(agg.Buckets))
	for _, b := range agg.Buckets {
		keys = append(keys, b.Key)
	}
	return keys, nil
}

// ParseTermsAggWithCount extracts string keys with their document counts.
func ParseTermsAggWithCount(raw json.RawMessage) ([]TermsAggBucket, error) {
	var agg struct {
		Buckets []TermsAggBucket `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &agg); err != nil {
		return nil, fmt.Errorf("parse terms aggregation: %w", err)
	}
	return agg.Buckets, nil
}
