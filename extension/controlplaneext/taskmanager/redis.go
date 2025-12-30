// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// Redis key patterns
const (
	keyPendingGlobal   = "%s:pending:global"        // List: global pending tasks
	keyPendingAgent    = "%s:pending:%s"            // List: agent-specific pending tasks
	keyTaskDetail      = "%s:detail:%s"             // Hash: task details
	keyCancelled       = "%s:cancelled"             // Set: cancelled task IDs
	keyResult          = "%s:result:%s"             // Hash: task results
	keyRunning         = "%s:running"               // Hash: taskID -> agentID
	keyEventSubmitted  = "%s:events:task:submitted" // Pub/Sub channel
	keyEventCompleted  = "%s:events:task:completed" // Pub/Sub channel
	keyEventCancelled  = "%s:events:task:cancelled" // Pub/Sub channel
)

// RedisTaskManager implements TaskManager using Redis as backend.
type RedisTaskManager struct {
	logger    *zap.Logger
	config    Config
	keyPrefix string

	mu      sync.RWMutex
	client  redis.UniversalClient
	started bool
}

// NewRedisTaskManager creates a new Redis-based task manager.
func NewRedisTaskManager(logger *zap.Logger, config Config, client redis.UniversalClient) (*RedisTaskManager, error) {
	keyPrefix := config.KeyPrefix
	if keyPrefix == "" {
		keyPrefix = "otel:tasks"
	}

	return &RedisTaskManager{
		logger:    logger,
		config:    config,
		keyPrefix: keyPrefix,
		client:    client,
	}, nil
}

// Ensure RedisTaskManager implements TaskManager.
var _ TaskManager = (*RedisTaskManager)(nil)

// Start initializes the Redis client.
func (m *RedisTaskManager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.started {
		return nil
	}

	m.logger.Info("Starting Redis task manager",
		zap.String("key_prefix", m.keyPrefix),
	)

	if m.client == nil {
		return errors.New("redis client not provided")
	}

	// Test connection
	if err := m.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("failed to connect to Redis: %w", err)
	}

	m.started = true

	return nil
}

// SubmitTask submits a task to the global queue.
func (m *RedisTaskManager) SubmitTask(ctx context.Context, task *controlplanev1.Task) error {
	return m.submitTaskToQueue(ctx, "", task)
}

// SubmitTaskForAgent submits a task for a specific agent.
func (m *RedisTaskManager) SubmitTaskForAgent(ctx context.Context, agentID string, task *controlplanev1.Task) error {
	return m.submitTaskToQueue(ctx, agentID, task)
}

func (m *RedisTaskManager) submitTaskToQueue(ctx context.Context, agentID string, task *controlplanev1.Task) error {
	if task == nil {
		return errors.New("task cannot be nil")
	}
	if task.TaskID == "" {
		return errors.New("task_id is required")
	}
	if task.TaskType == "" {
		return errors.New("task_type is required")
	}

	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()

	if client == nil {
		return errors.New("redis client not initialized")
	}

	// Serialize task
	taskData, err := json.Marshal(task)
	if err != nil {
		return err
	}

	// Create task info
	info := &TaskInfo{
		Task:      task,
		Status:    controlplanev1.TaskStatusPending,
		AgentID:   agentID,
		CreatedAt: time.Now().UnixNano(),
	}
	infoData, err := json.Marshal(info)
	if err != nil {
		return err
	}

	// Determine queue key
	var queueKey string
	if agentID == "" {
		queueKey = fmt.Sprintf(keyPendingGlobal, m.keyPrefix)
	} else {
		queueKey = fmt.Sprintf(keyPendingAgent, m.keyPrefix, agentID)
	}

	detailKey := fmt.Sprintf(keyTaskDetail, m.keyPrefix, task.TaskID)

	// Execute in transaction
	pipe := client.TxPipeline()

	// Store task details
	pipe.Set(ctx, detailKey, infoData, m.config.ResultTTL)

	// Add to queue
	pipe.LPush(ctx, queueKey, taskData)

	// Publish event
	eventKey := fmt.Sprintf(keyEventSubmitted, m.keyPrefix)
	pipe.Publish(ctx, eventKey, task.TaskID)

	_, err = pipe.Exec(ctx)
	if err != nil {
		return err
	}

	m.logger.Debug("Task submitted to Redis",
		zap.String("task_id", task.TaskID),
		zap.String("agent_id", agentID),
	)

	return nil
}

// FetchTask fetches the next task for an agent (blocking with timeout).
func (m *RedisTaskManager) FetchTask(ctx context.Context, agentID string, timeout time.Duration) (*controlplanev1.Task, error) {
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()

	if client == nil {
		return nil, errors.New("redis client not initialized")
	}

	// Try agent-specific queue first, then global queue
	agentQueueKey := fmt.Sprintf(keyPendingAgent, m.keyPrefix, agentID)
	globalQueueKey := fmt.Sprintf(keyPendingGlobal, m.keyPrefix)
	cancelledKey := fmt.Sprintf(keyCancelled, m.keyPrefix)

	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		// Try agent queue (non-blocking)
		result, err := client.RPop(ctx, agentQueueKey).Result()
		if err == nil {
			task, err := m.parseAndValidateTask(ctx, result, cancelledKey)
			if err == nil && task != nil {
				return task, nil
			}
			continue
		} else if err != redis.Nil {
			return nil, err
		}

		// Try global queue with blocking
		remainingTimeout := time.Until(deadline)
		if remainingTimeout <= 0 {
			break
		}
		if remainingTimeout > time.Second {
			remainingTimeout = time.Second // Poll every second
		}

		results, err := client.BRPop(ctx, remainingTimeout, globalQueueKey).Result()
		if err == redis.Nil {
			continue
		}
		if err != nil {
			return nil, err
		}

		if len(results) >= 2 {
			task, err := m.parseAndValidateTask(ctx, results[1], cancelledKey)
			if err == nil && task != nil {
				return task, nil
			}
		}
	}

	return nil, nil // Timeout
}

// parseAndValidateTask parses and validates a task from Redis.
func (m *RedisTaskManager) parseAndValidateTask(ctx context.Context, data string, cancelledKey string) (*controlplanev1.Task, error) {
	var task controlplanev1.Task
	if err := json.Unmarshal([]byte(data), &task); err != nil {
		return nil, err
	}

	// Check if cancelled
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()

	if client != nil {
		cancelled, err := client.SIsMember(ctx, cancelledKey, task.TaskID).Result()
		if err == nil && cancelled {
			return nil, nil // Skip cancelled task
		}
	}

	return &task, nil
}

// GetPendingTasks returns all pending tasks for an agent.
func (m *RedisTaskManager) GetPendingTasks(ctx context.Context, agentID string) ([]*controlplanev1.Task, error) {
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()

	if client == nil {
		return nil, errors.New("redis client not initialized")
	}

	agentQueueKey := fmt.Sprintf(keyPendingAgent, m.keyPrefix, agentID)
	globalQueueKey := fmt.Sprintf(keyPendingGlobal, m.keyPrefix)
	cancelledKey := fmt.Sprintf(keyCancelled, m.keyPrefix)

	var tasks []*controlplanev1.Task

	// Get from agent queue
	agentTasks, err := client.LRange(ctx, agentQueueKey, 0, -1).Result()
	if err != nil && err != redis.Nil {
		return nil, err
	}

	for _, data := range agentTasks {
		task, err := m.parseAndValidateTask(ctx, data, cancelledKey)
		if err == nil && task != nil {
			tasks = append(tasks, task)
		}
	}

	// Get from global queue
	globalTasks, err := client.LRange(ctx, globalQueueKey, 0, -1).Result()
	if err != nil && err != redis.Nil {
		return nil, err
	}

	for _, data := range globalTasks {
		task, err := m.parseAndValidateTask(ctx, data, cancelledKey)
		if err == nil && task != nil {
			tasks = append(tasks, task)
		}
	}

	return tasks, nil
}

// GetGlobalPendingTasks returns all pending tasks in the global queue.
func (m *RedisTaskManager) GetGlobalPendingTasks(ctx context.Context) ([]*controlplanev1.Task, error) {
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()

	if client == nil {
		return nil, errors.New("redis client not initialized")
	}

	globalQueueKey := fmt.Sprintf(keyPendingGlobal, m.keyPrefix)
	cancelledKey := fmt.Sprintf(keyCancelled, m.keyPrefix)

	results, err := client.LRange(ctx, globalQueueKey, 0, -1).Result()
	if err != nil {
		return nil, err
	}

	var tasks []*controlplanev1.Task
	for _, data := range results {
		task, err := m.parseAndValidateTask(ctx, data, cancelledKey)
		if err == nil && task != nil {
			tasks = append(tasks, task)
		}
	}

	return tasks, nil
}

// CancelTask cancels a task by ID.
func (m *RedisTaskManager) CancelTask(ctx context.Context, taskID string) error {
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()

	if client == nil {
		return errors.New("redis client not initialized")
	}

	cancelledKey := fmt.Sprintf(keyCancelled, m.keyPrefix)
	detailKey := fmt.Sprintf(keyTaskDetail, m.keyPrefix, taskID)
	eventKey := fmt.Sprintf(keyEventCancelled, m.keyPrefix)

	pipe := client.TxPipeline()

	// Add to cancelled set
	pipe.SAdd(ctx, cancelledKey, taskID)

	// Update task detail status
	pipe.HSet(ctx, detailKey, "status", int(controlplanev1.TaskStatusCancelled))

	// Publish cancel event
	pipe.Publish(ctx, eventKey, taskID)

	_, err := pipe.Exec(ctx)
	if err != nil {
		return err
	}

	m.logger.Info("Task cancelled", zap.String("task_id", taskID))
	return nil
}

// IsTaskCancelled checks if a task has been cancelled.
func (m *RedisTaskManager) IsTaskCancelled(ctx context.Context, taskID string) (bool, error) {
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()

	if client == nil {
		return false, errors.New("redis client not initialized")
	}

	cancelledKey := fmt.Sprintf(keyCancelled, m.keyPrefix)
	return client.SIsMember(ctx, cancelledKey, taskID).Result()
}

// ReportTaskResult reports the result of a task execution.
func (m *RedisTaskManager) ReportTaskResult(ctx context.Context, result *controlplanev1.TaskResult) error {
	if result == nil {
		return errors.New("result cannot be nil")
	}

	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()

	if client == nil {
		return errors.New("redis client not initialized")
	}

	resultData, err := json.Marshal(result)
	if err != nil {
		return err
	}

	resultKey := fmt.Sprintf(keyResult, m.keyPrefix, result.TaskID)
	detailKey := fmt.Sprintf(keyTaskDetail, m.keyPrefix, result.TaskID)
	runningKey := fmt.Sprintf(keyRunning, m.keyPrefix)
	eventKey := fmt.Sprintf(keyEventCompleted, m.keyPrefix)

	pipe := client.TxPipeline()

	// Store result
	pipe.Set(ctx, resultKey, resultData, m.config.ResultTTL)

	// Update task detail
	pipe.HSet(ctx, detailKey, "status", int(result.Status), "result", resultData)

	// Remove from running
	pipe.HDel(ctx, runningKey, result.TaskID)

	// Publish completion event
	pipe.Publish(ctx, eventKey, result.TaskID)

	_, err = pipe.Exec(ctx)
	if err != nil {
		return err
	}

	m.logger.Debug("Task result reported",
		zap.String("task_id", result.TaskID),
		zap.String("status", result.Status.String()),
	)

	return nil
}

// GetTaskResult retrieves the result of a task.
func (m *RedisTaskManager) GetTaskResult(ctx context.Context, taskID string) (*controlplanev1.TaskResult, bool, error) {
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()

	if client == nil {
		return nil, false, errors.New("redis client not initialized")
	}

	resultKey := fmt.Sprintf(keyResult, m.keyPrefix, taskID)

	data, err := client.Get(ctx, resultKey).Result()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	var result controlplanev1.TaskResult
	if err := json.Unmarshal([]byte(data), &result); err != nil {
		return nil, false, err
	}

	return &result, true, nil
}

// GetTaskStatus retrieves the status of a task.
func (m *RedisTaskManager) GetTaskStatus(ctx context.Context, taskID string) (*TaskInfo, error) {
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()

	if client == nil {
		return nil, errors.New("redis client not initialized")
	}

	detailKey := fmt.Sprintf(keyTaskDetail, m.keyPrefix, taskID)

	data, err := client.Get(ctx, detailKey).Result()
	if err == redis.Nil {
		return nil, errors.New("task not found: " + taskID)
	}
	if err != nil {
		return nil, err
	}

	var info TaskInfo
	if err := json.Unmarshal([]byte(data), &info); err != nil {
		return nil, err
	}

	return &info, nil
}

// SetTaskRunning marks a task as running by an agent.
func (m *RedisTaskManager) SetTaskRunning(ctx context.Context, taskID string, agentID string) error {
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()

	if client == nil {
		return errors.New("redis client not initialized")
	}

	runningKey := fmt.Sprintf(keyRunning, m.keyPrefix)
	detailKey := fmt.Sprintf(keyTaskDetail, m.keyPrefix, taskID)

	pipe := client.TxPipeline()

	// Set running mapping
	pipe.HSet(ctx, runningKey, taskID, agentID)

	// Update task detail
	pipe.HSet(ctx, detailKey,
		"status", int(controlplanev1.TaskStatusRunning),
		"agent_id", agentID,
		"started_at", time.Now().UnixNano(),
	)

	_, err := pipe.Exec(ctx)
	return err
}

// Close releases resources.
func (m *RedisTaskManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.started {
		return nil
	}

	// Note: We don't close the Redis client here because it's managed by the storage extension
	m.started = false

	return nil
}
