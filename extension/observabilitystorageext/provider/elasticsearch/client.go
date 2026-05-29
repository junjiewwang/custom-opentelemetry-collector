// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// Client wraps the HTTP client for Elasticsearch operations.
// It provides bulk indexing, index management, and cluster operations.
//
// Methods are organized across files by responsibility:
//   - client.go:       Core (struct, NewClient, Ping, ClusterHealth, HTTP transport)
//   - client_bulk.go:  Bulk indexing (BulkIndex, BulkResponse types)
//   - client_admin.go: Index management (Template, ILM, Delete, Count, Refresh, List, Stats)
type Client struct {
	httpClient *http.Client
	addresses  []string
	username   string
	password   string
	logger     *zap.Logger

	// Round-robin index for load balancing across ES nodes.
	nextAddr int
}

// NewClient creates a new ES HTTP client.
func NewClient(config *Config, logger *zap.Logger) (*Client, error) {
	return &Client{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		addresses: config.Addresses,
		username:  config.Username,
		password:  config.Password,
		logger:    logger.Named("es-client"),
	}, nil
}

// Ping verifies connectivity to the ES cluster.
func (c *Client) Ping(ctx context.Context) error {
	resp, err := c.doRequest(ctx, http.MethodGet, "/", nil)
	if err != nil {
		return fmt.Errorf("ping failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ping returned status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// ClusterHealth retrieves the cluster health status.
func (c *Client) ClusterHealth(ctx context.Context) (map[string]any, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/_cluster/health", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode cluster health: %w", err)
	}
	return result, nil
}

// doRequest executes an HTTP request to one of the ES nodes.
func (c *Client) doRequest(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	addr := c.getNextAddress()
	url := addr + path

	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request to %s failed: %w", url, err)
	}
	return resp, nil
}

// getNextAddress returns the next ES node address in round-robin fashion.
func (c *Client) getNextAddress() string {
	addr := c.addresses[c.nextAddr%len(c.addresses)]
	c.nextAddr++
	return addr
}
