// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// SearchRequest represents an ES _search request body.
type SearchRequest struct {
	Query        map[string]any   `json:"query,omitempty"`
	Sort         []map[string]any `json:"sort,omitempty"`
	From         int              `json:"from,omitempty"`
	Size         int              `json:"size"`
	Source       any              `json:"_source,omitempty"`
	Aggregations map[string]any   `json:"aggs,omitempty"`
	SearchAfter  []any            `json:"search_after,omitempty"`
}

// SearchResponse represents an ES _search response.
type SearchResponse struct {
	Took     int  `json:"took"`
	TimedOut bool `json:"timed_out"`
	Hits     struct {
		Total struct {
			Value    int64  `json:"value"`
			Relation string `json:"relation"`
		} `json:"total"`
		Hits []SearchHit `json:"hits"`
	} `json:"hits"`
	Aggregations map[string]json.RawMessage `json:"aggregations,omitempty"`
}

// SearchHit represents a single hit in search results.
type SearchHit struct {
	ID     string          `json:"_id"`
	Index  string          `json:"_index"`
	Score  *float64        `json:"_score"`
	Source json.RawMessage `json:"_source"`
	Sort   []any           `json:"sort,omitempty"`
}

// Search executes a search request against the specified index pattern.
func (c *Client) Search(ctx context.Context, indexPattern string, req *SearchRequest) (*SearchResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal search request: %w", err)
	}

	path := fmt.Sprintf("/%s/_search", indexPattern)
	resp, err := c.doRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read search response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("search returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var searchResp SearchResponse
	if err := json.Unmarshal(respBody, &searchResp); err != nil {
		return nil, fmt.Errorf("failed to decode search response: %w", err)
	}
	return &searchResp, nil
}

// MultiSearch executes multiple search requests in a single call (msearch API).
func (c *Client) MultiSearch(ctx context.Context, requests []MultiSearchItem) ([]SearchResponse, error) {
	var ndjson []byte
	for _, item := range requests {
		header, err := json.Marshal(map[string]any{"index": item.Index})
		if err != nil {
			return nil, fmt.Errorf("failed to marshal msearch header: %w", err)
		}
		body, err := json.Marshal(item.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal msearch body: %w", err)
		}
		ndjson = append(ndjson, header...)
		ndjson = append(ndjson, '\n')
		ndjson = append(ndjson, body...)
		ndjson = append(ndjson, '\n')
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/_msearch", ndjson)
	if err != nil {
		return nil, fmt.Errorf("msearch request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read msearch response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("msearch returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var msearchResp struct {
		Responses []SearchResponse `json:"responses"`
	}
	if err := json.Unmarshal(respBody, &msearchResp); err != nil {
		return nil, fmt.Errorf("failed to decode msearch response: %w", err)
	}
	return msearchResp.Responses, nil
}

// MultiSearchItem represents a single search within a multi-search request.
type MultiSearchItem struct {
	Index string
	Body  *SearchRequest
}
