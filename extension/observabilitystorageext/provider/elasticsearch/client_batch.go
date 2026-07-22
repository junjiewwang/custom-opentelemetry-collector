// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import "context"

// Searcher is the minimal interface for ES search operations.
// Both *Client and future batch-capable search implementations
// satisfy this interface, allowing readers to accept either.
type Searcher interface {
	Search(ctx context.Context, indexPattern string, req *SearchRequest) (*SearchResponse, error)
}

// Compile-time check that Client satisfies Searcher.
var _ Searcher = (*Client)(nil)
