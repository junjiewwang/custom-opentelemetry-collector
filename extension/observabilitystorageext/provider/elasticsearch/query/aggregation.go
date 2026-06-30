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
