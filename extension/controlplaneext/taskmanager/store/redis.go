// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane_legacy/v1"
)

// Redis key patterns
const (
	keyPendingGlobal  = "%s:pending:global"        // List: global pending tasks
	keyPendingAgent   = "%s:pending:%s"            // List: agent-specific pending tasks
	keyTaskDetail     = "%s:detail:%s"             // String: task details JSON
	keyCancelled      = "%s:cancelled"             // Set: cancelled task IDs
	keyResult         = "%s:result:%s"             // String: task result JSON
	keyRunning        = "%s:running"               // Hash: taskID -> agentID
	keyEventSubmitted = "%s:events:task:submitted" // Pub/Sub channel
	keyEventCompleted = "%s:events:task:completed" // Pub/Sub channel
	keyEventCancelled = "%s:events:task:cancelled" // Pub/Sub channel
)

// RedisTaskStore implements TaskStore using Redis as backend.
type RedisTaskStore struct {
	logger    *zap.Logger
	client    redis.UniversalClient
	keyPrefix string
	ttl       time.Duration

	mu      sync.RWMutex
	started bool
}

// NewRedisTaskStore creates a new Redis-based task store.
func NewRedisTaskStore(logger *zap.Logger, client redis.UniversalClient, keyPrefix string, ttl time.Duration) *RedisTaskStore {
	if keyPrefix == "" {
		keyPrefix = "otel:tasks"
	}
	return &RedisTaskStore{
		logger:    logger,
		client:    client,
		keyPrefix: keyPrefix,
		ttl:       ttl,
	}
}

// Ensure RedisTaskStore implements TaskStore.
var _ TaskStore = (*RedisTaskStore)(nil)

// ===== Key Helpers =====

func (s *RedisTaskStore) detailKey(taskID string) string {
	return fmt.Sprintf(keyTaskDetail, s.keyPrefix, taskID)
}

func (s *RedisTaskStore) resultKey(taskID string) string {
	return fmt.Sprintf(keyResult, s.keyPrefix, taskID)
}

func (s *RedisTaskStore) queueKey(queueID string) string {
	if queueID == QueueGlobal {
		return fmt.Sprintf(keyPendingGlobal, s.keyPrefix)
	}
	return fmt.Sprintf(keyPendingAgent, s.keyPrefix, queueID)
}

func (s *RedisTaskStore) cancelledKey() string {
	return fmt.Sprintf(keyCancelled, s.keyPrefix)
}

func (s *RedisTaskStore) runningKey() string {
	return fmt.Sprintf(keyRunning, s.keyPrefix)
}

func (s *RedisTaskStore) eventKey(eventType string) string {
	switch eventType {
	case "submitted":
		return fmt.Sprintf(keyEventSubmitted, s.keyPrefix)
	case "completed":
		return fmt.Sprintf(keyEventCompleted, s.keyPrefix)
	case "cancelled":
		return fmt.Sprintf(keyEventCancelled, s.keyPrefix)
	default:
		return fmt.Sprintf("%s:events:task:%s", s.keyPrefix, eventType)
	}
}

func (s *RedisTaskStore) getClient() (redis.UniversalClient, error) {
	s.mu.RLock()
	client := s.client
	s.mu.RUnlock()

	if client == nil {
		return nil, errors.New("redis client not initialized")
	}
	return client, nil
}

// ===== Task Detail Operations =====

// SaveTaskInfo implements TaskStore.
func (s *RedisTaskStore) SaveTaskInfo(ctx context.Context, info *TaskInfo, isNew bool) error {
	client, err := s.getClient()
	if err != nil {
		return err
	}

	data, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("marshal task info: %w", err)
	}

	key := s.detailKey(info.Task.TaskID)

	if isNew {
		ok, err := client.SetNX(ctx, key, data, s.ttl).Result()
		if err != nil {
			return fmt.Errorf("save task info: %w", err)
		}
		if !ok {
			return errors.New("task already exists: " + info.Task.TaskID)
		}
		return nil
	}

	return client.Set(ctx, key, data, s.ttl).Err()
}

// GetTaskInfo implements TaskStore.
func (s *RedisTaskStore) GetTaskInfo(ctx context.Context, taskID string) (*TaskInfo, error) {
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}

	data, err := client.Get(ctx, s.detailKey(taskID)).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get task info: %w", err)
	}

	var info TaskInfo
	if err := json.Unmarshal([]byte(data), &info); err != nil {
		return nil, fmt.Errorf("unmarshal task info: %w", err)
	}
	return &info, nil
}

// maxRetries is the maximum number of retries for optimistic locking conflicts.
const maxRetries = 10

// baseBackoff is the base delay for exponential backoff.
const baseBackoff = 5 * time.Millisecond

// maxBackoff is the maximum delay for exponential backoff.
const maxBackoff = 100 * time.Millisecond

// updateTaskInfoScript is a Lua script for atomic CAS (Compare-And-Swap) update.
// It reads the current value, applies version check, and updates atomically.
// KEYS[1] = task detail key
// ARGV[1] = expected version (for CAS, -1 to skip version check)
// ARGV[2] = new JSON data
// ARGV[3] = TTL in seconds
// Returns: 1 = success, 0 = version mismatch, -1 = not found
var updateTaskInfoScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if not current then
    return -1
end

local expected_version = tonumber(ARGV[1])
if expected_version >= 0 then
    local data = cjson.decode(current)
    if data.version ~= expected_version then
        return 0
    end
end

local ttl = tonumber(ARGV[3])
if ttl > 0 then
    redis.call('SET', KEYS[1], ARGV[2], 'EX', ttl)
else
    redis.call('SET', KEYS[1], ARGV[2])
end
return 1
`)

// ErrVersionMismatch indicates a CAS version conflict.
var ErrVersionMismatch = errors.New("version mismatch: concurrent modification detected")

// ErrNoUpdateNeeded indicates the updater determined no update is needed (idempotent).
var ErrNoUpdateNeeded = errors.New("no update needed")

// UpdateTaskInfo implements TaskStore with optimistic locking using Lua script.
// Uses atomic CAS (Compare-And-Swap) with version field for conflict detection.
// Automatically retries on version conflicts (up to maxRetries times) with exponential backoff.
//
// The updater function is called on each retry with fresh data from Redis.
// If updater returns ErrNoUpdateNeeded, the update is skipped and nil is returned.
func (s *RedisTaskStore) UpdateTaskInfo(ctx context.Context, taskID string, updater func(*TaskInfo) error) error {
	client, err := s.getClient()
	if err != nil {
		return err
	}

	key := s.detailKey(taskID)

	for attempt := 0; attempt < maxRetries; attempt++ {
		// Step 1: Read current state (fresh on each retry)
		data, err := client.Get(ctx, key).Result()
		if err == redis.Nil {
			return errors.New("task not found: " + taskID)
		}
		if err != nil {
			return fmt.Errorf("get task info: %w", err)
		}

		var info TaskInfo
		if err := json.Unmarshal([]byte(data), &info); err != nil {
			return fmt.Errorf("unmarshal task info: %w", err)
		}

		expectedVersion := info.Version
		currentStatus := info.Status

		// Step 2: Apply updater (in-memory modification)
		// The updater receives fresh data on each retry
		if err := updater(&info); err != nil {
			// If updater says no update needed, return success
			if errors.Is(err, ErrNoUpdateNeeded) {
				return nil
			}
			return err
		}

		// Step 3: Serialize updated data
		updated, err := json.Marshal(&info)
		if err != nil {
			return fmt.Errorf("marshal task info: %w", err)
		}

		// Step 4: Atomic CAS update via Lua script
		ttlSeconds := int64(s.ttl.Seconds())
		result, err := updateTaskInfoScript.Run(ctx, client, []string{key},
			expectedVersion, string(updated), ttlSeconds).Int()
		if err != nil {
			return fmt.Errorf("execute update script: %w", err)
		}

		switch result {
		case 1: // Success
			return nil
		case 0: // Version mismatch - retry with fresh data
			s.logger.Debug("Version mismatch, retrying with backoff",
				zap.String("task_id", taskID),
				zap.Int("attempt", attempt+1),
				zap.Int("max_retries", maxRetries),
				zap.Int64("expected_version", expectedVersion),
				zap.String("current_status", currentStatus.String()),
			)
			// Exponential backoff with jitter
			backoff := time.Duration(1<<uint(attempt)) * baseBackoff
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			// Add jitter (0-50% of backoff)
			jitter := time.Duration(rand.Int63n(int64(backoff / 2)))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff + jitter):
			}
			continue
		case -1: // Not found (deleted between read and write)
			return errors.New("task not found: " + taskID)
		default:
			return fmt.Errorf("unexpected script result: %d", result)
		}
	}

	return fmt.Errorf("update task info failed after %d retries: %w", maxRetries, ErrVersionMismatch)
}

// ===== Atomic State Machine Operations (Authoritative) =====

// applyTaskResultScript atomically applies a task result update to the task detail record.
// Return: {code, agent_id, status}
// code:  1=updated, 2=noop, -1=not found, -2=rejected
// status: task's current status snapshot (after update for code=1, otherwise current)
var applyTaskResultScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if not current then
    return {-1, '', 0}
end

local info = cjson.decode(current)
local cur = tonumber(info.status) or 0
local new = tonumber(ARGV[1])
local now = tonumber(ARGV[2])
local agent = ARGV[3]
local result_json = ARGV[4]
local ttl = tonumber(ARGV[5])

local function is_terminal(s)
    return s == 1 or s == 2 or s == 3 or s == 4
end

local existing_agent = info.agent_id
if not existing_agent then existing_agent = '' end

-- Once terminal, everything is a no-op (first terminal wins).
if is_terminal(cur) then
    return {2, existing_agent, cur}
end

-- Idempotent.
if cur == new then
    return {2, existing_agent, cur}
end

-- Reject rollback RUNNING -> PENDING.
if cur == 6 and new == 5 then
    return {-2, existing_agent, cur}
end

-- Apply update.
info.status = new
info.last_updated_at_millis = now
info.version = (tonumber(info.version) or 0) + 1

if agent and agent ~= '' then
    -- Only set agent_id if empty to avoid flapping.
    if not info.agent_id or info.agent_id == '' then
        info.agent_id = agent
    end
end

if new == 6 then
    local started = tonumber(info.started_at_millis) or 0
    if started == 0 then
        info.started_at_millis = now
    end
end

if result_json and result_json ~= '' then
    info.result = cjson.decode(result_json)
end

local updated = cjson.encode(info)
if ttl and ttl > 0 then
    redis.call('SET', KEYS[1], updated, 'EX', ttl)
else
    redis.call('SET', KEYS[1], updated)
end

local after_agent = info.agent_id
if not after_agent then after_agent = '' end
return {1, after_agent, new}
`)

// applyCancelScript atomically cancels a task in the task detail record.
// Return: {code, agent_id, status}
var applyCancelScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if not current then
    return {-1, '', 0}
end

local info = cjson.decode(current)
local cur = tonumber(info.status) or 0
local now = tonumber(ARGV[1])
local ttl = tonumber(ARGV[2])

local function is_terminal(s)
    return s == 1 or s == 2 or s == 3 or s == 4
end

local existing_agent = info.agent_id
if not existing_agent then existing_agent = '' end

-- Idempotent: already cancelled.
if cur == 4 then
    return {2, existing_agent, cur}
end

-- Reject cancelling a non-cancelled terminal task.
if is_terminal(cur) then
    return {-2, existing_agent, cur}
end

info.status = 4
info.last_updated_at_millis = now
info.version = (tonumber(info.version) or 0) + 1

local updated = cjson.encode(info)
if ttl and ttl > 0 then
    redis.call('SET', KEYS[1], updated, 'EX', ttl)
else
    redis.call('SET', KEYS[1], updated)
end

local after_agent = info.agent_id
if not after_agent then after_agent = '' end
return {1, after_agent, 4}
`)

// applySetRunningScript atomically sets a task to RUNNING in the task detail record.
// Return: {code, agent_id, status}
var applySetRunningScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if not current then
    return {-1, '', 0}
end

local info = cjson.decode(current)
local cur = tonumber(info.status) or 0
local now = tonumber(ARGV[1])
local agent = ARGV[2]
local ttl = tonumber(ARGV[3])

local function is_terminal(s)
    return s == 1 or s == 2 or s == 3 or s == 4
end

local existing_agent = info.agent_id
if not existing_agent then existing_agent = '' end

-- Idempotent.
if cur == 6 then
    return {2, existing_agent, cur}
end

-- Reject terminal.
if is_terminal(cur) then
    return {-2, existing_agent, cur}
end

info.status = 6
info.last_updated_at_millis = now
info.version = (tonumber(info.version) or 0) + 1

if agent and agent ~= '' then
    info.agent_id = agent
end

local started = tonumber(info.started_at_millis) or 0
if started == 0 then
    info.started_at_millis = now
end

local updated = cjson.encode(info)
if ttl and ttl > 0 then
    redis.call('SET', KEYS[1], updated, 'EX', ttl)
else
    redis.call('SET', KEYS[1], updated)
end

local after_agent = info.agent_id
if not after_agent then after_agent = '' end
return {1, after_agent, 6}
`)

func parseApplyReply(reply []any) (ApplyTaskUpdateResult, error) {
	if len(reply) != 3 {
		return ApplyTaskUpdateResult{}, fmt.Errorf("unexpected apply reply length: %d", len(reply))
	}

	codeNum, ok := reply[0].(int64)
	if !ok {
		// go-redis may return int
		if codeInt, ok2 := reply[0].(int); ok2 {
			codeNum = int64(codeInt)
		} else {
			return ApplyTaskUpdateResult{}, fmt.Errorf("unexpected apply reply code type: %T", reply[0])
		}
	}

	agentID, _ := reply[1].(string)
	if agentID == "" {
		if reply[1] == nil {
			agentID = ""
		}
	}

	statusNum, ok := reply[2].(int64)
	if !ok {
		if statusInt, ok2 := reply[2].(int); ok2 {
			statusNum = int64(statusInt)
		} else {
			return ApplyTaskUpdateResult{}, fmt.Errorf("unexpected apply reply status type: %T", reply[2])
		}
	}

	return ApplyTaskUpdateResult{
		Code:    ApplyTaskUpdateCode(codeNum),
		Status:  controlplanev1.TaskStatus(statusNum),
		AgentID: agentID,
	}, nil
}

func (s *RedisTaskStore) ApplyTaskResult(ctx context.Context, taskID string, result *controlplanev1.TaskResult, nowMillis int64) (ApplyTaskUpdateResult, error) {
	client, err := s.getClient()
	if err != nil {
		return ApplyTaskUpdateResult{}, err
	}
	if result == nil {
		return ApplyTaskUpdateResult{}, errors.New("result cannot be nil")
	}

	key := s.detailKey(taskID)
	resultBytes, err := json.Marshal(result)
	if err != nil {
		return ApplyTaskUpdateResult{}, fmt.Errorf("marshal task result: %w", err)
	}

	ttlSeconds := int64(s.ttl.Seconds())
	reply, err := applyTaskResultScript.Run(ctx, client, []string{key},
		int64(result.Status), nowMillis, result.AgentID, string(resultBytes), ttlSeconds).Slice()
	if err != nil {
		return ApplyTaskUpdateResult{}, fmt.Errorf("execute applyTaskResult script: %w", err)
	}

	res, err := parseApplyReply(reply)
	if err != nil {
		return ApplyTaskUpdateResult{}, err
	}
	if res.Code == -1 {
		return ApplyTaskUpdateResult{}, TaskNotFound(taskID)
	}
	return res, nil
}

func (s *RedisTaskStore) ApplyCancel(ctx context.Context, taskID string, nowMillis int64) (ApplyTaskUpdateResult, error) {
	client, err := s.getClient()
	if err != nil {
		return ApplyTaskUpdateResult{}, err
	}

	key := s.detailKey(taskID)
	ttlSeconds := int64(s.ttl.Seconds())
	reply, err := applyCancelScript.Run(ctx, client, []string{key}, nowMillis, ttlSeconds).Slice()
	if err != nil {
		return ApplyTaskUpdateResult{}, fmt.Errorf("execute applyCancel script: %w", err)
	}

	res, err := parseApplyReply(reply)
	if err != nil {
		return ApplyTaskUpdateResult{}, err
	}
	if res.Code == -1 {
		return ApplyTaskUpdateResult{}, TaskNotFound(taskID)
	}
	return res, nil
}

func (s *RedisTaskStore) ApplySetRunning(ctx context.Context, taskID string, agentID string, nowMillis int64) (ApplyTaskUpdateResult, error) {
	client, err := s.getClient()
	if err != nil {
		return ApplyTaskUpdateResult{}, err
	}

	key := s.detailKey(taskID)
	ttlSeconds := int64(s.ttl.Seconds())
	reply, err := applySetRunningScript.Run(ctx, client, []string{key}, nowMillis, agentID, ttlSeconds).Slice()
	if err != nil {
		return ApplyTaskUpdateResult{}, fmt.Errorf("execute applySetRunning script: %w", err)
	}

	res, err := parseApplyReply(reply)
	if err != nil {
		return ApplyTaskUpdateResult{}, err
	}
	if res.Code == -1 {
		return ApplyTaskUpdateResult{}, TaskNotFound(taskID)
	}
	return res, nil
}

// ListTaskInfos implements TaskStore.
// Uses batched SCAN + MGET for efficiency.
func (s *RedisTaskStore) ListTaskInfos(ctx context.Context) ([]*TaskInfo, error) {
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}

	pattern := fmt.Sprintf("%s:detail:*", s.keyPrefix)
	const batchSize = 200

	var tasks []*TaskInfo
	var keyBatch []string

	iter := client.Scan(ctx, 0, pattern, 100).Iterator()
	for iter.Next(ctx) {
		keyBatch = append(keyBatch, iter.Val())

		if len(keyBatch) >= batchSize {
			batchTasks := s.fetchTaskBatch(ctx, client, keyBatch)
			tasks = append(tasks, batchTasks...)
			keyBatch = keyBatch[:0]
		}
	}

	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("scan task keys: %w", err)
	}

	// Process remaining keys
	if len(keyBatch) > 0 {
		batchTasks := s.fetchTaskBatch(ctx, client, keyBatch)
		tasks = append(tasks, batchTasks...)
	}

	return tasks, nil
}

func (s *RedisTaskStore) fetchTaskBatch(ctx context.Context, client redis.UniversalClient, keys []string) []*TaskInfo {
	if len(keys) == 0 {
		return nil
	}

	results, err := client.MGet(ctx, keys...).Result()
	if err != nil {
		s.logger.Warn("Failed to MGET task details", zap.Int("key_count", len(keys)), zap.Error(err))
		return nil
	}

	tasks := make([]*TaskInfo, 0, len(results))
	for i, data := range results {
		if data == nil {
			continue
		}

		dataStr, ok := data.(string)
		if !ok {
			continue
		}

		var info TaskInfo
		if err := json.Unmarshal([]byte(dataStr), &info); err != nil {
			s.logger.Warn("Failed to unmarshal task info", zap.String("key", keys[i]), zap.Error(err))
			continue
		}
		tasks = append(tasks, &info)
	}

	return tasks
}

// DeleteTaskInfo implements TaskStore.
func (s *RedisTaskStore) DeleteTaskInfo(ctx context.Context, taskID string) error {
	client, err := s.getClient()
	if err != nil {
		return err
	}

	pipe := client.TxPipeline()
	pipe.Del(ctx, s.detailKey(taskID))
	pipe.Del(ctx, s.resultKey(taskID))
	pipe.SRem(ctx, s.cancelledKey(), taskID)
	pipe.HDel(ctx, s.runningKey(), taskID)

	_, err = pipe.Exec(ctx)
	return err
}

// ===== Queue Operations =====

// EnqueueTask implements TaskStore.
func (s *RedisTaskStore) EnqueueTask(ctx context.Context, queueID string, taskID string, priority int32, createdAtMillis int64) error {
	client, err := s.getClient()
	if err != nil {
		return err
	}

	// Note: Redis List is FIFO, doesn't support priority natively.
	// For priority support, consider using Sorted Set (ZADD) instead.
	// Current implementation uses LPUSH (newest at head).
	return client.LPush(ctx, s.queueKey(queueID), taskID).Err()
}

// DequeueTask implements TaskStore.
func (s *RedisTaskStore) DequeueTask(ctx context.Context, queueID string, timeout time.Duration) (string, error) {
	client, err := s.getClient()
	if err != nil {
		return "", err
	}

	results, err := client.BRPop(ctx, timeout, s.queueKey(queueID)).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("dequeue task: %w", err)
	}

	if len(results) >= 2 {
		return s.resolveQueueItem(results[1]), nil
	}
	return "", nil
}

// DequeueTaskMulti implements TaskStore.
func (s *RedisTaskStore) DequeueTaskMulti(ctx context.Context, queueIDs []string, timeout time.Duration) (string, error) {
	client, err := s.getClient()
	if err != nil {
		return "", err
	}

	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		// Try each queue in order (non-blocking)
		for _, queueID := range queueIDs {
			result, err := client.RPop(ctx, s.queueKey(queueID)).Result()
			if err == nil {
				return s.resolveQueueItem(result), nil
			}
			if err != redis.Nil {
				return "", fmt.Errorf("dequeue task: %w", err)
			}
		}

		// Wait with blocking on the last queue
		remainingTimeout := time.Until(deadline)
		if remainingTimeout <= 0 {
			break
		}
		if remainingTimeout > time.Second {
			remainingTimeout = time.Second
		}

		// Block on all queues
		keys := make([]string, len(queueIDs))
		for i, queueID := range queueIDs {
			keys[i] = s.queueKey(queueID)
		}

		results, err := client.BRPop(ctx, remainingTimeout, keys...).Result()
		if err == redis.Nil {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("dequeue task: %w", err)
		}

		if len(results) >= 2 {
			return s.resolveQueueItem(results[1]), nil
		}
	}

	return "", nil
}

// resolveQueueItem handles both new format (task_id) and legacy format (JSON).
func (s *RedisTaskStore) resolveQueueItem(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	// Check if it's legacy JSON format
	if strings.HasPrefix(value, "{") {
		var task controlplanev1.Task
		if err := json.Unmarshal([]byte(value), &task); err != nil {
			s.logger.Warn("Failed to unmarshal legacy queue item", zap.Error(err))
			return ""
		}
		return task.TaskID
	}

	return value
}

// PeekQueue implements TaskStore.
func (s *RedisTaskStore) PeekQueue(ctx context.Context, queueID string) ([]string, error) {
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}

	results, err := client.LRange(ctx, s.queueKey(queueID), 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("peek queue: %w", err)
	}

	taskIDs := make([]string, 0, len(results))
	for _, item := range results {
		if taskID := s.resolveQueueItem(item); taskID != "" {
			taskIDs = append(taskIDs, taskID)
		}
	}
	return taskIDs, nil
}

// RemoveFromQueue implements TaskStore.
func (s *RedisTaskStore) RemoveFromQueue(ctx context.Context, queueID string, taskID string) error {
	client, err := s.getClient()
	if err != nil {
		return err
	}

	return client.LRem(ctx, s.queueKey(queueID), 0, taskID).Err()
}

// RemoveFromAllQueues implements TaskStore.
func (s *RedisTaskStore) RemoveFromAllQueues(ctx context.Context, taskID string, agentID string) error {
	client, err := s.getClient()
	if err != nil {
		return err
	}

	pipe := client.TxPipeline()

	// Remove from global queue
	globalKey := s.queueKey(QueueGlobal)
	pipe.LRem(ctx, globalKey, 0, taskID)

	// Remove from agent queue
	if agentID != "" {
		agentKey := s.queueKey(agentID)
		pipe.LRem(ctx, agentKey, 0, taskID)
	}

	_, err = pipe.Exec(ctx)
	return err
}

// ===== Result Operations =====

// SaveResult implements TaskStore.
func (s *RedisTaskStore) SaveResult(ctx context.Context, result *controlplanev1.TaskResult) error {
	client, err := s.getClient()
	if err != nil {
		return err
	}

	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}

	return client.Set(ctx, s.resultKey(result.TaskID), data, s.ttl).Err()
}

// GetResult implements TaskStore.
func (s *RedisTaskStore) GetResult(ctx context.Context, taskID string) (*controlplanev1.TaskResult, bool, error) {
	client, err := s.getClient()
	if err != nil {
		return nil, false, err
	}

	data, err := client.Get(ctx, s.resultKey(taskID)).Result()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get result: %w", err)
	}

	var result controlplanev1.TaskResult
	if err := json.Unmarshal([]byte(data), &result); err != nil {
		return nil, false, fmt.Errorf("unmarshal result: %w", err)
	}

	return &result, true, nil
}

// ===== Cancellation Operations =====

// SetCancelled implements TaskStore.
func (s *RedisTaskStore) SetCancelled(ctx context.Context, taskID string) error {
	client, err := s.getClient()
	if err != nil {
		return err
	}

	return client.SAdd(ctx, s.cancelledKey(), taskID).Err()
}

// IsCancelled implements TaskStore.
func (s *RedisTaskStore) IsCancelled(ctx context.Context, taskID string) (bool, error) {
	client, err := s.getClient()
	if err != nil {
		return false, err
	}

	return client.SIsMember(ctx, s.cancelledKey(), taskID).Result()
}

// ===== Running State Operations =====

// SetRunning implements TaskStore.
func (s *RedisTaskStore) SetRunning(ctx context.Context, taskID string, agentID string) error {
	client, err := s.getClient()
	if err != nil {
		return err
	}

	return client.HSet(ctx, s.runningKey(), taskID, agentID).Err()
}

// GetRunning implements TaskStore.
func (s *RedisTaskStore) GetRunning(ctx context.Context, taskID string) (string, error) {
	client, err := s.getClient()
	if err != nil {
		return "", err
	}

	result, err := client.HGet(ctx, s.runningKey(), taskID).Result()
	if err == redis.Nil {
		return "", nil
	}
	return result, err
}

// ClearRunning implements TaskStore.
func (s *RedisTaskStore) ClearRunning(ctx context.Context, taskID string) error {
	client, err := s.getClient()
	if err != nil {
		return err
	}

	return client.HDel(ctx, s.runningKey(), taskID).Err()
}

// ===== Event Operations =====

// PublishEvent implements TaskStore.
func (s *RedisTaskStore) PublishEvent(ctx context.Context, eventType string, taskID string) error {
	client, err := s.getClient()
	if err != nil {
		return err
	}

	return client.Publish(ctx, s.eventKey(eventType), taskID).Err()
}

// ===== Lifecycle =====

// Start implements TaskStore.
func (s *RedisTaskStore) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started {
		return nil
	}

	if s.client == nil {
		return errors.New("redis client not provided")
	}

	// Test connection
	if err := s.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("failed to connect to Redis: %w", err)
	}

	s.logger.Info("Starting Redis task store", zap.String("key_prefix", s.keyPrefix))
	s.started = true

	return nil
}

// Close implements TaskStore.
func (s *RedisTaskStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.started {
		return nil
	}

	// Note: We don't close the Redis client here because it's managed externally
	s.started = false
	return nil
}
