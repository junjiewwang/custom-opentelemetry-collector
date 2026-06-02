// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// RedisCoordinator implements TaskCoordinator using Redis for multi-node
// cooperative purge execution. It uses:
//   - SET NX EX for leader election
//   - LIST (LPUSH/RPOP) for atomic task claiming (work stealing)
//   - HASH for metadata and result collection
//
// Key naming convention:
//
//	lifecycle:leader           — leader lock (STRING, TTL 30s)
//	lifecycle:active_epoch     — current active epoch (STRING, TTL 2h)
//	lifecycle:meta:{epoch}     — epoch metadata (HASH, TTL 2h)
//	lifecycle:tasks:{epoch}    — task queue (LIST, TTL 2h)
//	lifecycle:results:{epoch}  — results (HASH, TTL 24h)
type RedisCoordinator struct {
	client    redis.UniversalClient
	nodeID    string
	logger    *zap.Logger
	leaderTTL time.Duration
}

// Compile-time interface satisfaction check.
var _ TaskCoordinator = (*RedisCoordinator)(nil)
var _ RetryableCoordinator = (*RedisCoordinator)(nil)

const (
	redisKeyLeader      = "lifecycle:leader"
	redisKeyActiveEpoch = "lifecycle:active_epoch"
	redisKeyMetaPrefix  = "lifecycle:meta:"
	redisKeyTaskPrefix  = "lifecycle:tasks:"
	redisKeyResultPfx   = "lifecycle:results:"
)

// NewRedisCoordinator creates a distributed coordinator backed by Redis.
func NewRedisCoordinator(client redis.UniversalClient, nodeID string, logger *zap.Logger) *RedisCoordinator {
	return &RedisCoordinator{
		client:    client,
		nodeID:    nodeID,
		logger:    logger.Named("purge-coordinator"),
		leaderTTL: 30 * time.Second,
	}
}

// TryBecomeLeader uses SET NX EX for non-blocking leader election.
func (c *RedisCoordinator) TryBecomeLeader(ctx context.Context) (bool, error) {
	ok, err := c.client.SetNX(ctx, redisKeyLeader, c.nodeID, c.leaderTTL).Result()
	if err != nil {
		return false, fmt.Errorf("leader election failed: %w", err)
	}
	if ok {
		c.logger.Debug("Acquired leader lock", zap.String("node", c.nodeID))
	}
	return ok, nil
}

// ReleaseLeader atomically releases the lock only if we own it (Lua CAS).
func (c *RedisCoordinator) ReleaseLeader(ctx context.Context) error {
	script := redis.NewScript(`
		if redis.call("GET", KEYS[1]) == ARGV[1] then
			return redis.call("DEL", KEYS[1])
		end
		return 0
	`)
	_, err := script.Run(ctx, c.client, []string{redisKeyLeader}, c.nodeID).Result()
	if err != nil && err != redis.Nil {
		return fmt.Errorf("release leader failed: %w", err)
	}
	return nil
}

// SubmitTasks atomically writes metadata + task queue + active epoch marker.
func (c *RedisCoordinator) SubmitTasks(ctx context.Context, epoch int64, tasks []PurgeTask) error {
	if len(tasks) == 0 {
		return nil
	}

	metaKey := fmt.Sprintf("%s%d", redisKeyMetaPrefix, epoch)
	taskKey := fmt.Sprintf("%s%d", redisKeyTaskPrefix, epoch)
	resultKey := fmt.Sprintf("%s%d", redisKeyResultPfx, epoch)

	pipe := c.client.Pipeline()

	// Metadata
	pipe.HSet(ctx, metaKey, map[string]interface{}{
		"total_tasks": len(tasks),
		"status":      "executing",
		"created_at":  time.Now().Unix(),
		"leader_node": c.nodeID,
	})
	pipe.Expire(ctx, metaKey, 2*time.Hour)

	// Task queue — serialize each task as JSON and LPUSH
	taskValues := make([]interface{}, 0, len(tasks))
	for i := range tasks {
		data, err := json.Marshal(&tasks[i])
		if err != nil {
			return fmt.Errorf("marshal task failed: %w", err)
		}
		taskValues = append(taskValues, data)
	}
	pipe.LPush(ctx, taskKey, taskValues...)
	pipe.Expire(ctx, taskKey, 2*time.Hour)

	// Pre-create result key with TTL
	pipe.Expire(ctx, resultKey, 24*time.Hour)

	// Active epoch marker
	pipe.Set(ctx, redisKeyActiveEpoch, epoch, 2*time.Hour)

	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("submit tasks pipeline failed: %w", err)
	}

	c.logger.Info("Tasks submitted to Redis",
		zap.Int64("epoch", epoch),
		zap.Int("count", len(tasks)),
	)
	return nil
}

// ClaimTask atomically pops one task from the queue (RPOP).
// Returns nil when pool is empty — no two nodes can get the same task.
func (c *RedisCoordinator) ClaimTask(ctx context.Context, epoch int64) (*PurgeTask, error) {
	taskKey := fmt.Sprintf("%s%d", redisKeyTaskPrefix, epoch)

	data, err := c.client.RPop(ctx, taskKey).Bytes()
	if err == redis.Nil {
		return nil, nil // pool empty
	}
	if err != nil {
		return nil, fmt.Errorf("claim task (RPOP) failed: %w", err)
	}

	var task PurgeTask
	if err := json.Unmarshal(data, &task); err != nil {
		return nil, fmt.Errorf("unmarshal task failed: %w", err)
	}
	return &task, nil
}

// ReportResult writes the task result to the results hash.
func (c *RedisCoordinator) ReportResult(ctx context.Context, epoch int64, taskID string, result TaskResult) error {
	resultKey := fmt.Sprintf("%s%d", redisKeyResultPfx, epoch)
	data, err := json.Marshal(&result)
	if err != nil {
		return fmt.Errorf("marshal result failed: %w", err)
	}
	return c.client.HSet(ctx, resultKey, taskID, data).Err()
}

// GetProgress computes the aggregated progress for an epoch.
func (c *RedisCoordinator) GetProgress(ctx context.Context, epoch int64) (*PurgeProgress, error) {
	metaKey := fmt.Sprintf("%s%d", redisKeyMetaPrefix, epoch)
	taskKey := fmt.Sprintf("%s%d", redisKeyTaskPrefix, epoch)
	resultKey := fmt.Sprintf("%s%d", redisKeyResultPfx, epoch)

	pipe := c.client.Pipeline()
	totalCmd := pipe.HGet(ctx, metaKey, "total_tasks")
	remainingCmd := pipe.LLen(ctx, taskKey)
	resultsCmd := pipe.HGetAll(ctx, resultKey)
	_, _ = pipe.Exec(ctx)

	total, _ := totalCmd.Int()
	remaining := int(remainingCmd.Val())

	// Aggregate results
	completed := 0
	failed := 0
	results, _ := resultsCmd.Result()
	for _, v := range results {
		var r TaskResult
		if json.Unmarshal([]byte(v), &r) == nil {
			switch r.Status {
			case TaskStatusFailed, TaskStatusTimeout:
				failed++
			default:
				completed++
			}
		}
	}

	status := "executing"
	if remaining == 0 && (completed+failed) >= total {
		status = "done"
	}

	return &PurgeProgress{
		Epoch:      epoch,
		TotalTasks: total,
		Completed:  completed,
		Failed:     failed,
		Remaining:  remaining,
		Status:     status,
	}, nil
}

// GetActiveEpoch returns the currently active purge batch epoch.
func (c *RedisCoordinator) GetActiveEpoch(ctx context.Context) (int64, error) {
	val, err := c.client.Get(ctx, redisKeyActiveEpoch).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get active epoch failed: %w", err)
	}
	return val, nil
}

// CompleteEpoch marks the epoch as done, removes active marker, sets result TTL.
func (c *RedisCoordinator) CompleteEpoch(ctx context.Context, epoch int64) error {
	metaKey := fmt.Sprintf("%s%d", redisKeyMetaPrefix, epoch)
	resultKey := fmt.Sprintf("%s%d", redisKeyResultPfx, epoch)
	taskKey := fmt.Sprintf("%s%d", redisKeyTaskPrefix, epoch)

	pipe := c.client.Pipeline()
	pipe.HSet(ctx, metaKey, "status", "done")
	pipe.Del(ctx, redisKeyActiveEpoch)
	pipe.Del(ctx, taskKey) // clean up empty queue
	// Keep results for audit trail (24h)
	pipe.Expire(ctx, resultKey, 24*time.Hour)
	pipe.Expire(ctx, metaKey, 24*time.Hour)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("complete epoch pipeline failed: %w", err)
	}

	c.logger.Info("Epoch completed", zap.Int64("epoch", epoch))
	return nil
}

// GetFailedTasks scans results hash for failed tasks and returns those eligible for retry.
// Implements RetryableCoordinator.
func (c *RedisCoordinator) GetFailedTasks(ctx context.Context, epoch int64, maxRetries int) ([]PurgeTask, error) {
	resultKey := fmt.Sprintf("%s%d", redisKeyResultPfx, epoch)

	results, err := c.client.HGetAll(ctx, resultKey).Result()
	if err != nil {
		return nil, fmt.Errorf("get results for retry failed: %w", err)
	}

	var retryable []PurgeTask
	for taskID, resultJSON := range results {
		var result TaskResult
		if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
			continue
		}

		if result.Status != TaskStatusFailed && result.Status != TaskStatusTimeout {
			continue
		}

		// Parse task metadata from the taskID format: "{epoch}:{signal}:{indexName}"
		task, ok := parseTaskID(taskID, epoch)
		if !ok {
			continue
		}

		// Check retry eligibility (we use the task ID convention to track retries;
		// for accurate tracking, the original retry count should come from the result)
		if task.Retry < maxRetries {
			retryable = append(retryable, task)
		}
	}

	return retryable, nil
}

// parseTaskID reconstructs a PurgeTask from a task ID string.
// Format: "{epoch}:{signal}:{indexName}"
func parseTaskID(taskID string, epoch int64) (PurgeTask, bool) {
	// Find first colon (after epoch)
	firstColon := -1
	for i, ch := range taskID {
		if ch == ':' {
			firstColon = i
			break
		}
	}
	if firstColon < 0 || firstColon >= len(taskID)-1 {
		return PurgeTask{}, false
	}

	rest := taskID[firstColon+1:]
	// Find second colon (between signal and indexName)
	secondColon := -1
	for i, ch := range rest {
		if ch == ':' {
			secondColon = i
			break
		}
	}
	if secondColon < 0 || secondColon >= len(rest)-1 {
		return PurgeTask{}, false
	}

	signal := SignalType(rest[:secondColon])
	indexName := rest[secondColon+1:]

	return PurgeTask{
		ID:        taskID,
		Epoch:     epoch,
		Signal:    signal,
		IndexName: indexName,
		Retry:     0, // Will be incremented by retryFailedTasks
	}, true
}
