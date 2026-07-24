// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"encoding/json"
	"sync"
)

// fakeSearcher is a test double for the Searcher interface.
// It returns a canned *SearchResponse for each Search call and records the
// requests it received, so reader unit tests can assert on the emitted query
// without a live ES cluster.
//
// Callers populate Responses (in call order) before invoking the reader.
// Each response may be either a *SearchResponse (used directly) or a
// JSON-serializable map/struct (marshaled then unmarshaled into a
// *SearchResponse, which is how ES results actually arrive as RawMessage
// aggregations).
type fakeSearcher struct {
	mu        sync.Mutex
	Responses []any // []any, each is *SearchResponse or JSON-serializable
	calls     int
	Err       error

	// Recorded requests, in call order.
	LastIndexPattern string
	LastRequest      *SearchRequest
	Requests         []*SearchRequest
}

func (f *fakeSearcher) Search(ctx context.Context, indexPattern string, req *SearchRequest) (*SearchResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.LastIndexPattern = indexPattern
	f.LastRequest = req
	f.Requests = append(f.Requests, req)

	if f.Err != nil {
		return nil, f.Err
	}
	if f.calls >= len(f.Responses) {
		// Default: empty response rather than panic, so readers that make
		// an unexpected extra call fail predictably with empty data.
		return &SearchResponse{}, nil
	}
	resp := f.Responses[f.calls]
	f.calls++

	switch v := resp.(type) {
	case *SearchResponse:
		return v, nil
	default:
		// Marshal then unmarshal so aggregation RawMessages are populated
		// exactly as a real ES JSON body would be.
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		var sr SearchResponse
		if err := json.Unmarshal(raw, &sr); err != nil {
			return nil, err
		}
		return &sr, nil
	}
}

// callsMade returns the number of Search calls recorded so far.
func (f *fakeSearcher) callsMade() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// Compile-time check that fakeSearcher satisfies Searcher.
var _ Searcher = (*fakeSearcher)(nil)
