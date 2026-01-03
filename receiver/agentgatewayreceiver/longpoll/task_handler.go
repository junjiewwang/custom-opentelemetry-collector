// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package longpoll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// Redis key patterns (must match taskmanager/redis.go)
const (
	keyPendingGlobal  = "%s:pending:global"        // List: global pending tasks
	keyPendingAgent   = "%s:pending:%s"            // List: agent-specific pending tasks
	keyTaskDetail     = "%s:detail:%s"             // Hash: task details
	keyCancelled      = "%s:cancelled"             // Set: cancelled task IDs
	keyEventSubmitted = "%s:events:task:submitted" // Pub/Sub channel
)

// TaskPollHandler implements LongPollHandler for task polling.
// It integrates with Redis for task storage and Pub/Sub for notifications.
type TaskPollHandler struct {
	logger      *zap.Logger
	redisClient redis.UniversalClient
	keyPrefix   string

	// Waiters management (per agent)
	waiters sync.Map // agentID -> *TaskWaiter

	// Pub/Sub management
	pubsub     *redis.PubSub
	pubsubOnce sync.Once
	pubsubDone chan struct{}

	// State
	running atomic.Bool
}

// TaskWaiter represents a waiting task poll request.
type TaskWaiter struct {
	agentID    string
	resultChan chan *HandlerResult
	ctx        context.Context
	cancel     context.CancelFunc
}

// NewTaskPollHandler creates a new TaskPollHandler.
func NewTaskPollHandler(logger *zap.Logger, redisClient redis.UniversalClient, keyPrefix string) *TaskPollHandler {
	if keyPrefix == "" {
		keyPrefix = "otel:tasks"
	}

	return &TaskPollHandler{
		logger:      logger,
		redisClient: redisClient,
		keyPrefix:   keyPrefix,
		pubsubDone:  make(chan struct{}),
	}
}

// Ensure TaskPollHandler implements LongPollHandler.
var _ LongPollHandler = (*TaskPollHandler)(nil)

// GetType returns the handler type.
func (h *TaskPollHandler) GetType() LongPollType {
	return LongPollTypeTask
}

// Start initializes the handler.
func (h *TaskPollHandler) Start(ctx context.Context) error {
	if h.running.Swap(true) {
		return nil
	}

	// Start Redis Pub/Sub subscriber
	h.startPubSub()

	h.logger.Info("TaskPollHandler started", zap.String("key_prefix", h.keyPrefix))
	return nil
}

// Stop stops the handler.
func (h *TaskPollHandler) Stop() error {
	if !h.running.Swap(false) {
		return nil
	}

	// Cancel all waiters
	h.waiters.Range(func(key, value interface{}) bool {
		waiter := value.(*TaskWaiter)
		if waiter.cancel != nil {
			waiter.cancel()
		}
		return true
	})

	// Close Pub/Sub
	if h.pubsub != nil {
		_ = h.pubsub.Close()
		<-h.pubsubDone
	}

	h.logger.Info("TaskPollHandler stopped")
	return nil
}

// ShouldContinue returns whether the handler should continue polling.
func (h *TaskPollHandler) ShouldContinue() bool {
	return h.running.Load()
}

// CheckImmediate checks if there are pending tasks immediately.
func (h *TaskPollHandler) CheckImmediate(ctx context.Context, req *PollRequest) (bool, *HandlerResult, error) {
	if h.redisClient == nil {
		return false, nil, errors.New("redis client not initialized")
	}

	agentQueueKey := fmt.Sprintf(keyPendingAgent, h.keyPrefix, req.AgentID)
	globalQueueKey := fmt.Sprintf(keyPendingGlobal, h.keyPrefix)

	h.logger.Debug("CheckImmediate: checking pending tasks",
		zap.String("agent_id", req.AgentID),
		zap.String("agent_queue_key", agentQueueKey),
		zap.String("global_queue_key", globalQueueKey),
	)

	// Get pending tasks for the agent
	tasks, err := h.getPendingTasks(ctx, req.AgentID)
	if err != nil {
		return false, nil, err
	}

	h.logger.Debug("CheckImmediate: pending tasks result",
		zap.String("agent_id", req.AgentID),
		zap.Int("task_count", len(tasks)),
	)

	if len(tasks) > 0 {
		result := &HandlerResult{
			HasChanges: true,
			Response:   NewTaskResponse(true, tasks, fmt.Sprintf("%d tasks available", len(tasks))),
		}
		return true, result, nil
	}

	return false, nil, nil
}

// Poll executes the long poll wait for new tasks.
func (h *TaskPollHandler) Poll(ctx context.Context, req *PollRequest) (*HandlerResult, error) {
	if h.redisClient == nil {
		return nil, errors.New("redis client not initialized")
	}

	// Step 1: Check for immediate tasks
	hasChanges, result, err := h.CheckImmediate(ctx, req)
	if err != nil {
		return nil, err
	}
	if hasChanges {
		return result, nil
	}

	// Step 2: No tasks, register waiter and wait for notification
	waiterCtx, cancel := context.WithCancel(ctx)
	waiter := &TaskWaiter{
		agentID:    req.AgentID,
		resultChan: make(chan *HandlerResult, 1),
		ctx:        waiterCtx,
		cancel:     cancel,
	}

	// Register waiter
	h.waiters.Store(req.AgentID, waiter)
	h.logger.Debug("Registered waiter for long poll",
		zap.String("agent_id", req.AgentID),
		zap.Int("total_waiters", h.GetWaiterCount()),
	)
	defer func() {
		h.waiters.Delete(req.AgentID)
		h.logger.Debug("Unregistered waiter",
			zap.String("agent_id", req.AgentID),
		)
		cancel()
	}()

	// Ensure Pub/Sub is active
	h.startPubSub()

	// Step 3: Double-check for tasks after registering waiter
	// This prevents race condition where task is submitted between
	// Step 1 check and waiter registration
	hasChanges, result, err = h.CheckImmediate(ctx, req)
	if err != nil {
		return nil, err
	}
	if hasChanges {
		h.logger.Debug("Found tasks in double-check after waiter registration",
			zap.String("agent_id", req.AgentID),
		)
		return result, nil
	}

	// Step 4: Wait for task notification or timeout
	select {
	case result := <-waiter.resultChan:
		return result, nil
	case <-ctx.Done():
		// Timeout - return no tasks
		return &HandlerResult{
			HasChanges: false,
			Response:   NoChangeResponse(LongPollTypeTask),
		}, nil
	}
}

// getPendingTasks gets pending tasks for an agent from Redis.
func (h *TaskPollHandler) getPendingTasks(ctx context.Context, agentID string) ([]*controlplanev1.Task, error) {
	agentQueueKey := fmt.Sprintf(keyPendingAgent, h.keyPrefix, agentID)
	globalQueueKey := fmt.Sprintf(keyPendingGlobal, h.keyPrefix)
	cancelledKey := fmt.Sprintf(keyCancelled, h.keyPrefix)

	var tasks []*controlplanev1.Task

	// Get from agent queue
	agentTasks, err := h.redisClient.LRange(ctx, agentQueueKey, 0, -1).Result()
	if err != nil && err != redis.Nil {
		return nil, err
	}

	for _, data := range agentTasks {
		task, err := h.parseAndValidateTask(ctx, data, cancelledKey)
		if err == nil && task != nil {
			tasks = append(tasks, task)
		}
	}

	// Get from global queue
	globalTasks, err := h.redisClient.LRange(ctx, globalQueueKey, 0, -1).Result()
	if err != nil && err != redis.Nil {
		return nil, err
	}

	for _, data := range globalTasks {
		task, err := h.parseAndValidateTask(ctx, data, cancelledKey)
		if err == nil && task != nil {
			tasks = append(tasks, task)
		}
	}

	return tasks, nil
}

// parseAndValidateTask parses and validates a task from Redis.
// Returns nil if the task should be skipped (cancelled, already completed, etc.)
func (h *TaskPollHandler) parseAndValidateTask(ctx context.Context, data string, cancelledKey string) (*controlplanev1.Task, error) {
	var task controlplanev1.Task
	if err := json.Unmarshal([]byte(data), &task); err != nil {
		return nil, err
	}

	// Check if cancelled (fast path using cancelled set)
	cancelled, err := h.redisClient.SIsMember(ctx, cancelledKey, task.TaskID).Result()
	if err == nil && cancelled {
		return nil, nil // Skip cancelled task
	}

	// Check task status from detail storage
	// Only dispatch tasks that are in dispatchable states (PENDING/RUNNING)
	detailKey := fmt.Sprintf(keyTaskDetail, h.keyPrefix, task.TaskID)
	detailData, err := h.redisClient.Get(ctx, detailKey).Result()
	if err == nil && detailData != "" {
		var taskInfo struct {
			Status controlplanev1.TaskStatus `json:"status"`
		}
		if err := json.Unmarshal([]byte(detailData), &taskInfo); err == nil {
			if !taskInfo.Status.IsDispatchable() {
				h.logger.Debug("Skipping non-dispatchable task",
					zap.String("task_id", task.TaskID),
					zap.String("status", taskInfo.Status.String()),
				)
				return nil, nil // Skip task with terminal status
			}
		}
	}

	return &task, nil
}

// startPubSub starts the Redis Pub/Sub subscriber.
func (h *TaskPollHandler) startPubSub() {
	h.pubsubOnce.Do(func() {
		channel := fmt.Sprintf(keyEventSubmitted, h.keyPrefix)
		h.logger.Info("Starting Redis Pub/Sub subscriber",
			zap.String("channel", channel),
			zap.String("key_prefix", h.keyPrefix),
		)

		h.pubsub = h.redisClient.Subscribe(context.Background(), channel)

		// Wait for subscription confirmation BEFORE starting message handler
		// This ensures we don't miss any messages
		msg, err := h.pubsub.Receive(context.Background())
		if err != nil {
			h.logger.Error("Failed to confirm Pub/Sub subscription",
				zap.String("channel", channel),
				zap.Error(err),
			)
			return
		}

		// Verify it's a subscription confirmation
		switch v := msg.(type) {
		case *redis.Subscription:
			h.logger.Info("Pub/Sub subscription confirmed",
				zap.String("channel", v.Channel),
				zap.String("kind", v.Kind),
				zap.Int("count", v.Count),
			)
		default:
			h.logger.Warn("Unexpected message type during subscription",
				zap.String("channel", channel),
				zap.Any("message", msg),
			)
		}

		// Now start the message handler goroutine
		go h.handlePubSubMessages()

		h.logger.Info("Started Redis Pub/Sub subscriber goroutine", zap.String("channel", channel))
	})
}

// handlePubSubMessages handles messages from Redis Pub/Sub.
func (h *TaskPollHandler) handlePubSubMessages() {
	defer close(h.pubsubDone)

	h.logger.Info("Pub/Sub message handler started, waiting for messages...")

	// Use ReceiveMessage instead of Channel() to avoid message loss
	// Channel() has an internal buffer that may drop messages
	for {
		if !h.running.Load() {
			h.logger.Info("Pub/Sub handler stopping")
			return
		}

		// ReceiveMessage blocks until a message is received or context is cancelled
		msg, err := h.pubsub.ReceiveMessage(context.Background())
		if err != nil {
			// Check if we're shutting down
			if !h.running.Load() {
				return
			}
			h.logger.Warn("Error receiving Pub/Sub message",
				zap.Error(err),
			)
			continue
		}

		h.logger.Info("Pub/Sub message received",
			zap.String("channel", msg.Channel),
			zap.String("payload", msg.Payload),
		)

		// msg.Payload is the taskID
		taskID := msg.Payload
		h.logger.Debug("Received task submitted event via Pub/Sub",
			zap.String("task_id", taskID),
			zap.Int("waiter_count", h.GetWaiterCount()),
		)

		// Get task details to determine target agent
		task, targetAgentID, err := h.getTaskDetails(context.Background(), taskID)
		if err != nil {
			h.logger.Warn("Failed to get task details",
				zap.String("task_id", taskID),
				zap.Error(err),
			)
			continue
		}

		if task == nil {
			h.logger.Warn("Task details not found",
				zap.String("task_id", taskID),
			)
			continue
		}

		h.logger.Debug("Task details retrieved for notification",
			zap.String("task_id", taskID),
			zap.String("target_agent_id", targetAgentID),
			zap.String("task_target_agent_id", task.TargetAgentID),
		)

		// Notify appropriate waiters
		if targetAgentID == "" {
			// Global task - notify all waiters
			h.logger.Debug("Notifying all waiters for global task",
				zap.String("task_id", taskID),
				zap.Int("waiter_count", h.GetWaiterCount()),
			)
			h.notifyAllWaiters(task)
		} else {
			// Agent-specific task
			h.logger.Debug("Notifying specific waiter for agent task",
				zap.String("task_id", taskID),
				zap.String("target_agent_id", targetAgentID),
			)
			h.notifyWaiter(targetAgentID, task)
		}
	}
}

// getTaskDetails gets task details from Redis.
func (h *TaskPollHandler) getTaskDetails(ctx context.Context, taskID string) (*controlplanev1.Task, string, error) {
	detailKey := fmt.Sprintf(keyTaskDetail, h.keyPrefix, taskID)

	data, err := h.redisClient.Get(ctx, detailKey).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, "", nil
		}
		return nil, "", err
	}

	// Parse task info (which contains the task and agent_id)
	var taskInfo struct {
		Task    *controlplanev1.Task `json:"task"`
		AgentID string               `json:"agent_id"`
	}
	if err := json.Unmarshal([]byte(data), &taskInfo); err != nil {
		return nil, "", err
	}

	return taskInfo.Task, taskInfo.AgentID, nil
}

// notifyWaiter notifies a specific waiter.
func (h *TaskPollHandler) notifyWaiter(agentID string, task *controlplanev1.Task) {
	// Log all registered waiters for debugging
	var registeredAgents []string
	h.waiters.Range(func(key, _ interface{}) bool {
		registeredAgents = append(registeredAgents, key.(string))
		return true
	})
	h.logger.Debug("Looking for waiter",
		zap.String("target_agent_id", agentID),
		zap.Strings("registered_waiters", registeredAgents),
	)

	if waiterVal, ok := h.waiters.Load(agentID); ok {
		waiter := waiterVal.(*TaskWaiter)

		result := &HandlerResult{
			HasChanges: true,
			Response:   NewTaskResponse(true, []*controlplanev1.Task{task}, "new task available"),
		}

		select {
		case waiter.resultChan <- result:
			h.logger.Debug("Successfully notified waiter of new task",
				zap.String("agent_id", agentID),
				zap.String("task_id", task.TaskID),
			)
		default:
			h.logger.Warn("Failed to notify waiter (channel full or closed)",
				zap.String("agent_id", agentID),
				zap.String("task_id", task.TaskID),
			)
		}
	} else {
		h.logger.Warn("No waiter found for agent",
			zap.String("target_agent_id", agentID),
			zap.Strings("registered_waiters", registeredAgents),
			zap.String("task_id", task.TaskID),
		)
	}
}

// notifyAllWaiters notifies all waiters (for global tasks).
func (h *TaskPollHandler) notifyAllWaiters(task *controlplanev1.Task) {
	h.waiters.Range(func(key, value interface{}) bool {
		waiter := value.(*TaskWaiter)

		result := &HandlerResult{
			HasChanges: true,
			Response:   NewTaskResponse(true, []*controlplanev1.Task{task}, "new global task available"),
		}

		select {
		case waiter.resultChan <- result:
			h.logger.Debug("Notified waiter of new global task",
				zap.String("agent_id", waiter.agentID),
				zap.String("task_id", task.TaskID),
			)
		default:
		}

		return true
	})
}

// GetWaiterCount returns the number of active waiters.
func (h *TaskPollHandler) GetWaiterCount() int {
	count := 0
	h.waiters.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}
