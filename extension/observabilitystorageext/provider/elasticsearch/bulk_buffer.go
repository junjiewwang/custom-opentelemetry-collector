// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

// bulkBuffer is a generic batch buffer for bulk ES operations.
// It accumulates documents and flushes them either when the batch size
// is reached or when the flush interval elapses.
type bulkBuffer struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	count  int
	config *Config
	client *Client
	logger *zap.Logger
	signal string // "trace", "metric", "log" for logging

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// newBulkBuffer creates a new bulk buffer.
func newBulkBuffer(client *Client, config *Config, logger *zap.Logger, signal string) *bulkBuffer {
	return &bulkBuffer{
		config: config,
		client: client,
		logger: logger,
		signal: signal,
		stopCh: make(chan struct{}),
	}
}

// Start begins the background flush goroutine.
func (b *bulkBuffer) Start() {
	b.wg.Add(1)
	go b.flushLoop()
}

// Stop signals the background flush goroutine to stop.
func (b *bulkBuffer) Stop() {
	close(b.stopCh)
	b.wg.Wait()
}

// Add adds a document to the buffer. If batch size is reached, triggers a flush.
// indexName is the target ES index for this document. The ctx is honored by the
// triggered flush (previously this dropped the caller's context and flushed with
// context.Background(), which could write data the caller had already cancelled).
func (b *bulkBuffer) Add(ctx context.Context, indexName string, doc any) error {
	action := map[string]any{
		"index": map[string]any{
			"_index": indexName,
		},
	}

	actionBytes, err := json.Marshal(action)
	if err != nil {
		return fmt.Errorf("failed to marshal bulk action: %w", err)
	}

	docBytes, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("failed to marshal document: %w", err)
	}

	b.mu.Lock()
	b.buf.Write(actionBytes)
	b.buf.WriteByte('\n')
	b.buf.Write(docBytes)
	b.buf.WriteByte('\n')
	b.count++
	shouldFlush := b.count >= b.config.BatchSize
	b.mu.Unlock()

	if shouldFlush {
		return b.Flush(ctx)
	}
	return nil
}

// Flush sends all buffered documents to ES.
func (b *bulkBuffer) Flush(ctx context.Context) error {
	b.mu.Lock()
	if b.count == 0 {
		b.mu.Unlock()
		return nil
	}

	data := make([]byte, b.buf.Len())
	copy(data, b.buf.Bytes())
	count := b.count
	b.buf.Reset()
	b.count = 0
	b.mu.Unlock()

	b.logger.Debug("Flushing bulk buffer",
		zap.String("signal", b.signal),
		zap.Int("doc_count", count),
		zap.Int("payload_bytes", len(data)),
	)

	var lastErr error
	for attempt := 0; attempt <= b.config.MaxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(attempt*attempt) * 100 * time.Millisecond
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}

		resp, err := b.client.BulkIndex(ctx, data)
		if err != nil {
			lastErr = err
			b.logger.Warn("Bulk request failed, retrying",
				zap.String("signal", b.signal),
				zap.Int("attempt", attempt+1),
				zap.Error(err),
			)
			continue
		}

		if resp.Errors {
			errorCount := 0
			for _, item := range resp.Items {
				if item.Index != nil && item.Index.Error != nil {
					errorCount++
					if errorCount <= 3 {
						b.logger.Warn("Bulk item error",
							zap.String("signal", b.signal),
							zap.String("error_type", item.Index.Error.Type),
							zap.String("reason", item.Index.Error.Reason),
						)
					}
				}
			}
			b.logger.Warn("Bulk request completed with errors",
				zap.String("signal", b.signal),
				zap.Int("total_docs", count),
				zap.Int("error_count", errorCount),
			)
		} else {
			b.logger.Debug("Bulk request succeeded",
				zap.String("signal", b.signal),
				zap.Int("doc_count", count),
				zap.Int("took_ms", resp.Took),
			)
		}
		return nil
	}

	return fmt.Errorf("bulk request failed after %d retries: %w", b.config.MaxRetries, lastErr)
}

// flushLoop periodically flushes the buffer based on flush interval.
func (b *bulkBuffer) flushLoop() {
	defer b.wg.Done()

	ticker := time.NewTicker(b.config.FlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-b.stopCh:
			return
		case <-ticker.C:
			if err := b.Flush(context.Background()); err != nil {
				b.logger.Error("Periodic flush failed",
					zap.String("signal", b.signal),
					zap.Error(err),
				)
			}
		}
	}
}
