// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package controlplaneext

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

// Ensure redisChunkStore implements ChunkStore.
var _ ChunkStore = (*redisChunkStore)(nil)

// Redis key patterns for chunk uploads:
//
//	{prefix}:chunk:{key}:data    — Hash: chunkIndex -> chunkData (binary)
//	{prefix}:chunk:{key}:meta    — Hash: totalChunks, fileName, contentType, taskID, uploadID, createdAt
const (
	chunkDataKeySuffix = ":data"
	chunkMetaKeySuffix = ":meta"
)

// redisChunkStore implements ChunkStore using Redis as backend.
// Each upload is stored as two Redis Hashes:
// - data hash: field=chunkIndex, value=chunkData (binary)
// - meta hash: upload metadata (totalChunks, fileName, etc.)
type redisChunkStore struct {
	logger    *zap.Logger
	client    redis.UniversalClient
	keyPrefix string
	uploadTTL time.Duration
}

// NewRedisChunkStore creates a Redis-backed chunk store.
func NewRedisChunkStore(logger *zap.Logger, client redis.UniversalClient, keyPrefix string, uploadTTL time.Duration) ChunkStore {
	if keyPrefix == "" {
		keyPrefix = "otel:chunks"
	}
	if uploadTTL <= 0 {
		uploadTTL = 1 * time.Hour
	}

	return &redisChunkStore{
		logger:    logger,
		client:    client,
		keyPrefix: keyPrefix,
		uploadTTL: uploadTTL,
	}
}

func (s *redisChunkStore) dataKey(key string) string {
	return s.keyPrefix + ":chunk:" + key + chunkDataKeySuffix
}

func (s *redisChunkStore) metaKey(key string) string {
	return s.keyPrefix + ":chunk:" + key + chunkMetaKeySuffix
}

// StoreChunk implements ChunkStore.
func (s *redisChunkStore) StoreChunk(ctx context.Context, key string, req *model.ChunkUpload) (int32, int32, error) {
	dk := s.dataKey(key)
	mk := s.metaKey(key)

	// Use a pipeline to atomically check/set metadata and store chunk
	pipe := s.client.TxPipeline()

	// Check if meta exists
	existsCmd := pipe.Exists(ctx, mk)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("redis check meta: %w", err)
	}

	metaExists := existsCmd.Val() > 0

	if !metaExists {
		// First chunk: create metadata
		metaFields := map[string]any{
			"task_id":      req.TaskID,
			"upload_id":    req.UploadID,
			"total_chunks": req.TotalChunks,
			"created_at":   time.Now().UnixMilli(),
		}
		if req.FileName != "" {
			metaFields["file_name"] = req.FileName
		}
		if req.ContentType != "" {
			metaFields["content_type"] = req.ContentType
		}

		pipe = s.client.TxPipeline()
		pipe.HSet(ctx, mk, metaFields)
		pipe.Expire(ctx, mk, s.uploadTTL)
		pipe.Expire(ctx, dk, s.uploadTTL)
	} else {
		pipe = s.client.TxPipeline()

		// Verify total_chunks matches
		totalChunksCmd := pipe.HGet(ctx, mk, "total_chunks")
		_, err := pipe.Exec(ctx)
		if err != nil {
			return 0, 0, fmt.Errorf("redis get total_chunks: %w", err)
		}

		storedTotal, err := totalChunksCmd.Int()
		if err != nil {
			return 0, 0, fmt.Errorf("redis parse total_chunks: %w", err)
		}
		if int32(storedTotal) != req.TotalChunks {
			count, _ := s.client.HLen(ctx, dk).Result()
			return int32(count), int32(storedTotal),
				fmt.Errorf("total_chunks mismatch with existing upload")
		}

		// Update file metadata if not yet set
		if req.FileName != "" {
			s.client.HSetNX(ctx, mk, "file_name", req.FileName)
		}
		if req.ContentType != "" {
			s.client.HSetNX(ctx, mk, "content_type", req.ContentType)
		}

		pipe = s.client.TxPipeline()
	}

	// Store the chunk data
	chunkField := fmt.Sprintf("%d", req.ChunkIndex)
	pipe.HSet(ctx, dk, chunkField, req.ChunkData)
	// Refresh TTL
	pipe.Expire(ctx, dk, s.uploadTTL)
	pipe.Expire(ctx, mk, s.uploadTTL)

	_, err = pipe.Exec(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("redis store chunk: %w", err)
	}

	// Get current count
	count, err := s.client.HLen(ctx, dk).Result()
	if err != nil {
		return 0, 0, fmt.Errorf("redis count chunks: %w", err)
	}

	return int32(count), req.TotalChunks, nil
}

// GetCompleteUpload implements ChunkStore.
func (s *redisChunkStore) GetCompleteUpload(ctx context.Context, key string) (*CompleteUpload, bool) {
	dk := s.dataKey(key)
	mk := s.metaKey(key)

	// Get metadata
	metaMap, err := s.client.HGetAll(ctx, mk).Result()
	if err != nil || len(metaMap) == 0 {
		return nil, false
	}

	totalChunks := 0
	if v, ok := metaMap["total_chunks"]; ok {
		fmt.Sscanf(v, "%d", &totalChunks)
	}
	if totalChunks <= 0 {
		return nil, false
	}

	// Get all chunks
	chunkMap, err := s.client.HGetAll(ctx, dk).Result()
	if err != nil || len(chunkMap) != totalChunks {
		return nil, false
	}

	// Assemble data in order
	var data []byte
	for i := 0; i < totalChunks; i++ {
		field := fmt.Sprintf("%d", i)
		chunkStr, ok := chunkMap[field]
		if !ok {
			return nil, false
		}
		data = append(data, []byte(chunkStr)...)
	}

	result := &CompleteUpload{
		Data:        data,
		FileName:    metaMap["file_name"],
		ContentType: metaMap["content_type"],
	}

	// Remove completed upload (best effort)
	s.client.Del(ctx, dk, mk)

	return result, true
}

// Close implements ChunkStore.
func (s *redisChunkStore) Close() error {
	// Redis client lifecycle is managed externally.
	return nil
}


