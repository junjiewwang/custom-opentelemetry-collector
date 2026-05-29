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

// PutIndexTemplate creates or updates an index template.
func (c *Client) PutIndexTemplate(ctx context.Context, name string, template map[string]any) error {
	body, err := json.Marshal(template)
	if err != nil {
		return fmt.Errorf("failed to marshal index template: %w", err)
	}

	resp, err := c.doRequest(ctx, http.MethodPut, fmt.Sprintf("/_index_template/%s", name), body)
	if err != nil {
		return fmt.Errorf("put index template failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("put index template returned status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// PutILMPolicy creates or updates an ILM policy.
func (c *Client) PutILMPolicy(ctx context.Context, name string, policy map[string]any) error {
	body, err := json.Marshal(policy)
	if err != nil {
		return fmt.Errorf("failed to marshal ILM policy: %w", err)
	}

	resp, err := c.doRequest(ctx, http.MethodPut, fmt.Sprintf("/_ilm/policy/%s", name), body)
	if err != nil {
		return fmt.Errorf("put ILM policy failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("put ILM policy returned status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// DeleteByQuery deletes documents matching the given query in the specified index pattern.
func (c *Client) DeleteByQuery(ctx context.Context, indexPattern string, query map[string]any) (int64, error) {
	body, err := json.Marshal(map[string]any{"query": query})
	if err != nil {
		return 0, fmt.Errorf("failed to marshal delete query: %w", err)
	}

	path := fmt.Sprintf("/%s/_delete_by_query?conflicts=proceed", indexPattern)
	resp, err := c.doRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return 0, fmt.Errorf("delete by query failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("failed to read delete response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("delete by query returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Deleted int64 `json:"deleted"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return 0, fmt.Errorf("failed to decode delete response: %w", err)
	}
	return result.Deleted, nil
}

// GetIndicesStats retrieves index-level statistics.
func (c *Client) GetIndicesStats(ctx context.Context, indexPattern string) (map[string]any, error) {
	path := fmt.Sprintf("/%s/_stats", indexPattern)
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode indices stats: %w", err)
	}
	return result, nil
}

// Count returns the number of documents matching the given query in the specified index pattern.
// If query is nil, it counts all documents.
func (c *Client) Count(ctx context.Context, indexPattern string, query map[string]any) (int64, error) {
	var body []byte
	if query != nil {
		var err error
		body, err = json.Marshal(map[string]any{"query": query})
		if err != nil {
			return 0, fmt.Errorf("failed to marshal count query: %w", err)
		}
	}

	path := fmt.Sprintf("/%s/_count", indexPattern)
	resp, err := c.doRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return 0, fmt.Errorf("count request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("failed to read count response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("count returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Count int64 `json:"count"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return 0, fmt.Errorf("failed to decode count response: %w", err)
	}
	return result.Count, nil
}

// RefreshIndex forces a refresh on the specified index pattern, making recent writes searchable.
func (c *Client) RefreshIndex(ctx context.Context, indexPattern string) error {
	path := fmt.Sprintf("/%s/_refresh", indexPattern)
	resp, err := c.doRequest(ctx, http.MethodPost, path, nil)
	if err != nil {
		return fmt.Errorf("refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("refresh returned status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// ListIndices returns the names of indices matching the given pattern.
func (c *Client) ListIndices(ctx context.Context, indexPattern string) ([]string, error) {
	path := fmt.Sprintf("/_cat/indices/%s?h=index&format=json", indexPattern)
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("list indices request failed: %w", err)
	}
	defer resp.Body.Close()

	// 404 means no indices match, return empty slice.
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list indices returned status %d: %s", resp.StatusCode, string(body))
	}

	var items []struct {
		Index string `json:"index"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("failed to decode list indices response: %w", err)
	}

	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Index)
	}
	return names, nil
}

// DeleteIndex deletes the specified index by exact name.
// If the index doesn't exist, it returns nil (idempotent).
func (c *Client) DeleteIndex(ctx context.Context, indexName string) error {
	path := fmt.Sprintf("/%s", indexName)
	resp, err := c.doRequest(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return fmt.Errorf("delete index request failed: %w", err)
	}
	defer resp.Body.Close()

	// 404 is acceptable (index doesn't exist)
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete index returned status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// DeleteIndicesByPattern lists indices matching the pattern and deletes them one by one.
// This works even when ES has action.destructive_requires_name=true.
func (c *Client) DeleteIndicesByPattern(ctx context.Context, indexPattern string) error {
	indices, err := c.ListIndices(ctx, indexPattern)
	if err != nil {
		return err
	}
	for _, idx := range indices {
		if err := c.DeleteIndex(ctx, idx); err != nil {
			return fmt.Errorf("failed to delete index %s: %w", idx, err)
		}
	}
	return nil
}
