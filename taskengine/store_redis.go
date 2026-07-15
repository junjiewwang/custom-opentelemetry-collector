// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskengine

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/collector/custom/taskengine/node"
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

func (s *RedisStore) runningKey() string {
	return fmt.Sprintf("%s:running_tasks", s.prefix)
}

// runningDeadlineKey returns the key for the deadline-indexed ZSET.
// This ZSET uses score=deadline (createdAt+timeout) for O(logN+K) overdue queries.
// Separate from runningKey() to allow zero-downtime migration.
func (s *RedisStore) runningDeadlineKey() string {
	return fmt.Sprintf("%s:running_deadlines", s.prefix)
}

// ─── New Key Layout (Step 2: HASH + STRING split) ───

// metaKey returns the HASH key for task metadata.
// Uses {id} HashTag to ensure meta + payload land on the same Redis Cluster slot.
func (s *RedisStore) metaKey(id string) string {
	return fmt.Sprintf("%s:{%s}:meta", s.prefix, id)
}

// payloadKey returns the STRING key for task payload (opaque JSON).
// Same {id} HashTag as metaKey for Cluster co-location.
func (s *RedisStore) payloadKey(id string) string {
	return fmt.Sprintf("%s:{%s}:payload", s.prefix, id)
}

// pendingIndexKey returns the ZSET key for the pending task index.
// Uses {idx} HashTag so all indexes share one Cluster slot.
func (s *RedisStore) pendingIndexKey() string {
	return fmt.Sprintf("%s:{idx}:pending", s.prefix)
}

// runningIndexKey returns the ZSET key for the running task index (score=deadline).
// Uses {idx} HashTag — this is the Cluster-aware successor of runningDeadlineKey().
func (s *RedisStore) runningIndexKey() string {
	return fmt.Sprintf("%s:{idx}:running", s.prefix)
}

// newResultKey returns the result key under the new layout.
// Same {id} HashTag as metaKey/payloadKey.
func (s *RedisStore) newResultKey(id string) string {
	return fmt.Sprintf("%s:{%s}:result", s.prefix, id)
}

func (s *RedisStore) eventChannel(eventType TaskEventType) string {
	return fmt.Sprintf("%s:events:%s", s.prefix, eventType)
}

// ─── Task CRUD ───

// SaveTask stores a new task in Redis using the split layout:
//   - te:{id}:meta    — HASH (task metadata, ~200B)
//   - te:{id}:payload — STRING (opaque business JSON, ~1.5KB)
//   - te:{idx}:pending — ZSET (score=createdAt for pending tasks)
//   - te:group:{groupID} — SET (group membership)
//
// Also writes the legacy te:task:{id} STRING for backward compatibility during
// the migration period. This will be removed after full rollout.
func (s *RedisStore) SaveTask(ctx context.Context, task *Task) error {
	// ─── Step 1: Check existence (NX semantics) ───
	// Use HSETNX on the "id" field as an atomic existence check.
	ok, err := s.client.HSetNX(ctx, s.metaKey(task.ID), "id", task.ID).Result()
	if err != nil {
		return fmt.Errorf("save task %s: %w", task.ID, err)
	}
	if !ok {
		return fmt.Errorf("task %s already exists", task.ID)
	}

	// ─── Step 2: Build meta HASH fields ───
	metaFields := taskToMetaMap(task)

	// ─── Step 3: Pipeline — complete the write atomically ───
	pipe := s.client.Pipeline()

	// HSET meta (all remaining fields; "id" already set by HSETNX above)
	pipe.HSet(ctx, s.metaKey(task.ID), metaFields)

	// SET payload (opaque JSON)
	if task.Payload != nil {
		pipe.Set(ctx, s.payloadKey(task.ID), string(task.Payload), 0)
	}

	// ZADD pending index (score = createdAt for ordering)
	if task.Status == StatusPending {
		pipe.ZAdd(ctx, s.pendingIndexKey(), redis.Z{
			Score:  float64(task.CreatedAt),
			Member: task.ID,
		})
	}

	// SADD group membership
	if task.GroupID != "" {
		pipe.SAdd(ctx, s.groupKey(task.GroupID), task.ID)
	}

	// ─── Legacy: also write the old STRING format for fallback ───
	legacyData, err := json.Marshal(task)
	if err != nil {
		// Rollback: delete the meta key if marshal fails
		s.client.Del(ctx, s.metaKey(task.ID))
		return fmt.Errorf("marshal task %s (legacy): %w", task.ID, err)
	}
	pipe.Set(ctx, s.taskKey(task.ID), legacyData, 0)

	if _, err := pipe.Exec(ctx); err != nil {
		// Best-effort rollback
		s.client.Del(ctx, s.metaKey(task.ID))
		return fmt.Errorf("save task %s pipeline: %w", task.ID, err)
	}

	return nil
}

// taskToMetaMap converts a Task to a flat map suitable for Redis HSET.
// Only metadata fields are included; Payload is stored separately.
func taskToMetaMap(task *Task) map[string]interface{} {
	m := map[string]interface{}{
		"id":         task.ID,
		"type":       string(task.Type),
		"status":     string(task.Status),
		"createdAt":  strconv.FormatInt(task.CreatedAt, 10),
		"timeout":    strconv.FormatInt(int64(task.Timeout), 10), // nanoseconds
		"priority":   strconv.FormatInt(int64(task.Priority), 10),
		"maxRetries": strconv.Itoa(task.MaxRetries),
		"retryCount": strconv.Itoa(task.RetryCount),
	}
	if task.ExpiresAt != 0 {
		m["expiresAt"] = strconv.FormatInt(task.ExpiresAt, 10)
	}
	if task.ClaimedBy != "" {
		m["claimedBy"] = task.ClaimedBy
	}
	if task.GroupID != "" {
		m["groupId"] = task.GroupID
	}
	if task.Metadata != nil {
		// Store metadata as JSON string in a single HASH field
		if metaJSON, err := json.Marshal(task.Metadata); err == nil {
			m["metadata"] = string(metaJSON)
		}
	}
	// Routing: serialize as individual fields for easy HGET
	m["routeStrategy"] = string(task.Routing.Strategy)
	if task.Routing.TargetNodeID != "" {
		m["routeTarget"] = task.Routing.TargetNodeID
	}
	if len(task.Routing.RequiredCapabilities) > 0 {
		caps := make([]string, len(task.Routing.RequiredCapabilities))
		for i, c := range task.Routing.RequiredCapabilities {
			caps[i] = string(c)
		}
		m["routeCaps"] = strings.Join(caps, ",")
	}
	return m
}

// metaMapToTask reconstructs a Task from a HASH meta map + payload bytes.
// The payload parameter may be nil if not loaded.
func metaMapToTask(meta map[string]string, payload []byte) (*Task, error) {
	task := &Task{}
	task.ID = meta["id"]
	task.Type = TaskType(meta["type"])
	task.Status = TaskStatus(meta["status"])

	if v, ok := meta["createdAt"]; ok {
		task.CreatedAt, _ = strconv.ParseInt(v, 10, 64)
	}
	if v, ok := meta["timeout"]; ok {
		ns, _ := strconv.ParseInt(v, 10, 64)
		task.Timeout = time.Duration(ns)
	}
	if v, ok := meta["priority"]; ok {
		p, _ := strconv.ParseInt(v, 10, 32)
		task.Priority = int32(p)
	}
	if v, ok := meta["maxRetries"]; ok {
		task.MaxRetries, _ = strconv.Atoi(v)
	}
	if v, ok := meta["retryCount"]; ok {
		task.RetryCount, _ = strconv.Atoi(v)
	}
	if v, ok := meta["expiresAt"]; ok {
		task.ExpiresAt, _ = strconv.ParseInt(v, 10, 64)
	}
	if v, ok := meta["claimedBy"]; ok {
		task.ClaimedBy = v
	}
	if v, ok := meta["groupId"]; ok {
		task.GroupID = v
	}
	if v, ok := meta["metadata"]; ok && v != "" {
		var md map[string]string
		if json.Unmarshal([]byte(v), &md) == nil {
			task.Metadata = md
		}
	}
	// Routing
	task.Routing.Strategy = RoutingStrategy(meta["routeStrategy"])
	if v, ok := meta["routeTarget"]; ok {
		task.Routing.TargetNodeID = v
	}
	if v, ok := meta["routeCaps"]; ok && v != "" {
		parts := strings.Split(v, ",")
		caps := make([]node.Capability, len(parts))
		for i, p := range parts {
			caps[i] = node.Capability(p)
		}
		task.Routing.RequiredCapabilities = caps
	}

	// Payload
	if payload != nil {
		task.Payload = json.RawMessage(payload)
	}

	return task, nil
}

// metaMapToTaskMeta converts a HASH meta map to TaskMeta (lightweight, no Payload).
func metaMapToTaskMeta(meta map[string]string) *TaskMeta {
	tm := &TaskMeta{}
	tm.ID = meta["id"]
	tm.Type = TaskType(meta["type"])
	tm.Status = TaskStatus(meta["status"])

	if v, ok := meta["createdAt"]; ok {
		tm.CreatedAt, _ = strconv.ParseInt(v, 10, 64)
	}
	if v, ok := meta["timeout"]; ok {
		ns, _ := strconv.ParseInt(v, 10, 64)
		tm.Timeout = time.Duration(ns)
	}
	if v, ok := meta["priority"]; ok {
		p, _ := strconv.ParseInt(v, 10, 32)
		tm.Priority = int32(p)
	}
	if v, ok := meta["maxRetries"]; ok {
		tm.MaxRetries, _ = strconv.Atoi(v)
	}
	if v, ok := meta["retryCount"]; ok {
		tm.RetryCount, _ = strconv.Atoi(v)
	}
	if v, ok := meta["claimedBy"]; ok {
		tm.ClaimedBy = v
	}
	if v, ok := meta["groupId"]; ok {
		tm.GroupID = v
	}
	return tm
}

// GetTask retrieves a task by ID.
// Tries the new HASH+STRING layout first, falls back to legacy STRING format.
func (s *RedisStore) GetTask(ctx context.Context, taskID string) (*Task, error) {
	// ─── New format: HGETALL meta + GET payload (Pipeline 1 round-trip) ───
	pipe := s.client.Pipeline()
	metaCmd := pipe.HGetAll(ctx, s.metaKey(taskID))
	payloadCmd := pipe.Get(ctx, s.payloadKey(taskID))
	_, _ = pipe.Exec(ctx) // errors checked per command

	meta, metaErr := metaCmd.Result()
	if metaErr == nil && len(meta) > 0 {
		// New format found — assemble task
		var payload []byte
		if payloadData, err := payloadCmd.Result(); err == nil {
			payload = []byte(payloadData)
		}
		// payloadCmd returning redis.Nil is OK (task may have nil payload)
		return metaMapToTask(meta, payload)
	}

	// ─── Fallback: legacy STRING format ───
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

// GetTasks retrieves multiple tasks by ID using pipeline batch.
// Tries new HASH+STRING format first, falls back to legacy MGET for missing ones.
// Missing/nil results are silently omitted.
func (s *RedisStore) GetTasks(ctx context.Context, taskIDs []string) ([]*Task, error) {
	if len(taskIDs) == 0 {
		return nil, nil
	}

	// ─── Phase 1: Pipeline HGETALL + GET for new format ───
	pipe := s.client.Pipeline()
	type pipeResult struct {
		metaCmd    *redis.MapStringStringCmd
		payloadCmd *redis.StringCmd
	}
	pipeResults := make([]pipeResult, len(taskIDs))
	for i, id := range taskIDs {
		pipeResults[i].metaCmd = pipe.HGetAll(ctx, s.metaKey(id))
		pipeResults[i].payloadCmd = pipe.Get(ctx, s.payloadKey(id))
	}
	_, _ = pipe.Exec(ctx)

	tasks := make([]*Task, 0, len(taskIDs))
	var fallbackIDs []string // IDs that need legacy format lookup

	for i, pr := range pipeResults {
		meta, err := pr.metaCmd.Result()
		if err == nil && len(meta) > 0 {
			var payload []byte
			if data, pErr := pr.payloadCmd.Result(); pErr == nil {
				payload = []byte(data)
			}
			task, tErr := metaMapToTask(meta, payload)
			if tErr == nil {
				tasks = append(tasks, task)
				continue
			}
			s.logger.Warn("failed to assemble task from meta hash",
				zap.String("task_id", taskIDs[i]), zap.Error(tErr))
		}
		// New format not found — need legacy fallback
		fallbackIDs = append(fallbackIDs, taskIDs[i])
	}

	// ─── Phase 2: Legacy MGET fallback for remaining IDs ───
	if len(fallbackIDs) > 0 {
		legacyTasks, err := s.getTasksLegacy(ctx, fallbackIDs)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, legacyTasks...)
	}

	return tasks, nil
}

// getTasksLegacy fetches tasks using the legacy STRING format (MGET).
func (s *RedisStore) getTasksLegacy(ctx context.Context, taskIDs []string) ([]*Task, error) {
	keys := make([]string, len(taskIDs))
	for i, id := range taskIDs {
		keys[i] = s.taskKey(id)
	}

	results, err := s.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("mget tasks (legacy): %w", err)
	}

	tasks := make([]*Task, 0, len(taskIDs))
	for i, val := range results {
		if val == nil {
			continue
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

// updateTaskStatusScript atomically validates and applies a status transition
// on the HASH meta key. Index maintenance is handled asynchronously via
// updateIndex() because indexes are on a different Cluster slot.
//
// KEYS[1] = te:{id}:meta (HASH, same slot as payload)
// KEYS[2] = te:{id}:payload (STRING, for terminal TTL)
// KEYS[3] = te:task:{id} (legacy STRING, dual-write for migration compat)
// ARGV[1] = new status
// ARGV[2] = claimedBy (may be empty)
// ARGV[3] = terminal TTL seconds (14 days)
//
// Returns string:
//
//	"OK:{oldStatus}"     — updated successfully, old status for index maintenance
//	"NOT_FOUND"          — task not found
//	"INVALID:{oldStatus}"" — invalid transition
//	"TERMINAL"           — already in terminal state
//	"SAME"               — same status (idempotent)
var updateTaskStatusScript = redis.NewScript(`
local currentStatus = redis.call('HGET', KEYS[1], 'status')
if not currentStatus then
    return 'NOT_FOUND'
end

local newStatus = ARGV[1]
local claimedBy = ARGV[2]
local terminalTTL = tonumber(ARGV[3])

-- Same status = idempotent no-op
if currentStatus == newStatus then
    return 'SAME'
end

-- Terminal guard: cannot transition from terminal state
local terminalStates = {success=true, failed=true, timeout=true, skipped=true, cancelled=true}
if terminalStates[currentStatus] then
    return 'TERMINAL'
end

-- ─── Validate transition ───
local validNext = {
    pending = {running=true, cancelled=true, timeout=true},
    running = {success=true, failed=true, timeout=true, skipped=true, cancelled=true}
}

local allowed = validNext[currentStatus]
if not allowed or not allowed[newStatus] then
    return 'INVALID:' .. currentStatus
end

-- ─── Apply transition to HASH meta (atomically within same slot) ───
redis.call('HSET', KEYS[1], 'status', newStatus)
if claimedBy ~= '' then
    redis.call('HSET', KEYS[1], 'claimedBy', claimedBy)
end

-- ─── Terminal TTL: auto-expire meta + payload after 14 days ───
if terminalStates[newStatus] then
    redis.call('EXPIRE', KEYS[1], terminalTTL)
    redis.call('EXPIRE', KEYS[2], terminalTTL)
end

-- ─── Legacy dual-write: update old STRING format (migration compat) ───
-- Will be removed after full migration.
local legacyData = redis.call('GET', KEYS[3])
if legacyData then
    local task = cjson.decode(legacyData)
    task.status = newStatus
    if claimedBy ~= '' then
        task.claimedBy = claimedBy
    end
    redis.call('SET', KEYS[3], cjson.encode(task))
    if terminalStates[newStatus] then
        redis.call('EXPIRE', KEYS[3], terminalTTL)
    end
end

-- Return old status so caller can decide index transitions
return 'OK:' .. currentStatus
`)

// terminalTTLSeconds is the TTL applied to task data keys when a task
// reaches a terminal state. 14 days gives enough time for debugging.
const terminalTTLSeconds = 1209600

// defaultTimeoutMillis is the fallback timeout for tasks with Timeout=0.
// Used to compute deadline score when metadata is unavailable.
const defaultTimeoutMillis = 120000 // 120s

// UpdateTaskStatus atomically validates and applies a status transition
// on the HASH meta key. Index maintenance is async (best-effort).
func (s *RedisStore) UpdateTaskStatus(ctx context.Context, taskID string, status TaskStatus, claimedBy string) error {
	// ─── Step 1: Lua atomic transition on meta HASH ───
	result, err := updateTaskStatusScript.Run(ctx, s.client,
		[]string{s.metaKey(taskID), s.payloadKey(taskID), s.taskKey(taskID)},
		string(status), claimedBy, terminalTTLSeconds,
	).Text()
	if err != nil {
		return fmt.Errorf("update task status %s: %w", taskID, err)
	}

	// ─── Step 2: Parse result and handle ───
	switch {
	case result == "NOT_FOUND":
		return fmt.Errorf("task %s not found", taskID)
	case result == "TERMINAL":
		return fmt.Errorf("task %s is already in a terminal state", taskID)
	case result == "SAME":
		return nil // Idempotent — no index change needed
	case strings.HasPrefix(result, "INVALID:"):
		oldStatus := TaskStatus(strings.TrimPrefix(result, "INVALID:"))
		return &InvalidTransitionError{From: oldStatus, To: status}
	case strings.HasPrefix(result, "OK:"):
		oldStatus := TaskStatus(strings.TrimPrefix(result, "OK:"))
		// ─── Step 3: Async index maintenance (best-effort, cross-slot) ───
		s.updateIndex(ctx, taskID, oldStatus, status, claimedBy)
		return nil
	default:
		return fmt.Errorf("unexpected script result: %s", result)
	}
}

// updateIndex maintains ZSET indexes after a status transition.
//
// This is async/best-effort because indexes are on {idx} slot (different from
// {id} slot where meta lives). Lua cannot atomically span both slots in
// Redis Cluster. Instead, we use a Pipeline (non-atomic) and log failures.
//
// The Reaper Safety-Net ensures correctness: if an index is stale, the Reaper
// validates against meta.status before acting.
func (s *RedisStore) updateIndex(ctx context.Context, taskID string, from, to TaskStatus, claimedBy string) {
	pipe := s.client.Pipeline()

	// Determine deadline score for running index
	var deadlineScore float64
	if to == StatusRunning {
		// Read timeout from meta HASH to compute deadline
		timeoutStr, err := s.client.HGet(ctx, s.metaKey(taskID), "timeout").Result()
		var timeoutNs int64
		if err == nil {
			timeoutNs, _ = strconv.ParseInt(timeoutStr, 10, 64)
		}
		if timeoutNs == 0 {
			timeoutNs = defaultTimeoutMillis * int64(time.Millisecond)
		}
		createdAtStr, err := s.client.HGet(ctx, s.metaKey(taskID), "createdAt").Result()
		var createdAt int64
		if err == nil {
			createdAt, _ = strconv.ParseInt(createdAtStr, 10, 64)
		}
		deadlineMs := createdAt + timeoutNs/int64(time.Millisecond)
		deadlineScore = float64(deadlineMs)
	}

	// ─── Running indexes ───
	if to == StatusRunning {
		// New running index (score=deadline)
		pipe.ZAdd(ctx, s.runningIndexKey(), redis.Z{Score: deadlineScore, Member: taskID})
		// Legacy running ZSET (score=createdAt)
		pipe.ZAdd(ctx, s.runningKey(), redis.Z{Score: deadlineScore - float64(defaultTimeoutMillis), Member: s.taskKey(taskID)})
		// Step 1 deadline ZSET (score=deadline)
		pipe.ZAdd(ctx, s.runningDeadlineKey(), redis.Z{Score: deadlineScore, Member: taskID})
	}
	if from == StatusRunning && to != StatusRunning {
		pipe.ZRem(ctx, s.runningIndexKey(), taskID)
		pipe.ZRem(ctx, s.runningKey(), s.taskKey(taskID))
		pipe.ZRem(ctx, s.runningDeadlineKey(), taskID)
	}

	// ─── Pending index: remove when leaving pending ───
	if from == StatusPending {
		pipe.ZRem(ctx, s.pendingIndexKey(), taskID)
	}

	if _, err := pipe.Exec(ctx); err != nil {
		s.logger.Debug("updateIndex: index maintenance failed (non-critical, Reaper Safety-Net covers it)",
			zap.String("task_id", taskID),
			zap.String("from", string(from)),
			zap.String("to", string(to)),
			zap.Error(err),
		)
	}
}

// DeleteTask removes a task and all its associated keys (new + legacy formats).
func (s *RedisStore) DeleteTask(ctx context.Context, taskID string) error {
	// Get task first to find group (uses new-then-legacy lookup)
	task, err := s.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return nil // Already deleted
	}

	pipe := s.client.Pipeline()

	// ─── New format keys ───
	pipe.Del(ctx, s.metaKey(taskID))
	pipe.Del(ctx, s.payloadKey(taskID))
	pipe.Del(ctx, s.newResultKey(taskID))

	// ─── Legacy format keys ───
	pipe.Del(ctx, s.taskKey(taskID))
	pipe.Del(ctx, s.resultKey(taskID))

	// ─── Indexes ───
	pipe.ZRem(ctx, s.runningKey(), s.taskKey(taskID))       // legacy running ZSET (member=key)
	pipe.ZRem(ctx, s.runningDeadlineKey(), taskID)          // Step 1 deadline ZSET (member=id)
	pipe.ZRem(ctx, s.runningIndexKey(), taskID)             // new running index (member=id)
	pipe.ZRem(ctx, s.pendingIndexKey(), taskID)             // new pending index (member=id)

	// ─── Group ───
	if task.GroupID != "" {
		pipe.SRem(ctx, s.groupKey(task.GroupID), taskID)
	}

	_, err = pipe.Exec(ctx)
	return err
}

// ListTasks returns a paginated list of tasks.
func (s *RedisStore) ListTasks(ctx context.Context, query ListQuery) (*ListPage, error) {
	// Fast path: running tasks via ZSET index (O(logN) vs O(N) SCAN)
	if query.Status == StatusRunning && query.GroupID == "" {
		return s.listRunningTasks(ctx, query)
	}

	// Slow path with group filtering
	if query.GroupID != "" {
		ids, err := s.client.SMembers(ctx, s.groupKey(query.GroupID)).Result()
		if err != nil {
			return nil, fmt.Errorf("list tasks for group %s: %w", query.GroupID, err)
		}
		tasks, err := s.getTasksChunked(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("batch get tasks: %w", err)
		}
		return filterAndPage(tasks, query), nil
	}

	// Slow path: full SCAN
	return s.listTasksSlow(ctx, query)
}

func filterAndPage(tasks []*Task, query ListQuery) *ListPage {
	var filtered []*Task
	for _, task := range tasks {
		if query.TaskType != "" && task.Type != query.TaskType {
			continue
		}
		if query.Status != "" && task.Status != query.Status {
			continue
		}
		filtered = append(filtered, task)
	}
	total := len(filtered)
	limit := queryLimit(query)
	start := query.Offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	return &ListPage{
		Tasks:  filtered[start:end],
		Total:  total,
		Offset: query.Offset,
		Limit:  limit,
	}
}

// listRunningTasks queries running tasks via ZSET index.
// Falls back to the slow SCAN path when the index is empty (cold-start / rebuild).
func (s *RedisStore) listRunningTasks(ctx context.Context, query ListQuery) (*ListPage, error) {
	// Cap to avoid context deadline from reaper (30s)
	cap := int64(min(queryLimit(query), zrangeMaxTasks))
	taskKeys, err := s.client.ZRange(ctx, s.runningKey(), 0, cap-1).Result()
	if err != nil {
		return nil, fmt.Errorf("zrange running tasks: %w", err)
	}

	// Fall back to slow path if ZSET is empty (may need bootstrap)
	if len(taskKeys) == 0 {
		return s.listTasksSlow(ctx, query)
	}

	// Extract task IDs from keys
	taskIDs := make([]string, len(taskKeys))
	prefixLen := len(fmt.Sprintf("%s:task:", s.prefix))
	for i, key := range taskKeys {
		if len(key) > prefixLen {
			taskIDs[i] = key[prefixLen:]
		}
	}

	// Batch fetch with chunking
	tasks, err := s.getTasksChunked(ctx, taskIDs)
	if err != nil {
		return nil, fmt.Errorf("batch get tasks: %w", err)
	}

	// Apply filters (Status is already guaranteed RUNNING by ZSET membership)
	var filtered []*Task
	for _, task := range tasks {
		if query.TaskType != "" && task.Type != query.TaskType {
			continue
		}
		filtered = append(filtered, task)
	}

	total := len(filtered)
	start := query.Offset
	if start > total {
		start = total
	}
	end := start + queryLimit(query)
	if end > total {
		end = total
	}

	return &ListPage{
		Tasks:  filtered[start:end],
		Total:  total,
		Offset: query.Offset,
		Limit:  queryLimit(query),
	}, nil
}

// listTasksSlow is the original SCAN-based implementation (used as fallback).
func (s *RedisStore) listTasksSlow(ctx context.Context, query ListQuery) (*ListPage, error) {
	limit := queryLimit(query)
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
	prefixLen := len(fmt.Sprintf("%s:task:", s.prefix))
	taskIDs := make([]string, 0, len(keys))
	for _, key := range keys {
		if len(key) > prefixLen {
			taskIDs = append(taskIDs, key[prefixLen:])
		}
	}

	tasks, err := s.getTasksChunked(ctx, taskIDs)
	if err != nil {
		return nil, fmt.Errorf("batch get tasks: %w", err)
	}

	var allTasks []*Task
	for _, task := range tasks {
		if query.Status != "" && task.Status != query.Status {
			continue
		}
		if query.TaskType != "" && task.Type != query.TaskType {
			continue
		}
		allTasks = append(allTasks, task)
	}

	total := len(allTasks)
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

// getTasksChunked fetches tasks in batches to avoid oversized MGET payloads.
const (
	taskMGetChunkSize = 20 // MGET batch size to avoid single oversized operation
	zrangeMaxTasks    = 200 // max running tasks per ZRANGE to stay within context deadline
)

func (s *RedisStore) getTasksChunked(ctx context.Context, taskIDs []string) ([]*Task, error) {
	if len(taskIDs) == 0 {
		return nil, nil
	}
	var all []*Task
	for i := 0; i < len(taskIDs); i += taskMGetChunkSize {
		end := i + taskMGetChunkSize
		if end > len(taskIDs) {
			end = len(taskIDs)
		}
		batch, err := s.GetTasks(ctx, taskIDs[i:end])
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
	}
	return all, nil
}

func queryLimit(query ListQuery) int {
	if query.Limit <= 0 {
		return 100
	}
	return query.Limit
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
//
// Optimized approach (Plan C):
//   - Phase 1 (O(1)): SCARD group → Total; ZCARD running/pending indexes
//   - Phase 2 (O(N) with Pipeline, N = group size): SMEMBERS + Pipeline HGET status
//     for terminal state breakdown (Completed/Failed/Timeout/Cancelled)
//
// This avoids loading full Task payloads (~2KB each) and uses only lightweight
// metadata access. Typical group size is 20~100, so Phase 2 is < 2ms.
//
// Falls back to the legacy ListTasks path when groupID is empty (rare admin query).
func (s *RedisStore) GetProgress(ctx context.Context, taskType TaskType, groupID string) (*Progress, error) {
	// Fallback: when no groupID specified, use legacy path (admin/debug scenario)
	if groupID == "" {
		return s.getProgressLegacy(ctx, taskType, groupID)
	}

	// ─── Phase 1: O(1) counts from existing indexes ───
	// Get all task IDs in the group (needed for Total and Phase 2)
	ids, err := s.client.SMembers(ctx, s.groupKey(groupID)).Result()
	if err != nil {
		return nil, fmt.Errorf("get progress: smembers group %s: %w", groupID, err)
	}

	if len(ids) == 0 {
		return &Progress{}, nil
	}

	// ─── Phase 2: Pipeline HGET status for all tasks in group ───
	// Use Pipeline for a single round-trip regardless of group size.
	pipe := s.client.Pipeline()
	statusCmds := make([]*redis.StringCmd, len(ids))
	typeCmds := make([]*redis.StringCmd, 0, len(ids))

	// If taskType filter is specified, we also need to read the type field
	needTypeFilter := taskType != ""
	if needTypeFilter {
		typeCmds = make([]*redis.StringCmd, len(ids))
	}

	for i, id := range ids {
		statusCmds[i] = pipe.HGet(ctx, s.metaKey(id), "status")
		if needTypeFilter {
			typeCmds[i] = pipe.HGet(ctx, s.metaKey(id), "type")
		}
	}
	_, _ = pipe.Exec(ctx) // Errors checked per command below

	// ─── Phase 3: Aggregate status counts ───
	p := &Progress{}
	for i := range ids {
		status, err := statusCmds[i].Result()
		if err != nil {
			// Task may have been deleted/expired between SMEMBERS and HGET — skip
			continue
		}

		// Apply taskType filter if specified
		if needTypeFilter {
			taskTypeVal, tErr := typeCmds[i].Result()
			if tErr != nil || TaskType(taskTypeVal) != taskType {
				continue
			}
		}

		p.Total++
		switch TaskStatus(status) {
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

// getProgressLegacy is the fallback path for GetProgress when no groupID is
// specified. Uses ListTasks with full Task loading (admin/debug scenario only).
func (s *RedisStore) getProgressLegacy(ctx context.Context, taskType TaskType, groupID string) (*Progress, error) {
	page, err := s.ListTasks(ctx, ListQuery{
		TaskType: taskType,
		GroupID:  groupID,
		Limit:    10000,
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

// SubscribeEvents subscribes to all task lifecycle events via Redis PSubscribe.
// It uses the pattern {prefix}:events:* to receive events from all nodes.
//
// Lifecycle:
//   - ctx cancellation → close returned channel → goroutine exits
//   - Redis disconnection → go-redis auto-reconnect, events resume
//   - Caller must NOT close the returned channel
func (s *RedisStore) SubscribeEvents(ctx context.Context) (<-chan TaskEvent, error) {
	pattern := fmt.Sprintf("%s:events:*", s.prefix)
	pubsub := s.client.PSubscribe(ctx, pattern)
	ch := make(chan TaskEvent, 256)

	go func() {
		defer close(ch)
		defer pubsub.Close()

		msgCh := pubsub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-msgCh:
				if !ok {
					return
				}
				var event TaskEvent
				if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
					s.logger.Debug("Failed to unmarshal event from Pub/Sub, skipping",
						zap.String("channel", msg.Channel),
						zap.Error(err),
					)
					continue
				}
				select {
				case ch <- event:
				default:
					s.logger.Debug("Event channel full, dropping event",
						zap.String("type", string(event.Type)),
						zap.String("task_id", event.TaskID),
					)
				}
			}
		}
	}()

	return ch, nil
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

// ─── Reaper Optimized Path ───

// reaperMaxBatch limits the number of overdue task IDs returned per call
// to provide backpressure and avoid overwhelming the Reaper.
const reaperMaxBatch = 500

// GetOverdueRunningTasks returns task IDs whose deadline has been exceeded.
// Uses ZRANGEBYSCORE on the running_deadlines ZSET (score = deadline millis).
// This is an O(logN + K) operation with zero JSON deserialization.
func (s *RedisStore) GetOverdueRunningTasks(ctx context.Context, nowMillis int64) ([]string, error) {
	taskIDs, err := s.client.ZRangeByScore(ctx, s.runningDeadlineKey(), &redis.ZRangeBy{
		Min:   "-inf",
		Max:   fmt.Sprintf("%d", nowMillis),
		Count: reaperMaxBatch,
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("get overdue running tasks: %w", err)
	}
	return taskIDs, nil
}

// ─── Metadata-Only Access ───

// GetTaskMeta retrieves only the metadata of a task (no Payload, ~200B).
// Reads directly from the HASH meta key — O(1) with zero JSON deserialization.
// Falls back to legacy STRING format during migration.
func (s *RedisStore) GetTaskMeta(ctx context.Context, taskID string) (*TaskMeta, error) {
	// Try new HASH format first
	meta, err := s.client.HGetAll(ctx, s.metaKey(taskID)).Result()
	if err == nil && len(meta) > 0 {
		return metaMapToTaskMeta(meta), nil
	}

	// Fallback to legacy STRING
	task, err := s.getTaskLegacy(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, nil
	}
	return taskToMeta(task), nil
}

// GetTasksMeta retrieves metadata for multiple tasks in a single batch.
// Uses pipeline HGETALL for efficiency — no Payload deserialization.
func (s *RedisStore) GetTasksMeta(ctx context.Context, taskIDs []string) ([]*TaskMeta, error) {
	if len(taskIDs) == 0 {
		return nil, nil
	}

	// Pipeline HGETALL for all tasks
	pipe := s.client.Pipeline()
	cmds := make([]*redis.MapStringStringCmd, len(taskIDs))
	for i, id := range taskIDs {
		cmds[i] = pipe.HGetAll(ctx, s.metaKey(id))
	}
	_, _ = pipe.Exec(ctx)

	metas := make([]*TaskMeta, 0, len(taskIDs))
	var fallbackIDs []string

	for i, cmd := range cmds {
		meta, err := cmd.Result()
		if err == nil && len(meta) > 0 {
			metas = append(metas, metaMapToTaskMeta(meta))
			continue
		}
		fallbackIDs = append(fallbackIDs, taskIDs[i])
	}

	// Fallback for legacy format tasks
	if len(fallbackIDs) > 0 {
		legacyTasks, err := s.getTasksLegacy(ctx, fallbackIDs)
		if err != nil {
			return nil, err
		}
		for _, task := range legacyTasks {
			metas = append(metas, taskToMeta(task))
		}
	}

	return metas, nil
}

// getTaskLegacy reads a task using the old STRING format only.
func (s *RedisStore) getTaskLegacy(ctx context.Context, taskID string) (*Task, error) {
	data, err := s.client.Get(ctx, s.taskKey(taskID)).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get task %s (legacy): %w", taskID, err)
	}
	var task Task
	if err := json.Unmarshal([]byte(data), &task); err != nil {
		return nil, fmt.Errorf("unmarshal task %s (legacy): %w", taskID, err)
	}
	return &task, nil
}

// taskToMeta extracts metadata fields from a full Task.
func taskToMeta(task *Task) *TaskMeta {
	return &TaskMeta{
		ID:         task.ID,
		Type:       task.Type,
		Status:     task.Status,
		CreatedAt:  task.CreatedAt,
		Timeout:    task.Timeout,
		ClaimedBy:  task.ClaimedBy,
		GroupID:    task.GroupID,
		Priority:   task.Priority,
		MaxRetries: task.MaxRetries,
		RetryCount: task.RetryCount,
	}
}
