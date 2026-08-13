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
	return b.addInternal(ctx, indexName, "", doc)
}

// AddWithID adds a document with a deterministic _id. Used by rollup to ensure
// idempotent writes: the same (index, _id) always overwrites the same document,
// so re-running a rollup slice converges even if two replicas race.
func (b *bulkBuffer) AddWithID(ctx context.Context, indexName, docID string, doc any) error {
	return b.addInternal(ctx, indexName, docID, doc)
}

// AddScriptedUpsert buffers a scripted-upsert "update" action. body must be the
// NDJSON payload following the action line — the map produced by
// metaScriptedUpsert (script + scripted_upsert + upsert doc). The action line is
// built here with the update verb and deterministic _id so re-running converges.
func (b *bulkBuffer) AddScriptedUpsert(ctx context.Context, indexName, docID string, body map[string]any) error {
	action := map[string]any{
		"update": map[string]any{
			"_index": indexName,
			"_id":    docID,
		},
	}
	actionBytes, err := json.Marshal(action)
	if err != nil {
		return fmt.Errorf("failed to marshal update action: %w", err)
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal upsert body: %w", err)
	}

	b.mu.Lock()
	b.buf.Write(actionBytes)
	b.buf.WriteByte('\n')
	b.buf.Write(bodyBytes)
	b.buf.WriteByte('\n')
	b.count++
	shouldFlush := b.count >= b.config.BatchSize
	b.mu.Unlock()

	if shouldFlush {
		return b.Flush(ctx)
	}
	return nil
}

func (b *bulkBuffer) addInternal(ctx context.Context, indexName, docID string, doc any) error {
	indexMeta := map[string]any{
		"_index": indexName,
	}
	if docID != "" {
		indexMeta["_id"] = docID
	}
	action := map[string]any{
		"index": indexMeta,
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
				// A bulk item is exactly one action, so at most one of these is
				// non-nil. Check all of them — checking only Index would swallow
				// failures from scripted-upsert "update" actions (their Index
				// field is nil).
				for _, res := range []*BulkItemResponse{item.Index, item.Update, item.Create, item.Delete} {
					if res != nil && res.Error != nil {
						errorCount++
						if errorCount <= 3 {
							b.logger.Warn("Bulk item error",
								zap.String("signal", b.signal),
								zap.String("error_type", res.Error.Type),
								zap.String("reason", res.Error.Reason),
							)
						}
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
