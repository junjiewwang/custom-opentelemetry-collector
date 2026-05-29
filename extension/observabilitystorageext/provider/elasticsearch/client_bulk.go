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

// BulkIndex sends a bulk indexing request to ES.
// actions is a list of NDJSON lines (action + document pairs).
func (c *Client) BulkIndex(ctx context.Context, actions []byte) (*BulkResponse, error) {
	resp, err := c.doRequest(ctx, http.MethodPost, "/_bulk", actions)
	if err != nil {
		return nil, fmt.Errorf("bulk request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read bulk response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("bulk request returned status %d: %s", resp.StatusCode, string(body))
	}

	var bulkResp BulkResponse
	if err := json.Unmarshal(body, &bulkResp); err != nil {
		return nil, fmt.Errorf("failed to decode bulk response: %w", err)
	}
	return &bulkResp, nil
}

// BulkResponse represents the response from an ES bulk request.
type BulkResponse struct {
	Took   int  `json:"took"`
	Errors bool `json:"errors"`
	Items  []struct {
		Index *BulkItemResponse `json:"index,omitempty"`
	} `json:"items"`
}

// BulkItemResponse represents the response for a single item in a bulk request.
type BulkItemResponse struct {
	ID     string `json:"_id"`
	Result string `json:"result"`
	Status int    `json:"status"`
	Error  *struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
	} `json:"error,omitempty"`
}
