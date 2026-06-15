// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskengine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// RedisStore implements Store using Redis.
//
// Key layout (prefix default "te"):
//
//	te:task:{taskID}         — STRING (Task JSON)
//	te:q:{queueID}          — LIST (task IDs, LPUSH + RPOP)
//	te:result:{taskID}      — STRING (TaskResult JSON, TTL 24h)
//	te:group:{groupID}      — SET (task IDs in group)
//	te:events:{eventType}   — Pub/Sub channel
type RedisStore struct {
	client    redis.UniversalClient
	prefix    string
	resultTTL time.Duration
	logger    *zap.Logger
}

// RedisStoreConfig holds configuration for the Redis store.
type RedisStoreConfig struct {
	// KeyPrefix is the prefix for all Redis keys (default: "te").
	KeyPrefix string
	// ResultTTL is how long results are kept (default: 24h).
	ResultTTL time.Duration
}

// DefaultRedisStoreConfig returns sensible defaults.
func DefaultRedisStoreConfig() RedisStoreConfig {
	return RedisStoreConfig{
		KeyPrefix: "te",
		ResultTTL: 24 * time.Hour,
	}
}

// NewRedisStore creates a Redis-backed Store implementation.
func NewRedisStore(client redis.UniversalClient, logger *zap.Logger, cfg RedisStoreConfig) *RedisStore {
	if cfg.KeyPrefix == "" {
		cfg.KeyPrefix = "te"
	}
	if cfg.ResultTTL == 0 {
		cfg.ResultTTL = 24 * time.Hour
	}
	return &RedisStore{
		client:    client,
		prefix:    cfg.KeyPrefix,
		resultTTL: cfg.ResultTTL,
		logger:    logger,
	}
}

// ─── Key helpers ───

func (s *RedisStore) taskKey(taskID string) string {
	return fmt.Sprintf("%s:task:%s", s.prefix, taskID)
}

func (s *RedisStore) queueKey(queueID string) string {
	return fmt.Sprintf("%s:q:%s", s.prefix, queueID)
}

func (s *RedisStore) resultKey(taskID string) string {
	return fmt.Sprintf("%s:result:%s", s.prefix, taskID)
}

func (s *RedisStore) groupKey(groupID string) string {
	return fmt.Sprintf("%s:group:%s", s.prefix, groupID)
}

func (s *RedisStore) eventChannel(eventType TaskEventType) string {
	return fmt.Sprintf("%s:events:%s", s.prefix, eventType)
}

// ─── Task CRUD ───

// SaveTask stores a new task in Redis.
func (s *RedisStore) SaveTask(ctx context.Context, task *Task) error {
	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal task %s: %w", task.ID, err)
	}

	// Use SET NX to prevent overwriting existing tasks
	ok, err := s.client.SetNX(ctx, s.taskKey(task.ID), data, 0).Result()
	if err != nil {
		return fmt.Errorf("save task %s: %w", task.ID, err)
	}
	if !ok {
		return fmt.Errorf("task %s already exists", task.ID)
	}

	// Track group membership if GroupID is set
	if task.GroupID != "" {
		if err := s.client.SAdd(ctx, s.groupKey(task.GroupID), task.ID).Err(); err != nil {
			s.logger.Warn("failed to add task to group set",
				zap.String("taskID", task.ID),
				zap.String("groupID", task.GroupID),
				zap.Error(err),
			)
		}
	}

	return nil
}

// GetTask retrieves a task by ID.
func (s *RedisStore) GetTask(ctx context.Context, taskID string) (*Task, error) {
	data, err := s.client.Get(ctx, s.taskKey(taskID)).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get task %s: %w", taskID, err)
	}

	var task Task
	if err := json.Unmarshal([]byte(data), &task); err != nil {
		return nil, fmt.Errorf("unmarshal task %s: %w", taskID, err)
	}
	return &task, nil
}

// GetTasks retrieves multiple tasks by ID using Redis MGET for batch efficiency.
// Missing/nil results are silently omitted.
func (s *RedisStore) GetTasks(ctx context.Context, taskIDs []string) ([]*Task, error) {
	if len(taskIDs) == 0 {
		return nil, nil
	}

	keys := make([]string, len(taskIDs))
	for i, id := range taskIDs {
		keys[i] = s.taskKey(id)
	}

	results, err := s.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("mget tasks: %w", err)
	}

	tasks := make([]*Task, 0, len(taskIDs))
	for i, val := range results {
		if val == nil {
			continue // key not found
		}
		data, ok := val.(string)
		if !ok {
			s.logger.Warn("unexpected MGet result type",
				zap.String("task_id", taskIDs[i]),
				zap.Any("type", fmt.Sprintf("%T", val)))
			continue
		}
		var task Task
		if err := json.Unmarshal([]byte(data), &task); err != nil {
			s.logger.Warn("failed to unmarshal task in batch",
				zap.String("task_id", taskIDs[i]),
				zap.Error(err))
			continue
		}
		tasks = append(tasks, &task)
	}
	return tasks, nil
}

// updateTaskStatusScript is a Lua script that atomically validates and applies a status transition.
// This prevents race conditions where two nodes try to claim the same task.
//
// KEYS[1] = task key
// ARGV[1] = new status
// ARGV[2] = claimedBy (may be empty)
// Returns: 1 = updated, 0 = task not found, -1 = invalid transition, -2 = already terminal
var updateTaskStatusScript = redis.NewScript(`
local data = redis.call('GET', KEYS[1])
if not data then
    return 0
end

local task = cjson.decode(data)
local currentStatus = task.status
local newStatus = ARGV[1]
local claimedBy = ARGV[2]

-- Same status = idempotent no-op
if currentStatus == newStatus then
    return 1
end

-- Check if current status is terminal
local terminalStates = {success=true, failed=true, timeout=true, skipped=true, cancelled=true}
if terminalStates[currentStatus] then
    return -2
end

-- Validate transition
local validNext = {
    pending = {running=true, cancelled=true, timeout=true},
    running = {success=true, failed=true, timeout=true, skipped=true, cancelled=true}
}

local allowed = validNext[currentStatus]
if not allowed or not allowed[newStatus] then
    return -1
end

-- Apply transition
task.status = newStatus
if claimedBy ~= "" then
    task.claimedBy = claimedBy
end

redis.call('SET', KEYS[1], cjson.encode(task))
return 1
`)

// UpdateTaskStatus atomically validates and applies a status transition.
func (s *RedisStore) UpdateTaskStatus(ctx context.Context, taskID string, status TaskStatus, claimedBy string) error {
	result, err := updateTaskStatusScript.Run(ctx, s.client,
		[]string{s.taskKey(taskID)},
		string(status), claimedBy,
	).Int()
	if err != nil {
		return fmt.Errorf("update task status %s: %w", taskID, err)
	}

	switch result {
	case 1:
		return nil
	case 0:
		return fmt.Errorf("task %s not found", taskID)
	case -1:
		// Get current status for error message
		task, _ := s.GetTask(ctx, taskID)
		if task != nil {
			return &InvalidTransitionError{From: task.Status, To: status}
		}
		return &InvalidTransitionError{From: "unknown", To: status}
	case -2:
		return fmt.Errorf("task %s is already in a terminal state", taskID)
	default:
		return fmt.Errorf("unexpected script result: %d", result)
	}
}

// DeleteTask removes a task and its group membership.
func (s *RedisStore) DeleteTask(ctx context.Context, taskID string) error {
	// Get task first to find group
	task, err := s.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return nil // Already deleted
	}

	pipe := s.client.Pipeline()
	pipe.Del(ctx, s.taskKey(taskID))
	pipe.Del(ctx, s.resultKey(taskID))
	if task.GroupID != "" {
		pipe.SRem(ctx, s.groupKey(task.GroupID), taskID)
	}
	_, err = pipe.Exec(ctx)
	return err
}

// ListTasks returns a paginated list of tasks.
// NOTE: For simplicity, this scans group members or uses SCAN. In production,
// consider secondary indexes (ZSET by createdAt) for efficient pagination.
func (s *RedisStore) ListTasks(ctx context.Context, query ListQuery) (*ListPage, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}

	var taskIDs []string

	if query.GroupID != "" {
		// Get from group set
		ids, err := s.client.SMembers(ctx, s.groupKey(query.GroupID)).Result()
		if err != nil {
			return nil, fmt.Errorf("list tasks for group %s: %w", query.GroupID, err)
		}
		taskIDs = ids
	} else {
		// Scan pattern — less efficient but works for general queries
		pattern := fmt.Sprintf("%s:task:*", s.prefix)
		var cursor uint64
		var keys []string
		for {
			var batch []string
			var err error
			batch, cursor, err = s.client.Scan(ctx, cursor, pattern, 200).Result()
			if err != nil {
				return nil, fmt.Errorf("scan tasks: %w", err)
			}
			keys = append(keys, batch...)
			if cursor == 0 {
				break
			}
		}
		// Extract task IDs from keys
		prefixLen := len(fmt.Sprintf("%s:task:", s.prefix))
		for _, key := range keys {
			if len(key) > prefixLen {
				taskIDs = append(taskIDs, key[prefixLen:])
			}
		}
	}

	// Batch-fetch all tasks in a single MGET round-trip
	fetchedTasks, err := s.GetTasks(ctx, taskIDs)
	if err != nil {
		return nil, fmt.Errorf("batch get tasks: %w", err)
	}

	// Apply filters
	var allTasks []*Task
	for _, task := range fetchedTasks {
		if query.TaskType != "" && task.Type != query.TaskType {
			continue
		}
		if query.Status != "" && task.Status != query.Status {
			continue
		}
		allTasks = append(allTasks, task)
	}

	total := len(allTasks)
	// Apply pagination
	start := query.Offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}

	return &ListPage{
		Tasks:  allTasks[start:end],
		Total:  total,
		Offset: query.Offset,
		Limit:  limit,
	}, nil
}

// ─── Queue Operations ───

// Enqueue adds a task ID to the left of the specified queue (LPUSH).
func (s *RedisStore) Enqueue(ctx context.Context, queueID string, taskID string, _ int32) error {
	return s.client.LPush(ctx, s.queueKey(queueID), taskID).Err()
}

// dequeueScript atomically pops from the first non-empty queue.
// This is more efficient than multiple RPOP calls.
//
// KEYS = queue keys (in priority order)
// Returns: [queueKey, taskID] or nil
var dequeueScript = redis.NewScript(`
for i, key in ipairs(KEYS) do
    local val = redis.call('RPOP', key)
    if val then
        return {key, val}
    end
end
return nil
`)

// Dequeue atomically pops from the first non-empty queue in the list.
func (s *RedisStore) Dequeue(ctx context.Context, queueIDs []string) (string, error) {
	if len(queueIDs) == 0 {
		return "", nil
	}

	keys := make([]string, len(queueIDs))
	for i, id := range queueIDs {
		keys[i] = s.queueKey(id)
	}

	result, err := dequeueScript.Run(ctx, s.client, keys).StringSlice()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("dequeue from queues: %w", err)
	}

	if len(result) >= 2 {
		return result[1], nil
	}
	return "", nil
}

// RemoveFromQueue removes a specific task ID from a queue.
func (s *RedisStore) RemoveFromQueue(ctx context.Context, queueID string, taskID string) error {
	return s.client.LRem(ctx, s.queueKey(queueID), 0, taskID).Err()
}

// ─── Result Storage ───

// SaveResult persists a task result with TTL.
func (s *RedisStore) SaveResult(ctx context.Context, result *TaskResult) error {
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal result for task %s: %w", result.TaskID, err)
	}
	return s.client.Set(ctx, s.resultKey(result.TaskID), data, s.resultTTL).Err()
}

// GetResult retrieves the result for a task.
func (s *RedisStore) GetResult(ctx context.Context, taskID string) (*TaskResult, error) {
	data, err := s.client.Get(ctx, s.resultKey(taskID)).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get result for task %s: %w", taskID, err)
	}

	var result TaskResult
	if err := json.Unmarshal([]byte(data), &result); err != nil {
		return nil, fmt.Errorf("unmarshal result for task %s: %w", taskID, err)
	}
	return &result, nil
}

// ─── Progress ───

// GetProgress computes task progress for a given type and/or group.
func (s *RedisStore) GetProgress(ctx context.Context, taskType TaskType, groupID string) (*Progress, error) {
	page, err := s.ListTasks(ctx, ListQuery{
		TaskType: taskType,
		GroupID:  groupID,
		Limit:    10000, // Get all for progress
	})
	if err != nil {
		return nil, err
	}

	p := &Progress{Total: page.Total}
	for _, task := range page.Tasks {
		switch task.Status {
		case StatusPending:
			p.Pending++
		case StatusRunning:
			p.Running++
		case StatusSuccess, StatusSkipped:
			p.Completed++
		case StatusFailed:
			p.Failed++
		case StatusTimeout:
			p.Timeout++
		case StatusCancelled:
			p.Cancelled++
		}
	}
	return p, nil
}

// ─── Events ───

// PublishEvent publishes a task event to the appropriate Pub/Sub channel.
func (s *RedisStore) PublishEvent(ctx context.Context, event TaskEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	channel := s.eventChannel(event.Type)
	return s.client.Publish(ctx, channel, data).Err()
}

// ─── Lifecycle ───

// Start is a no-op for RedisStore (connection managed externally).
func (s *RedisStore) Start(_ context.Context) error {
	return nil
}

// Close is a no-op (Redis client is shared, not owned by this store).
func (s *RedisStore) Close() error {
	return nil
}
