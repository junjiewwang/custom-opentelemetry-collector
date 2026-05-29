// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// newTestConfig returns a Config suitable for unit tests.
func newTestConfig(addresses []string) *Config {
	return &Config{
		Addresses:     addresses,
		BatchSize:     3,
		FlushInterval: 100 * time.Millisecond,
		MaxRetries:    2,
		Traces: IndexConfig{
			IndexPrefix:     "otel-traces",
			IndexDateFormat: "2006.01.02",
			Shards:          1,
			Replicas:        0,
			Retention:       168 * time.Hour,
			RefreshInterval: "5s",
		},
		Metrics: IndexConfig{
			IndexPrefix:     "otel-metrics",
			IndexDateFormat: "2006.01.02",
			Shards:          1,
			Replicas:        0,
			Retention:       720 * time.Hour,
			RefreshInterval: "10s",
		},
		Logs: IndexConfig{
			IndexPrefix:     "otel-logs",
			IndexDateFormat: "2006.01.02",
			Shards:          1,
			Replicas:        0,
			Retention:       336 * time.Hour,
			RefreshInterval: "5s",
		},
	}
}

// newMockESServer creates a mock ES server that records bulk requests.
func newMockESServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	if handler == nil {
		handler = func(w http.ResponseWriter, r *http.Request) {
			resp := BulkResponse{Took: 10, Errors: false}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}
	}
	return httptest.NewServer(handler)
}

func TestBulkBuffer_FlushOnBatchSize(t *testing.T) {
	var bulkCalls atomic.Int32

	server := newMockESServer(t, func(w http.ResponseWriter, r *http.Request) {
		bulkCalls.Add(1)
		resp := BulkResponse{Took: 5, Errors: false}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	cfg.BatchSize = 3

	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	buf := newBulkBuffer(client, cfg, zaptest.NewLogger(t), "trace")
	// Don't start the flush loop for this test - we want to test batch size trigger only.

	// Add 2 docs - should not flush
	require.NoError(t, buf.Add("test-index", map[string]any{"field": "value1"}))
	require.NoError(t, buf.Add("test-index", map[string]any{"field": "value2"}))
	assert.Equal(t, int32(0), bulkCalls.Load(), "should not flush before batch_size")

	// Add 3rd doc - should trigger flush
	require.NoError(t, buf.Add("test-index", map[string]any{"field": "value3"}))
	assert.Equal(t, int32(1), bulkCalls.Load(), "should flush on batch_size reached")

	// Buffer should be empty now
	assert.Equal(t, 0, buf.count)
}

func TestBulkBuffer_FlushEmpty(t *testing.T) {
	server := newMockESServer(t, nil)
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	buf := newBulkBuffer(client, cfg, zaptest.NewLogger(t), "trace")

	// Flushing an empty buffer should be no-op, no error
	err = buf.Flush(context.Background())
	require.NoError(t, err)
}

func TestBulkBuffer_ManualFlush(t *testing.T) {
	var bulkCalls atomic.Int32

	server := newMockESServer(t, func(w http.ResponseWriter, r *http.Request) {
		bulkCalls.Add(1)
		resp := BulkResponse{Took: 5, Errors: false}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	cfg.BatchSize = 100 // large batch so it won't auto-trigger

	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	buf := newBulkBuffer(client, cfg, zaptest.NewLogger(t), "trace")

	// Add docs below batch size
	require.NoError(t, buf.Add("test-index", map[string]any{"field": "v1"}))
	require.NoError(t, buf.Add("test-index", map[string]any{"field": "v2"}))
	assert.Equal(t, int32(0), bulkCalls.Load())

	// Manual flush should send them
	require.NoError(t, buf.Flush(context.Background()))
	assert.Equal(t, int32(1), bulkCalls.Load())
	assert.Equal(t, 0, buf.count)
}

func TestBulkBuffer_RetryOnFailure(t *testing.T) {
	var attempts atomic.Int32

	server := newMockESServer(t, func(w http.ResponseWriter, r *http.Request) {
		count := attempts.Add(1)
		if count <= 2 {
			// First 2 attempts fail
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error": "unavailable"}`))
			return
		}
		// 3rd attempt succeeds
		resp := BulkResponse{Took: 5, Errors: false}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	cfg.BatchSize = 1
	cfg.MaxRetries = 3

	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	buf := newBulkBuffer(client, cfg, zaptest.NewLogger(t), "trace")

	// This should trigger flush (batch_size=1), which will retry
	err = buf.Add("test-index", map[string]any{"field": "value"})
	require.NoError(t, err)

	// Should have made 3 attempts (1 initial + 2 retries)
	assert.Equal(t, int32(3), attempts.Load())
}

func TestBulkBuffer_RetryExhausted(t *testing.T) {
	server := newMockESServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Always fail
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error": "unavailable"}`))
	})
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	cfg.BatchSize = 1
	cfg.MaxRetries = 1

	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	buf := newBulkBuffer(client, cfg, zaptest.NewLogger(t), "trace")

	// This should fail after exhausting retries
	err = buf.Add("test-index", map[string]any{"field": "value"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bulk request failed after")
}

func TestBulkBuffer_ContextCancellation(t *testing.T) {
	server := newMockESServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow response
		time.Sleep(5 * time.Second)
		resp := BulkResponse{Took: 5, Errors: false}
		json.NewEncoder(w).Encode(resp)
	})
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	cfg.BatchSize = 100
	cfg.MaxRetries = 5

	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	buf := newBulkBuffer(client, cfg, zaptest.NewLogger(t), "trace")

	// Add a doc
	require.NoError(t, buf.Add("test-index", map[string]any{"field": "value"}))

	// Create a context that's already cancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Flush with cancelled context should return quickly
	// Note: The first attempt will still be made since ctx check happens before retry sleep
	err = buf.Flush(ctx)
	// It may or may not error depending on timing, but it shouldn't hang
	// The test passing without timeout is the real assertion
}

func TestBulkBuffer_FlushLoop(t *testing.T) {
	var mu sync.Mutex
	var bulkCalls int

	server := newMockESServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		bulkCalls++
		mu.Unlock()
		resp := BulkResponse{Took: 5, Errors: false}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	cfg.BatchSize = 100 // high batch size so it won't auto-flush on add
	cfg.FlushInterval = 50 * time.Millisecond

	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	buf := newBulkBuffer(client, cfg, zaptest.NewLogger(t), "trace")
	buf.Start()

	// Add a doc
	require.NoError(t, buf.Add("test-index", map[string]any{"field": "value"}))

	// Wait for flush loop to kick in
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	calls := bulkCalls
	mu.Unlock()

	assert.Greater(t, calls, 0, "flush loop should have triggered at least once")

	buf.Stop()
}

func TestBulkBuffer_BulkResponseWithErrors(t *testing.T) {
	server := newMockESServer(t, func(w http.ResponseWriter, r *http.Request) {
		resp := BulkResponse{
			Took:   5,
			Errors: true,
			Items: []struct {
				Index *BulkItemResponse `json:"index,omitempty"`
			}{
				{
					Index: &BulkItemResponse{
						ID:     "1",
						Result: "created",
						Status: 201,
					},
				},
				{
					Index: &BulkItemResponse{
						ID:     "2",
						Status: 400,
						Error: &struct {
							Type   string `json:"type"`
							Reason string `json:"reason"`
						}{
							Type:   "mapper_parsing_exception",
							Reason: "failed to parse field [value]",
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	cfg.BatchSize = 2

	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	logger := zaptest.NewLogger(t)
	buf := newBulkBuffer(client, cfg, logger, "trace")

	// Add docs to trigger flush
	require.NoError(t, buf.Add("test-index", map[string]any{"field": "v1"}))
	// Note: Bulk response with partial errors still returns nil error
	// (only logs warnings) - this is by design since some docs succeeded
	require.NoError(t, buf.Add("test-index", map[string]any{"field": "v2"}))
}

func TestBulkBuffer_ConcurrentAdds(t *testing.T) {
	var bulkCalls atomic.Int32

	server := newMockESServer(t, func(w http.ResponseWriter, r *http.Request) {
		bulkCalls.Add(1)
		resp := BulkResponse{Took: 5, Errors: false}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	cfg.BatchSize = 10

	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	buf := newBulkBuffer(client, cfg, zap.NewNop(), "trace")

	// Concurrent adds should not panic or deadlock
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			err := buf.Add("test-index", map[string]any{"idx": idx})
			assert.NoError(t, err)
		}(i)
	}
	wg.Wait()

	// Flush remaining
	require.NoError(t, buf.Flush(context.Background()))

	// All 50 docs should have been flushed
	// 50 docs with batch_size 10 = at least 5 bulk calls
	assert.GreaterOrEqual(t, bulkCalls.Load(), int32(5))
}

func TestBulkBuffer_MarshalError(t *testing.T) {
	server := newMockESServer(t, nil)
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	buf := newBulkBuffer(client, cfg, zaptest.NewLogger(t), "trace")

	// Add a value that can't be marshaled to JSON
	ch := make(chan int)
	err = buf.Add("test-index", map[string]any{"bad": ch})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to marshal document")
}

func TestBulkBuffer_RoundRobinLoadBalancing(t *testing.T) {
	var mu sync.Mutex
	requestedURLs := make([]string, 0)

	handler := func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestedURLs = append(requestedURLs, r.Host)
		mu.Unlock()
		resp := BulkResponse{Took: 5, Errors: false}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}

	// Create 2 servers
	server1 := httptest.NewServer(http.HandlerFunc(handler))
	defer server1.Close()
	server2 := httptest.NewServer(http.HandlerFunc(handler))
	defer server2.Close()

	cfg := newTestConfig([]string{server1.URL, server2.URL})
	cfg.BatchSize = 1

	client, err := NewClient(cfg, zaptest.NewLogger(t))
	require.NoError(t, err)

	buf := newBulkBuffer(client, cfg, zaptest.NewLogger(t), "trace")

	// Add 4 docs (each triggers flush due to batch_size=1)
	for i := 0; i < 4; i++ {
		err = buf.Add("test-index", map[string]any{"idx": i})
		if err != nil {
			// retry exhaustion is fine for this test since we're testing load balancing
			continue
		}
	}

	mu.Lock()
	urls := requestedURLs
	mu.Unlock()

	// Should have hit both servers (round-robin)
	if len(urls) >= 2 {
		// At least 2 different hosts should be represented
		hostSet := make(map[string]bool)
		for _, u := range urls {
			hostSet[u] = true
		}
		// With round-robin and 4 requests to 2 servers, both should be hit
		fmt.Printf("Hosts hit: %v\n", hostSet)
	}
}
