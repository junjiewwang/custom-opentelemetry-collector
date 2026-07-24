// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
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

	// Round-robin index for load balancing across ES nodes (atomic for concurrent access).
	nextAddr atomic.Int32
}

// defaultHTTPTransport returns a tuned HTTP transport for ES communication.
// Configures connection pooling to handle concurrent Grafana query bursts
// (6+ concurrent queries should not queue on TCP connections).
func defaultHTTPTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		MaxConnsPerHost:       20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// NewClient creates a new ES HTTP client with tuned connection pooling.
func NewClient(config *Config, logger *zap.Logger) (*Client, error) {
	if len(config.Addresses) == 0 {
		return nil, fmt.Errorf("elasticsearch config addresses must not be empty")
	}
	return &Client{
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: defaultHTTPTransport(),
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

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("cluster health returned status %d: %s", resp.StatusCode, string(body))
	}

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
// Uses atomic increment for safe concurrent access from multiple goroutines.
func (c *Client) getNextAddress() string {
	idx := int(c.nextAddr.Add(1)) % len(c.addresses)
	return c.addresses[idx]
}

// Close releases the HTTP connection pool's idle connections. It is safe to
// call after all in-flight requests have completed (e.g. from Provider.Shutdown
// once the writers are flushed and stopped). Calling it while requests are
// in flight does not interrupt them, but may force new connections afterward.
func (c *Client) Close() {
	c.httpClient.CloseIdleConnections()
}
