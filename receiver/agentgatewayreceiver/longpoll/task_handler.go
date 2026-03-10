// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package longpoll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

// Redis key patterns (must match taskmanager/store/redis.go)
const (
	keyPendingGlobal  = "%s:pending:global"        // List: global pending tasks
	keyPendingAgent   = "%s:pending:%s"            // List: agent-specific pending tasks
	keyTaskDetail     = "%s:detail:%s"             // String: task details JSON
	keyCancelled      = "%s:cancelled"             // Set: cancelled task IDs
	keyEventSubmitted = "%s:events:task:submitted" // Pub/Sub channel
)

// TaskPollHandler implements LongPollHandler for task polling.
// It integrates with Redis for task storage and Pub/Sub for notifications.
type TaskPollHandler struct {
	logger      *zap.Logger
	redisClient redis.UniversalClient

	keyPrefix string

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
		// Claim-on-dispatch: remove from pending queues and mark as RUNNING
		// before returning to the agent. This prevents other poll requests
		// from seeing the same tasks, eliminating duplicate dispatch.
		h.claimDispatchedTasks(ctx, req.AgentID, tasks)

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
func (h *TaskPollHandler) getPendingTasks(ctx context.Context, agentID string) ([]*model.Task, error) {
	agentQueueKey := fmt.Sprintf(keyPendingAgent, h.keyPrefix, agentID)
	globalQueueKey := fmt.Sprintf(keyPendingGlobal, h.keyPrefix)

	seen := make(map[string]struct{}, 64)
	var tasks []*model.Task

	// Agent queue
	agentTasks, err := h.redisClient.LRange(ctx, agentQueueKey, 0, -1).Result()
	if err != nil && err != redis.Nil {
		return nil, err
	}
	for _, data := range agentTasks {
		task, err := h.parseAndValidateTask(ctx, data, agentQueueKey)
		if err != nil || task == nil {
			continue
		}
		if _, ok := seen[task.ID]; ok {
			continue
		}
		seen[task.ID] = struct{}{}
		tasks = append(tasks, task)
	}

	// Global queue
	globalTasks, err := h.redisClient.LRange(ctx, globalQueueKey, 0, -1).Result()
	if err != nil && err != redis.Nil {
		return nil, err
	}
	for _, data := range globalTasks {
		task, err := h.parseAndValidateTask(ctx, data, globalQueueKey)
		if err != nil || task == nil {
			continue
		}
		if _, ok := seen[task.ID]; ok {
			continue
		}
		seen[task.ID] = struct{}{}
		tasks = append(tasks, task)
	}

	return tasks, nil
}

type taskDetailEnvelope struct {
	Task   *model.Task      `json:"task"`
	Status model.TaskStatus `json:"status"`
}

func (h *TaskPollHandler) parseTaskDetail(data []byte) (*model.Task, model.TaskStatus, error) {
	var env taskDetailEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, model.TaskStatusUnspecified, err
	}

	return env.Task, env.Status, nil
}

func isDispatchableStatus(status model.TaskStatus) bool {
	// Only PENDING tasks are dispatchable.
	// RUNNING tasks must NOT be re-dispatched to avoid duplicate execution.
	return status == model.TaskStatusPending
}

// parseAndValidateTask parses and validates a task from Redis.
// Returns nil if the task should be skipped (cancelled, already completed, detail not found, etc.)
// The queueKey parameter is used to remove orphan tasks from the pending queue.
func (h *TaskPollHandler) parseAndValidateTask(ctx context.Context, data string, queueKey string) (*model.Task, error) {
	data = strings.TrimSpace(data)
	if data == "" {
		return nil, nil
	}

	// Queue item can be either:
	// - JSON (legacy format, contains full task)
	// - task_id string (new format)
	taskID := ""
	var taskFromQueue *model.Task
	if strings.HasPrefix(data, "{") {
		var t model.Task
		if err := json.Unmarshal([]byte(data), &t); err == nil && t.ID != "" {
			taskID = t.ID
			taskFromQueue = &t
		}
	}
	if taskID == "" {
		taskID = data
	}
	if taskID == "" {
		return nil, nil
	}

	// Cancelled check
	cancelledKey := fmt.Sprintf(keyCancelled, h.keyPrefix)
	cancelled, err := h.redisClient.SIsMember(ctx, cancelledKey, taskID).Result()
	if err == nil && cancelled {
		return nil, nil
	}
	if err != nil && err != redis.Nil {
		h.logger.Debug("Cancelled check failed", zap.String("task_id", taskID), zap.String("key", cancelledKey), zap.Error(err))
	}

	// Detail check
	detailKey := fmt.Sprintf(keyTaskDetail, h.keyPrefix, taskID)
	detailData, err := h.redisClient.Get(ctx, detailKey).Result()
	if err == redis.Nil {
		h.logger.Warn("Orphan task detected: detail not found, removing from pending queue",
			zap.String("task_id", taskID),
			zap.String("queue_key", queueKey),
		)
		if queueKey != "" {
			_ = h.redisClient.LRem(ctx, queueKey, 0, data).Err()
		}
		return nil, nil
	}
	if err != nil {
		h.logger.Warn("Failed to get task detail",
			zap.String("task_id", taskID),
			zap.String("detail_key", detailKey),
			zap.Error(err),
		)
		return nil, nil
	}

	taskFromDetail, status, err := h.parseTaskDetail([]byte(detailData))
	if err == nil {
		if !isDispatchableStatus(status) {
			return nil, nil
		}
	}

	if taskFromDetail != nil {
		return taskFromDetail, nil
	}
	return taskFromQueue, nil
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

		// Wait for subscription confirmation BEFORE starting message handler.
		msg, err := h.pubsub.Receive(context.Background())
		if err != nil {
			h.logger.Error("Failed to confirm Pub/Sub subscription",
				zap.String("channel", channel),
				zap.Error(err),
			)
			return
		}

		sub, ok := msg.(*redis.Subscription)
		if !ok || sub.Kind != "subscribe" {
			h.logger.Warn("Unexpected message type during subscription",
				zap.Any("message", msg),
			)
		}

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
func (h *TaskPollHandler) getTaskDetails(ctx context.Context, taskID string) (*model.Task, string, error) {
	detailKey := fmt.Sprintf(keyTaskDetail, h.keyPrefix, taskID)

	data, err := h.redisClient.Get(ctx, detailKey).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, "", nil
		}
		return nil, "", err
	}

	var env struct {
		Task    *model.Task `json:"task"`
		AgentID string      `json:"agent_id"`
	}
	if err := json.Unmarshal([]byte(data), &env); err != nil {
		return nil, "", err
	}

	return env.Task, env.AgentID, nil
}

// notifyWaiter notifies a specific waiter.
func (h *TaskPollHandler) notifyWaiter(agentID string, task *model.Task) {
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
			Response:   NewTaskResponse(true, []*model.Task{task}, "new task available"),
		}

		select {
		case waiter.resultChan <- result:
			h.logger.Debug("Successfully notified waiter of new task",
				zap.String("agent_id", agentID),
				zap.String("task_id", task.ID),
			)
		default:
			h.logger.Warn("Failed to notify waiter (channel full or closed)",
				zap.String("agent_id", agentID),
				zap.String("task_id", task.ID),
			)
		}
	} else {
		h.logger.Warn("No waiter found for agent",
			zap.String("target_agent_id", agentID),
			zap.Strings("registered_waiters", registeredAgents),
			zap.String("task_id", task.ID),
		)
	}
}

// notifyAllWaiters notifies all waiters (for global tasks).
func (h *TaskPollHandler) notifyAllWaiters(task *model.Task) {
	h.waiters.Range(func(key, value interface{}) bool {
		waiter := value.(*TaskWaiter)

		result := &HandlerResult{
			HasChanges: true,
			Response:   NewTaskResponse(true, []*model.Task{task}, "new global task available"),
		}

		select {
		case waiter.resultChan <- result:
			h.logger.Debug("Notified waiter of new global task",
				zap.String("agent_id", waiter.agentID),
				zap.String("task_id", task.ID),
			)
		default:
		}

		return true
	})
}

// claimDispatchedTasks removes dispatched tasks from pending queues and
// atomically marks them as RUNNING in the detail record ("claim-on-dispatch").
// This ensures that once a task is sent to an agent, no other poll request
// can pick it up. If the agent crashes, the StaleTaskReaper will detect
// the stuck RUNNING task and mark it as TIMEOUT.
func (h *TaskPollHandler) claimDispatchedTasks(ctx context.Context, agentID string, tasks []*model.Task) {
	agentQueueKey := fmt.Sprintf(keyPendingAgent, h.keyPrefix, agentID)
	globalQueueKey := fmt.Sprintf(keyPendingGlobal, h.keyPrefix)

	for _, task := range tasks {
		// Remove from both queues (best effort, ignore errors)
		_ = h.redisClient.LRem(ctx, agentQueueKey, 0, task.ID).Err()
		_ = h.redisClient.LRem(ctx, globalQueueKey, 0, task.ID).Err()

		// Atomically mark as RUNNING in the detail record
		h.markTaskDispatched(ctx, task.ID, agentID)

		h.logger.Debug("Claimed dispatched task from pending queue",
			zap.String("task_id", task.ID),
			zap.String("agent_id", agentID),
		)
	}
}

// markTaskDispatched atomically marks a task as RUNNING (dispatched) in the detail record.
// Only transitions from PENDING → RUNNING; if the task is already in another state,
// the update is silently skipped.
var markDispatchedScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if not current then return 0 end

local info = cjson.decode(current)
local cur = tonumber(info.status) or 0

-- Only mark RUNNING if currently PENDING (status=1)
if cur ~= 1 then return 0 end

info.status = 2
info.last_updated_at_millis = tonumber(ARGV[1])
info.version = (tonumber(info.version) or 0) + 1

if ARGV[2] and ARGV[2] ~= '' then
    if not info.agent_id or info.agent_id == '' then
        info.agent_id = ARGV[2]
    end
end

local started = tonumber(info.started_at_millis) or 0
if started == 0 then
    info.started_at_millis = tonumber(ARGV[1])
end

local ttl = redis.call('TTL', KEYS[1])
if ttl > 0 then
    redis.call('SET', KEYS[1], cjson.encode(info), 'EX', ttl)
else
    redis.call('SET', KEYS[1], cjson.encode(info))
end
return 1
`)

func (h *TaskPollHandler) markTaskDispatched(ctx context.Context, taskID string, agentID string) {
	detailKey := fmt.Sprintf(keyTaskDetail, h.keyPrefix, taskID)
	nowMillis := time.Now().UnixMilli()

	_, err := markDispatchedScript.Run(ctx, h.redisClient, []string{detailKey}, nowMillis, agentID).Int()
	if err != nil {
		h.logger.Warn("Failed to mark task as dispatched",
			zap.String("task_id", taskID),
			zap.String("agent_id", agentID),
			zap.Error(err),
		)
	}
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
