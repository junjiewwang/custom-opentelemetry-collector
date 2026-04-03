// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

// Redis key patterns
const (
	keyPendingGlobal      = "%s:pending:global"        // List: global pending tasks
	keyPendingAgent       = "%s:pending:%s"            // List: agent-specific pending tasks
	keyTaskDetail         = "%s:detail:%s"             // String: task details JSON
	keyCancelled          = "%s:cancelled"             // Set: cancelled task IDs
	keyResult             = "%s:result:%s"             // String: task result JSON
	keyRunning            = "%s:running"               // Hash: taskID -> agentID
	keyRunningByStartedAt = "%s:running:by_started_at"  // ZSET: taskID scored by started_at_millis
	keyEventSubmitted     = "%s:events:task:submitted" // Pub/Sub channel
	keyEventCompleted     = "%s:events:task:completed" // Pub/Sub channel
	keyEventCancelled     = "%s:events:task:cancelled" // Pub/Sub channel
	keyTaskIndexCreatedAt = "%s:idx:tasks:created_at"  // ZSET: taskID scored by created_at_millis
	keyTaskIndexStatus    = "%s:idx:tasks:status:%d"   // ZSET: taskID scored by created_at_millis
	keyTaskIndexApp       = "%s:idx:tasks:app:%s"      // ZSET: taskID scored by created_at_millis
	keyTaskIndexService   = "%s:idx:tasks:service:%s"  // ZSET: taskID scored by created_at_millis
	keyTaskIndexAgent     = "%s:idx:tasks:agent:%s"    // ZSET: taskID scored by created_at_millis
	keyTaskIndexType      = "%s:idx:tasks:type:%s"     // ZSET: taskID scored by created_at_millis
	keyTaskIndexMembers   = "%s:idx:tasks:members:%s"  // Set: taskID -> index keys
	keyTaskQueryTemp      = "%s:idx:tasks:query:%d:%d" // ZSET: temporary query index
)

// RedisTaskStore implements TaskStore using Redis as backend.
// Uses model-only storage format (no v1/v2 migration).
type RedisTaskStore struct {
	logger *zap.Logger
	client redis.UniversalClient

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

func (s *RedisTaskStore) runningByStartedAtKey() string {
	return fmt.Sprintf(keyRunningByStartedAt, s.keyPrefix)
}

func (s *RedisTaskStore) taskCreatedAtIndexKey() string {
	return fmt.Sprintf(keyTaskIndexCreatedAt, s.keyPrefix)
}

func (s *RedisTaskStore) taskStatusIndexKey(status model.TaskStatus) string {
	return fmt.Sprintf(keyTaskIndexStatus, s.keyPrefix, int(status))
}

func (s *RedisTaskStore) taskAppIndexKey(appID string) string {
	return fmt.Sprintf(keyTaskIndexApp, s.keyPrefix, appID)
}

func (s *RedisTaskStore) taskServiceIndexKey(serviceName string) string {
	return fmt.Sprintf(keyTaskIndexService, s.keyPrefix, serviceName)
}

func (s *RedisTaskStore) taskAgentIndexKey(agentID string) string {
	return fmt.Sprintf(keyTaskIndexAgent, s.keyPrefix, agentID)
}

func (s *RedisTaskStore) taskTypeIndexKey(taskType string) string {
	return fmt.Sprintf(keyTaskIndexType, s.keyPrefix, taskType)
}

func (s *RedisTaskStore) taskIndexMembersKey(taskID string) string {
	return fmt.Sprintf(keyTaskIndexMembers, s.keyPrefix, taskID)
}

func (s *RedisTaskStore) taskQueryTempKey() string {
	return fmt.Sprintf(keyTaskQueryTemp, s.keyPrefix, time.Now().UnixNano(), rand.Int63())
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

func (s *RedisTaskStore) taskIndexScore(info *TaskInfo) float64 {
	if info == nil {
		return 0
	}
	return float64(info.CreatedAtMillis)
}

func (s *RedisTaskStore) taskInfoIndexKeys(info *TaskInfo) []string {
	if info == nil || info.Task == nil || info.Task.ID == "" {
		return nil
	}

	keys := []string{
		s.taskCreatedAtIndexKey(),
		s.taskStatusIndexKey(info.Status),
	}
	if info.AppID != "" {
		keys = append(keys, s.taskAppIndexKey(info.AppID))
	}
	if info.ServiceName != "" {
		keys = append(keys, s.taskServiceIndexKey(info.ServiceName))
	}
	if info.AgentID != "" {
		keys = append(keys, s.taskAgentIndexKey(info.AgentID))
	}
	if info.Task.TypeName != "" {
		keys = append(keys, s.taskTypeIndexKey(info.Task.TypeName))
	}
	return uniqueSortedStrings(keys)
}

func uniqueSortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if len(result) == 0 {
		return nil
	}

	sort.Strings(result)
	return result
}

func stringSliceToAny(values []string) []any {
	if len(values) == 0 {
		return nil
	}

	result := make([]any, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func (s *RedisTaskStore) taskIndexMemberKeys(ctx context.Context, client redis.UniversalClient, taskID string) ([]string, error) {
	keys, err := client.SMembers(ctx, s.taskIndexMembersKey(taskID)).Result()
	if err != nil && err != redis.Nil {
		return nil, fmt.Errorf("list task index members: %w", err)
	}
	return uniqueSortedStrings(keys), nil
}

func (s *RedisTaskStore) rebuildTaskIndexesForTask(ctx context.Context, client redis.UniversalClient, info *TaskInfo) error {
	if info == nil || info.Task == nil || info.Task.ID == "" {
		return errors.New("task info/task is nil")
	}

	taskID := info.Task.ID
	oldKeys, err := s.taskIndexMemberKeys(ctx, client, taskID)
	if err != nil {
		return err
	}
	newKeys := s.taskInfoIndexKeys(info)

	pipe := client.TxPipeline()
	for _, key := range oldKeys {
		pipe.ZRem(ctx, key, taskID)
	}
	for _, key := range newKeys {
		pipe.ZAdd(ctx, key, redis.Z{Score: s.taskIndexScore(info), Member: taskID})
	}
	pipe.Del(ctx, s.taskIndexMembersKey(taskID))
	if len(newKeys) > 0 {
		pipe.SAdd(ctx, s.taskIndexMembersKey(taskID), stringSliceToAny(newKeys)...)
	}

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("rebuild task indexes: %w", err)
	}
	return nil
}

func (s *RedisTaskStore) cleanupMissingTaskArtifacts(ctx context.Context, client redis.UniversalClient, taskIDs []string) {
	for _, taskID := range uniqueSortedStrings(taskIDs) {
		memberKeys, err := s.taskIndexMemberKeys(ctx, client, taskID)
		if err != nil {
			s.logger.Warn("Failed to load task index members for cleanup", zap.String("task_id", taskID), zap.Error(err))
			continue
		}

		pipe := client.TxPipeline()
		for _, key := range memberKeys {
			pipe.ZRem(ctx, key, taskID)
		}
		pipe.Del(ctx, s.taskIndexMembersKey(taskID))
		pipe.Del(ctx, s.resultKey(taskID))
		pipe.SRem(ctx, s.cancelledKey(), taskID)
		pipe.HDel(ctx, s.runningKey(), taskID)

		if _, err := pipe.Exec(ctx); err != nil {
			s.logger.Warn("Failed to cleanup stale task artifacts", zap.String("task_id", taskID), zap.Error(err))
		}
	}
}

func (s *RedisTaskStore) removeTaskIDsFromSortedSet(ctx context.Context, client redis.UniversalClient, key string, taskIDs []string) {
	if key == "" || len(taskIDs) == 0 {
		return
	}

	if err := client.ZRem(ctx, key, stringSliceToAny(uniqueSortedStrings(taskIDs))...).Err(); err != nil {
		s.logger.Debug("Failed to cleanup task IDs from sorted set", zap.String("key", key), zap.Int("task_count", len(taskIDs)), zap.Error(err))
	}
}

func (s *RedisTaskStore) cleanupTempKeys(ctx context.Context, client redis.UniversalClient, keys []string) {
	keys = uniqueSortedStrings(keys)
	if len(keys) == 0 {
		return
	}
	if err := client.Del(ctx, keys...).Err(); err != nil {
		s.logger.Debug("Failed to cleanup temporary task query keys", zap.Int("key_count", len(keys)), zap.Error(err))
	}
}

// ===== Task Detail Operations =====

// SaveTaskInfo implements TaskStore.
func (s *RedisTaskStore) SaveTaskInfo(ctx context.Context, info *TaskInfo, isNew bool) error {
	client, err := s.getClient()
	if err != nil {
		return err
	}
	if info == nil || info.Task == nil {
		return errors.New("task info/task is nil")
	}

	data, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("marshal task info: %w", err)
	}

	key := s.detailKey(info.Task.ID)

	if isNew {
		ok, err := client.SetNX(ctx, key, data, s.ttl).Result()
		if err != nil {
			return fmt.Errorf("save task info: %w", err)
		}
		if !ok {
			return errors.New("task already exists: " + info.Task.ID)
		}
	} else {
		if err := client.Set(ctx, key, data, s.ttl).Err(); err != nil {
			return fmt.Errorf("save task info: %w", err)
		}
	}

	if err := s.rebuildTaskIndexesForTask(ctx, client, info); err != nil {
		return err
	}
	return nil
}

// GetTaskInfo implements TaskStore.
func (s *RedisTaskStore) GetTaskInfo(ctx context.Context, taskID string) (*TaskInfo, error) {
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}

	key := s.detailKey(taskID)
	data, err := client.Get(ctx, key).Result()
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
			return TaskNotFound(taskID)
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
		if err := updater(&info); err != nil {
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
			return s.rebuildTaskIndexesForTask(ctx, client, &info)
		case 0: // Version mismatch - retry with fresh data
			s.logger.Debug("Version mismatch, retrying with backoff",
				zap.String("task_id", taskID),
				zap.Int("attempt", attempt+1),
				zap.Int("max_retries", maxRetries),
				zap.Int64("expected_version", expectedVersion),
				zap.Int32("current_status", int32(currentStatus)),
			)
			// Exponential backoff with jitter
			backoff := time.Duration(1<<uint(attempt)) * baseBackoff
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			jitter := time.Duration(rand.Int63n(int64(backoff / 2)))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff + jitter):
			}
			continue
		case -1: // Not found (deleted between read and write)
			return TaskNotFound(taskID)
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
var applyTaskResultScript = redis.NewScript(fmt.Sprintf(`
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
    return %s
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
if cur == %d and new == %d then
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

if new == %d then
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
`, luaTerminalStatusExpr("s"), taskStatusRunningCode, taskStatusPendingCode, taskStatusRunningCode))

// applyCancelScript atomically cancels a task in the task detail record.
// Return: {code, agent_id, status}
var applyCancelScript = redis.NewScript(fmt.Sprintf(`
local current = redis.call('GET', KEYS[1])
if not current then
    return {-1, '', 0}
end

local info = cjson.decode(current)
local cur = tonumber(info.status) or 0
local now = tonumber(ARGV[1])
local ttl = tonumber(ARGV[2])

local function is_terminal(s)
    return %s
end

local existing_agent = info.agent_id
if not existing_agent then existing_agent = '' end

-- Idempotent: already cancelled.
if cur == %d then
    return {2, existing_agent, cur}
end

-- Reject cancelling a non-cancelled terminal task.
if is_terminal(cur) then
    return {-2, existing_agent, cur}
end

info.status = %d
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
return {1, after_agent, %d}
`, luaTerminalStatusExpr("s"), taskStatusCancelledCode, taskStatusCancelledCode, taskStatusCancelledCode))

// applySetRunningScript atomically sets a task to RUNNING in the task detail record.
// Return: {code, agent_id, status}
var applySetRunningScript = redis.NewScript(fmt.Sprintf(`
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
    return %s
end

local existing_agent = info.agent_id
if not existing_agent then existing_agent = '' end

-- Idempotent.
if cur == %d then
    return {2, existing_agent, cur}
end

-- Reject terminal.
if is_terminal(cur) then
    return {-2, existing_agent, cur}
end

info.status = %d
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
return {1, after_agent, %d}
`, luaTerminalStatusExpr("s"), taskStatusRunningCode, taskStatusRunningCode, taskStatusRunningCode))

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
		Status:  model.TaskStatus(statusNum),
		AgentID: agentID,
	}, nil
}

func (s *RedisTaskStore) ApplyTaskResult(ctx context.Context, taskID string, result *model.TaskResult, nowMillis int64) (ApplyTaskUpdateResult, error) {
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
	if res.Code == ApplyTaskUpdated {
		info, err := s.GetTaskInfo(ctx, taskID)
		if err != nil {
			return ApplyTaskUpdateResult{}, err
		}
		if info != nil {
			if err := s.rebuildTaskIndexesForTask(ctx, client, info); err != nil {
				return ApplyTaskUpdateResult{}, err
			}
		}
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
	if res.Code == ApplyTaskUpdated {
		info, err := s.GetTaskInfo(ctx, taskID)
		if err != nil {
			return ApplyTaskUpdateResult{}, err
		}
		if info != nil {
			if err := s.rebuildTaskIndexesForTask(ctx, client, info); err != nil {
				return ApplyTaskUpdateResult{}, err
			}
		}
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
	if res.Code == ApplyTaskUpdated {
		info, err := s.GetTaskInfo(ctx, taskID)
		if err != nil {
			return ApplyTaskUpdateResult{}, err
		}
		if info != nil {
			if err := s.rebuildTaskIndexesForTask(ctx, client, info); err != nil {
				return ApplyTaskUpdateResult{}, err
			}
		}
	}

	return res, nil
}

// ListTaskInfos implements TaskStore.
func (s *RedisTaskStore) ListTaskInfos(ctx context.Context) ([]*TaskInfo, error) {
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}

	return s.collectTaskInfosFromIndex(ctx, client, s.taskCreatedAtIndexKey(), TaskListQuery{}, 0, 0)
}

// ListTaskInfosPage implements TaskStore.
func (s *RedisTaskStore) ListTaskInfosPage(ctx context.Context, query TaskListQuery) (TaskListPage, error) {
	client, err := s.getClient()
	if err != nil {
		return TaskListPage{}, err
	}

	query = normalizeTaskListQuery(query)
	sc, legacyOffset, err := parseSeekCursor(query.Cursor)
	if err != nil {
		return TaskListPage{}, err
	}

	sourceKey, tempKeys, err := s.buildTaskQuerySource(ctx, client, query)
	if err != nil {
		return TaskListPage{}, err
	}
	defer s.cleanupTempKeys(ctx, client, tempKeys)

	if query.Limit <= 0 {
		items, err := s.collectTaskInfosFromIndex(ctx, client, sourceKey, query, 0, 0)
		if err != nil {
			return TaskListPage{}, err
		}
		return TaskListPage{Items: items}, nil
	}

	// seek cursor 模式：从 (LastScore, LastID) 之后继续取
	if sc != nil {
		return s.seekPageFromIndex(ctx, client, sourceKey, query, sc)
	}

	// 向后兼容：旧 offset cursor 模式
	items, err := s.collectTaskInfosFromIndex(ctx, client, sourceKey, query, legacyOffset, query.Limit+1)
	if err != nil {
		return TaskListPage{}, err
	}

	page := TaskListPage{Items: items}
	if len(items) > query.Limit {
		page.Items = items[:query.Limit]
		page.HasMore = true
		lastItem := page.Items[len(page.Items)-1]
		if lastItem != nil && lastItem.Task != nil {
			page.NextCursor = encodeSeekCursor(&seekCursor{
				LastScore: lastItem.CreatedAtMillis,
				LastID:    lastItem.Task.ID,
			})
		}
	}
	return page, nil
}

// seekPageFromIndex 基于 seek cursor 从 ZSET 索引中取下一页数据。
// 使用 ZREVRANGEBYSCORE 从 LastScore 开始向前取，跳过 LastID 及之前的记录。
func (s *RedisTaskStore) seekPageFromIndex(ctx context.Context, client redis.UniversalClient, sourceKey string, query TaskListQuery, sc *seekCursor) (TaskListPage, error) {
	targetCount := query.Limit + 1
	collected := make([]*TaskInfo, 0, targetCount)
	currentMaxScore := fmt.Sprintf("%d", sc.LastScore)
	passedLastID := false

	for len(collected) < targetCount {
		// 多取一些以应对脏索引和同分值跳过
		fetchCount := int64((targetCount - len(collected)) * 3)
		if fetchCount < 20 {
			fetchCount = 20
		}
		if fetchCount > 200 {
			fetchCount = 200
		}

		ids, err := client.ZRevRangeByScore(ctx, sourceKey, &redis.ZRangeBy{
			Min:    "-inf",
			Max:    currentMaxScore,
			Offset: 0,
			Count:  fetchCount,
		}).Result()
		if err != nil {
			return TaskListPage{}, fmt.Errorf("seek query task index: %w", err)
		}
		if len(ids) == 0 {
			break
		}

		// 过滤掉 cursor 之前（含）的记录
		filteredIDs := make([]string, 0, len(ids))
		for _, id := range ids {
			if !passedLastID {
				// 还没跳过 LastID，需要检查
				score, err := client.ZScore(ctx, sourceKey, id).Result()
				if err != nil {
					continue
				}
				scoreInt := int64(score)
				if scoreInt > sc.LastScore {
					// 分值更大，跳过（不应该出现在 ZREVRANGEBYSCORE 结果中，但防御性处理）
					continue
				}
				if scoreInt == sc.LastScore && id >= sc.LastID {
					// 同分值且 ID >= LastID，跳过
					continue
				}
				passedLastID = true
			}
			filteredIDs = append(filteredIDs, id)
		}

		if len(filteredIDs) > 0 {
			infos, invalidIDs, err := s.loadTaskInfosByIDs(ctx, client, filteredIDs, query)
			if err != nil {
				return TaskListPage{}, err
			}
			if len(invalidIDs) > 0 {
				s.removeTaskIDsFromSortedSet(ctx, client, sourceKey, invalidIDs)
			}
			collected = append(collected, infos...)
		}

		// 更新下一轮的 max score 为本批最后一个 ID 的分值
		lastID := ids[len(ids)-1]
		lastScore, err := client.ZScore(ctx, sourceKey, lastID).Result()
		if err == nil {
			nextMax := int64(lastScore)
			currentMaxScore = fmt.Sprintf("%d", nextMax)
		}

		if int64(len(ids)) < fetchCount {
			break
		}
	}

	page := TaskListPage{Items: collected}
	if len(collected) > query.Limit {
		page.Items = collected[:query.Limit]
		page.HasMore = true
		lastItem := page.Items[len(page.Items)-1]
		if lastItem != nil && lastItem.Task != nil {
			page.NextCursor = encodeSeekCursor(&seekCursor{
				LastScore: lastItem.CreatedAtMillis,
				LastID:    lastItem.Task.ID,
			})
		}
	}
	return page, nil
}

func (s *RedisTaskStore) buildTaskQuerySource(ctx context.Context, client redis.UniversalClient, query TaskListQuery) (string, []string, error) {
	filterKeys := make([]string, 0, 5)
	tempKeys := make([]string, 0, 2)

	statusKeys := make([]string, 0, len(query.Statuses))
	statusSeen := make(map[model.TaskStatus]struct{}, len(query.Statuses))
	for _, status := range query.Statuses {
		if _, ok := statusSeen[status]; ok {
			continue
		}
		statusSeen[status] = struct{}{}
		statusKeys = append(statusKeys, s.taskStatusIndexKey(status))
	}
	if len(statusKeys) == 1 {
		filterKeys = append(filterKeys, statusKeys[0])
	} else if len(statusKeys) > 1 {
		unionKey := s.taskQueryTempKey()
		if _, err := client.ZUnionStore(ctx, unionKey, &redis.ZStore{Keys: statusKeys, Aggregate: "MAX"}).Result(); err != nil {
			return "", nil, fmt.Errorf("build status query index: %w", err)
		}
		if err := client.Expire(ctx, unionKey, 15*time.Second).Err(); err != nil {
			return "", nil, fmt.Errorf("expire status query index: %w", err)
		}
		filterKeys = append(filterKeys, unionKey)
		tempKeys = append(tempKeys, unionKey)
	}

	if query.AppID != "" {
		filterKeys = append(filterKeys, s.taskAppIndexKey(query.AppID))
	}
	if query.ServiceName != "" {
		filterKeys = append(filterKeys, s.taskServiceIndexKey(query.ServiceName))
	}
	if query.AgentID != "" {
		filterKeys = append(filterKeys, s.taskAgentIndexKey(query.AgentID))
	}
	if query.TaskType != "" {
		filterKeys = append(filterKeys, s.taskTypeIndexKey(query.TaskType))
	}

	if len(filterKeys) == 0 {
		return s.taskCreatedAtIndexKey(), tempKeys, nil
	}
	if len(filterKeys) == 1 {
		return filterKeys[0], tempKeys, nil
	}

	intersectionKey := s.taskQueryTempKey()
	if _, err := client.ZInterStore(ctx, intersectionKey, &redis.ZStore{Keys: filterKeys, Aggregate: "MAX"}).Result(); err != nil {
		return "", nil, fmt.Errorf("build filtered task query index: %w", err)
	}
	if err := client.Expire(ctx, intersectionKey, 15*time.Second).Err(); err != nil {
		return "", nil, fmt.Errorf("expire filtered task query index: %w", err)
	}
	tempKeys = append(tempKeys, intersectionKey)
	return intersectionKey, tempKeys, nil
}

func (s *RedisTaskStore) collectTaskInfosFromIndex(ctx context.Context, client redis.UniversalClient, sourceKey string, query TaskListQuery, offset int, targetCount int) ([]*TaskInfo, error) {
	collected := make([]*TaskInfo, 0)
	currentOffset := offset
	batchSize := 200
	if targetCount > 0 {
		batchSize = targetCount * 2
		if batchSize < 20 {
			batchSize = 20
		}
		if batchSize > 200 {
			batchSize = 200
		}
	}

	for {
		if targetCount > 0 && len(collected) >= targetCount {
			break
		}

		ids, err := client.ZRevRange(ctx, sourceKey, int64(currentOffset), int64(currentOffset+batchSize-1)).Result()
		if err != nil {
			return nil, fmt.Errorf("query task index: %w", err)
		}
		if len(ids) == 0 {
			break
		}

		infos, invalidIDs, err := s.loadTaskInfosByIDs(ctx, client, ids, query)
		if err != nil {
			return nil, err
		}
		if len(invalidIDs) > 0 {
			s.removeTaskIDsFromSortedSet(ctx, client, sourceKey, invalidIDs)
		}
		if len(infos) == 0 && len(invalidIDs) == 0 {
			break
		}

		collected = append(collected, infos...)
		currentOffset = offset + len(collected)
		if len(ids) < batchSize {
			break
		}
	}

	return collected, nil
}

func (s *RedisTaskStore) loadTaskInfosByIDs(ctx context.Context, client redis.UniversalClient, taskIDs []string, query TaskListQuery) ([]*TaskInfo, []string, error) {
	if len(taskIDs) == 0 {
		return nil, nil, nil
	}

	detailKeys := make([]string, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		detailKeys = append(detailKeys, s.detailKey(taskID))
	}

	results, err := client.MGet(ctx, detailKeys...).Result()
	if err != nil {
		return nil, nil, fmt.Errorf("mget task details: %w", err)
	}

	infos := make([]*TaskInfo, 0, len(results))
	invalidIDs := make([]string, 0)
	staleIDs := make([]string, 0)
	for i, data := range results {
		taskID := taskIDs[i]
		if data == nil {
			invalidIDs = append(invalidIDs, taskID)
			staleIDs = append(staleIDs, taskID)
			continue
		}

		dataStr, ok := data.(string)
		if !ok {
			invalidIDs = append(invalidIDs, taskID)
			staleIDs = append(staleIDs, taskID)
			continue
		}

		var info TaskInfo
		if err := json.Unmarshal([]byte(dataStr), &info); err != nil {
			s.logger.Warn("Failed to unmarshal task info", zap.String("task_id", taskID), zap.Error(err))
			invalidIDs = append(invalidIDs, taskID)
			staleIDs = append(staleIDs, taskID)
			continue
		}
		if info.Task == nil || info.Task.ID == "" || info.Task.ID != taskID {
			invalidIDs = append(invalidIDs, taskID)
			staleIDs = append(staleIDs, taskID)
			continue
		}
		if !taskInfoMatchesQuery(&info, query) {
			if err := s.rebuildTaskIndexesForTask(ctx, client, &info); err != nil {
				return nil, nil, err
			}
			invalidIDs = append(invalidIDs, taskID)
			continue
		}

		infos = append(infos, cloneTaskInfo(&info))
	}

	if len(staleIDs) > 0 {
		s.cleanupMissingTaskArtifacts(ctx, client, staleIDs)
	}
	return infos, invalidIDs, nil
}

func (s *RedisTaskStore) loadTaskInfosByDetailKeys(ctx context.Context, client redis.UniversalClient, keys []string) ([]*TaskInfo, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	results, err := client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("mget task detail keys: %w", err)
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
		if info.Task == nil || info.Task.ID == "" {
			continue
		}
		tasks = append(tasks, &info)
	}

	return tasks, nil
}

func (s *RedisTaskStore) backfillTaskIndexes(ctx context.Context, client redis.UniversalClient) error {
	// 检查全局索引是否已存在，如果已有数据则跳过回填
	count, err := client.ZCard(ctx, s.taskCreatedAtIndexKey()).Result()
	if err == nil && count > 0 {
		s.logger.Info("Task indexes already exist, skipping backfill",
			zap.Int64("index_count", count))
		return nil
	}

	const batchSize = 200

	pattern := fmt.Sprintf("%s:detail:*", s.keyPrefix)
	iter := client.Scan(ctx, 0, pattern, 100).Iterator()
	keyBatch := make([]string, 0, batchSize)
	indexed := 0

	flushBatch := func() error {
		if len(keyBatch) == 0 {
			return nil
		}
		infos, err := s.loadTaskInfosByDetailKeys(ctx, client, keyBatch)
		if err != nil {
			return err
		}
		for _, info := range infos {
			if err := s.rebuildTaskIndexesForTask(ctx, client, info); err != nil {
				return err
			}
			indexed++
		}
		keyBatch = keyBatch[:0]
		return nil
	}

	for iter.Next(ctx) {
		keyBatch = append(keyBatch, iter.Val())
		if len(keyBatch) >= batchSize {
			if err := flushBatch(); err != nil {
				return fmt.Errorf("backfill task indexes: %w", err)
			}
		}
	}
	if err := iter.Err(); err != nil {
		return fmt.Errorf("scan task detail keys for index backfill: %w", err)
	}
	if err := flushBatch(); err != nil {
		return fmt.Errorf("backfill task indexes: %w", err)
	}

	if indexed > 0 {
		s.logger.Info("Backfilled Redis task list indexes", zap.Int("task_count", indexed))
	}
	return nil
}

// DeleteTaskInfo implements TaskStore.
func (s *RedisTaskStore) DeleteTaskInfo(ctx context.Context, taskID string) error {
	client, err := s.getClient()
	if err != nil {
		return err
	}

	memberKeys, err := s.taskIndexMemberKeys(ctx, client, taskID)
	if err != nil {
		return err
	}

	pipe := client.TxPipeline()
	pipe.Del(ctx, s.detailKey(taskID))
	pipe.Del(ctx, s.resultKey(taskID))
	pipe.SRem(ctx, s.cancelledKey(), taskID)
	pipe.HDel(ctx, s.runningKey(), taskID)
	for _, key := range memberKeys {
		pipe.ZRem(ctx, key, taskID)
	}
	pipe.Del(ctx, s.taskIndexMembersKey(taskID))

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

	if len(results) < 2 {
		return "", nil
	}

	return s.resolveQueueItem(results[1]), nil
}

// DequeueTaskMulti implements TaskStore.
func (s *RedisTaskStore) DequeueTaskMulti(ctx context.Context, queueIDs []string, timeout time.Duration) (string, error) {
	client, err := s.getClient()
	if err != nil {
		return "", err
	}

	keys := make([]string, 0, len(queueIDs))
	for _, queueID := range queueIDs {
		keys = append(keys, s.queueKey(queueID))
	}

	results, err := client.BRPop(ctx, timeout, keys...).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("dequeue task: %w", err)
	}

	if len(results) < 2 {
		return "", nil
	}

	taskID := s.resolveQueueItem(results[1])
	if taskID == "" {
		return "", nil
	}

	// Best-effort cross-queue de-dup.
	if len(queueIDs) > 0 {
		_ = s.RemoveFromAllQueues(ctx, taskID, queueIDs[0])
	}

	return taskID, nil
}

// resolveQueueItem handles both new format (task_id) and legacy format (JSON).
func (s *RedisTaskStore) resolveQueueItem(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	// Check if it's legacy JSON format
	if strings.HasPrefix(value, "{") {
		var task model.Task
		if err := json.Unmarshal([]byte(value), &task); err != nil {
			s.logger.Warn("Failed to unmarshal legacy queue item", zap.Error(err))
			return ""
		}
		return task.ID
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
		if err == redis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("peek queue: %w", err)
	}

	out := make([]string, 0, len(results))
	for _, item := range results {
		if taskID := s.resolveQueueItem(item); taskID != "" {
			out = append(out, taskID)
		}
	}

	return out, nil
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
	pipe.LRem(ctx, s.queueKey(QueueGlobal), 0, taskID)
	// Remove from agent queue
	if agentID != "" {
		pipe.LRem(ctx, s.queueKey(agentID), 0, taskID)
	}

	_, err = pipe.Exec(ctx)
	return err
}

// ===== Result Operations =====

// SaveResult implements TaskStore.
func (s *RedisTaskStore) SaveResult(ctx context.Context, result *model.TaskResult) error {
	client, err := s.getClient()
	if err != nil {
		return err
	}
	if result == nil {
		return errors.New("result is nil")
	}

	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}

	return client.Set(ctx, s.resultKey(result.TaskID), data, s.ttl).Err()
}

// GetResult implements TaskStore.
func (s *RedisTaskStore) GetResult(ctx context.Context, taskID string) (*model.TaskResult, bool, error) {
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

	var result model.TaskResult
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
// 双写模式：同时写入 Hash（精确查询）和 ZSET（时间扫描）。
func (s *RedisTaskStore) SetRunning(ctx context.Context, taskID string, agentID string) error {
	client, err := s.getClient()
	if err != nil {
		return err
	}

	// 获取 started_at_millis 作为 ZSET score
	var startedAtMillis int64
	info, err := s.GetTaskInfo(ctx, taskID)
	if err == nil && info != nil {
		startedAtMillis = info.StartedAtMillis
	}
	if startedAtMillis == 0 {
		startedAtMillis = time.Now().UnixMilli()
	}

	pipe := client.TxPipeline()
	pipe.HSet(ctx, s.runningKey(), taskID, agentID)
	pipe.ZAdd(ctx, s.runningByStartedAtKey(), redis.Z{
		Score:  float64(startedAtMillis),
		Member: taskID,
	})
	_, err = pipe.Exec(ctx)
	return err
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

// ListRunningTaskInfos implements TaskStore.
// 优先从 ZSET 时间索引读取（支持按 started_at 排序），回退到 Hash。
func (s *RedisTaskStore) ListRunningTaskInfos(ctx context.Context) ([]*TaskInfo, error) {
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}

	// 优先从 ZSET 读取（按 started_at 正序，最老的在前）
	zsetTaskIDs, err := client.ZRange(ctx, s.runningByStartedAtKey(), 0, -1).Result()
	if err != nil && err != redis.Nil {
		return nil, fmt.Errorf("list running tasks from zset: %w", err)
	}

	// 同时读取 Hash 用于补全 agentID
	runningEntries, err := client.HGetAll(ctx, s.runningKey()).Result()
	if err != nil {
		return nil, fmt.Errorf("list running tasks: %w", err)
	}

	// 合并两个来源的 taskID（ZSET 优先，Hash 补充）
	taskIDSet := make(map[string]struct{}, len(zsetTaskIDs)+len(runningEntries))
	for _, id := range zsetTaskIDs {
		taskIDSet[id] = struct{}{}
	}
	for id := range runningEntries {
		taskIDSet[id] = struct{}{}
	}
	if len(taskIDSet) == 0 {
		return nil, nil
	}

	const batchSize = 200

	taskIDs := make([]string, 0, len(taskIDSet))
	for id := range taskIDSet {
		taskIDs = append(taskIDs, id)
	}

	result := make([]*TaskInfo, 0, len(taskIDs))
	for start := 0; start < len(taskIDs); start += batchSize {
		end := start + batchSize
		if end > len(taskIDs) {
			end = len(taskIDs)
		}

		batch, staleTaskIDs := s.fetchRunningTaskBatch(ctx, client, taskIDs[start:end], runningEntries)
		result = append(result, batch...)
		s.cleanupStaleRunningEntries(ctx, client, staleTaskIDs)
	}

	return result, nil
}

func (s *RedisTaskStore) fetchRunningTaskBatch(ctx context.Context, client redis.UniversalClient, taskIDs []string, runningEntries map[string]string) ([]*TaskInfo, []string) {
	if len(taskIDs) == 0 {
		return nil, nil
	}

	detailKeys := make([]string, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		detailKeys = append(detailKeys, s.detailKey(taskID))
	}

	results, err := client.MGet(ctx, detailKeys...).Result()
	if err != nil {
		s.logger.Warn("Failed to MGET running task details", zap.Int("key_count", len(detailKeys)), zap.Error(err))
		return nil, nil
	}

	infos := make([]*TaskInfo, 0, len(results))
	staleTaskIDs := make([]string, 0)
	for i, data := range results {
		taskID := taskIDs[i]
		if data == nil {
			staleTaskIDs = append(staleTaskIDs, taskID)
			continue
		}

		dataStr, ok := data.(string)
		if !ok {
			staleTaskIDs = append(staleTaskIDs, taskID)
			continue
		}

		var info TaskInfo
		if err := json.Unmarshal([]byte(dataStr), &info); err != nil {
			s.logger.Warn("Failed to unmarshal running task info", zap.String("task_id", taskID), zap.Error(err))
			staleTaskIDs = append(staleTaskIDs, taskID)
			continue
		}
		if info.Task == nil || info.Task.ID == "" || info.Status != model.TaskStatusRunning {
			staleTaskIDs = append(staleTaskIDs, taskID)
			continue
		}
		if info.AgentID == "" {
			info.AgentID = runningEntries[taskID]
		}
		infos = append(infos, &info)
	}

	return infos, staleTaskIDs
}

func (s *RedisTaskStore) cleanupStaleRunningEntries(ctx context.Context, client redis.UniversalClient, taskIDs []string) {
	if len(taskIDs) == 0 {
		return
	}

	pipe := client.TxPipeline()
	pipe.HDel(ctx, s.runningKey(), taskIDs...)
	pipe.ZRem(ctx, s.runningByStartedAtKey(), stringSliceToAny(taskIDs)...)
	if _, err := pipe.Exec(ctx); err != nil {
		s.logger.Debug("Failed to cleanup stale running task markers", zap.Int("task_count", len(taskIDs)), zap.Error(err))
		return
	}

	s.logger.Debug("Cleaned stale running task markers", zap.Int("task_count", len(taskIDs)))
}

// ClearRunning implements TaskStore.
// 双删模式：同时从 Hash 和 ZSET 中移除。
func (s *RedisTaskStore) ClearRunning(ctx context.Context, taskID string) error {
	client, err := s.getClient()
	if err != nil {
		return err
	}

	pipe := client.TxPipeline()
	pipe.HDel(ctx, s.runningKey(), taskID)
	pipe.ZRem(ctx, s.runningByStartedAtKey(), taskID)
	_, err = pipe.Exec(ctx)
	return err
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

	// 异步执行索引回填，不阻塞启动
	client := s.client
	go func() {
		backfillCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := s.backfillTaskIndexes(backfillCtx, client); err != nil {
			s.logger.Error("Failed to backfill redis task list indexes", zap.Error(err))
		}
	}()

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
