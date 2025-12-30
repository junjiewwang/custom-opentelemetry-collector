// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package taskmanager

import (
	"container/heap"
	"context"
	"errors"
	"sync"
	"time"

	"go.uber.org/zap"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// MemoryTaskManager implements TaskManager using in-memory storage.
type MemoryTaskManager struct {
	logger *zap.Logger
	config Config

	mu             sync.RWMutex
	globalQueue    *taskPriorityQueue
	agentQueues    map[string]*taskPriorityQueue
	taskDetails    map[string]*TaskInfo
	cancelledTasks map[string]bool
	results        map[string]*controlplanev1.TaskResult
	runningTasks   map[string]string // taskID -> agentID

	// Lifecycle
	started  bool
	stopChan chan struct{}
	wg       sync.WaitGroup
}

// NewMemoryTaskManager creates a new in-memory task manager.
func NewMemoryTaskManager(logger *zap.Logger, config Config) *MemoryTaskManager {
	tm := &MemoryTaskManager{
		logger:         logger,
		config:         config,
		globalQueue:    &taskPriorityQueue{},
		agentQueues:    make(map[string]*taskPriorityQueue),
		taskDetails:    make(map[string]*TaskInfo),
		cancelledTasks: make(map[string]bool),
		results:        make(map[string]*controlplanev1.TaskResult),
		runningTasks:   make(map[string]string),
		stopChan:       make(chan struct{}),
	}

	heap.Init(tm.globalQueue)
	return tm
}

// Ensure MemoryTaskManager implements TaskManager.
var _ TaskManager = (*MemoryTaskManager)(nil)

// SubmitTask submits a task to the global queue.
func (m *MemoryTaskManager) SubmitTask(ctx context.Context, task *controlplanev1.Task) error {
	return m.submitTaskToQueue(ctx, "", task)
}

// SubmitTaskForAgent submits a task for a specific agent.
func (m *MemoryTaskManager) SubmitTaskForAgent(ctx context.Context, agentID string, task *controlplanev1.Task) error {
	return m.submitTaskToQueue(ctx, agentID, task)
}

func (m *MemoryTaskManager) submitTaskToQueue(ctx context.Context, agentID string, task *controlplanev1.Task) error {
	if task == nil {
		return errors.New("task cannot be nil")
	}
	if task.TaskID == "" {
		return errors.New("task_id is required")
	}
	if task.TaskType == "" {
		return errors.New("task_type is required")
	}

	// Check if task is expired
	if task.ExpiresAtUnixNano > 0 && time.Now().UnixNano() > task.ExpiresAtUnixNano {
		return errors.New("task has expired")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check for duplicate
	if _, exists := m.taskDetails[task.TaskID]; exists {
		return errors.New("task already exists: " + task.TaskID)
	}

	// Store task details
	m.taskDetails[task.TaskID] = &TaskInfo{
		Task:      task,
		Status:    controlplanev1.TaskStatusPending,
		AgentID:   agentID,
		CreatedAt: time.Now().UnixNano(),
	}

	// Add to appropriate queue
	if agentID == "" {
		heap.Push(m.globalQueue, &taskItem{task: task})
	} else {
		queue, ok := m.agentQueues[agentID]
		if !ok {
			queue = &taskPriorityQueue{}
			heap.Init(queue)
			m.agentQueues[agentID] = queue
		}
		heap.Push(queue, &taskItem{task: task})
	}

	m.logger.Debug("Task submitted",
		zap.String("task_id", task.TaskID),
		zap.String("task_type", task.TaskType),
		zap.String("agent_id", agentID),
	)

	return nil
}

// FetchTask fetches the next task for an agent.
func (m *MemoryTaskManager) FetchTask(ctx context.Context, agentID string, timeout time.Duration) (*controlplanev1.Task, error) {
	deadline := time.Now().Add(timeout)

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		if time.Now().After(deadline) {
			return nil, nil // Timeout, no task available
		}

		m.mu.Lock()
		// Try agent-specific queue first
		if queue, ok := m.agentQueues[agentID]; ok && queue.Len() > 0 {
			item := heap.Pop(queue).(*taskItem)
			task := item.task

			// Skip cancelled tasks
			if m.cancelledTasks[task.TaskID] {
				m.mu.Unlock()
				continue
			}

			m.mu.Unlock()
			return task, nil
		}

		// Try global queue
		if m.globalQueue.Len() > 0 {
			item := heap.Pop(m.globalQueue).(*taskItem)
			task := item.task

			// Skip cancelled tasks
			if m.cancelledTasks[task.TaskID] {
				m.mu.Unlock()
				continue
			}

			m.mu.Unlock()
			return task, nil
		}
		m.mu.Unlock()

		// Wait a bit before retrying
		time.Sleep(100 * time.Millisecond)
	}
}

// GetPendingTasks returns all pending tasks for an agent.
func (m *MemoryTaskManager) GetPendingTasks(ctx context.Context, agentID string) ([]*controlplanev1.Task, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var tasks []*controlplanev1.Task

	// Get from agent-specific queue
	if queue, ok := m.agentQueues[agentID]; ok {
		for _, item := range *queue {
			if !m.cancelledTasks[item.task.TaskID] {
				tasks = append(tasks, item.task)
			}
		}
	}

	// Also include global queue tasks
	for _, item := range *m.globalQueue {
		if !m.cancelledTasks[item.task.TaskID] {
			tasks = append(tasks, item.task)
		}
	}

	return tasks, nil
}

// GetGlobalPendingTasks returns all pending tasks in the global queue.
func (m *MemoryTaskManager) GetGlobalPendingTasks(ctx context.Context) ([]*controlplanev1.Task, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var tasks []*controlplanev1.Task
	for _, item := range *m.globalQueue {
		if !m.cancelledTasks[item.task.TaskID] {
			tasks = append(tasks, item.task)
		}
	}

	return tasks, nil
}

// CancelTask cancels a task by ID.
func (m *MemoryTaskManager) CancelTask(ctx context.Context, taskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cancelledTasks[taskID] = true

	// Update task status if exists
	if info, ok := m.taskDetails[taskID]; ok {
		info.Status = controlplanev1.TaskStatusCancelled
	}

	m.logger.Info("Task cancelled", zap.String("task_id", taskID))
	return nil
}

// IsTaskCancelled checks if a task has been cancelled.
func (m *MemoryTaskManager) IsTaskCancelled(ctx context.Context, taskID string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cancelledTasks[taskID], nil
}

// ReportTaskResult reports the result of a task execution.
func (m *MemoryTaskManager) ReportTaskResult(ctx context.Context, result *controlplanev1.TaskResult) error {
	if result == nil {
		return errors.New("result cannot be nil")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.results[result.TaskID] = result

	// Update task details
	if info, ok := m.taskDetails[result.TaskID]; ok {
		info.Status = result.Status
		info.Result = result
	}

	// Clear from running tasks
	delete(m.runningTasks, result.TaskID)

	m.logger.Debug("Task result reported",
		zap.String("task_id", result.TaskID),
		zap.String("status", result.Status.String()),
	)

	return nil
}

// GetTaskResult retrieves the result of a task.
func (m *MemoryTaskManager) GetTaskResult(ctx context.Context, taskID string) (*controlplanev1.TaskResult, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result, ok := m.results[taskID]
	return result, ok, nil
}

// GetTaskStatus retrieves the status of a task.
func (m *MemoryTaskManager) GetTaskStatus(ctx context.Context, taskID string) (*TaskInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	info, ok := m.taskDetails[taskID]
	if !ok {
		return nil, errors.New("task not found: " + taskID)
	}

	return info, nil
}

// SetTaskRunning marks a task as running by an agent.
func (m *MemoryTaskManager) SetTaskRunning(ctx context.Context, taskID string, agentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.runningTasks[taskID] = agentID

	if info, ok := m.taskDetails[taskID]; ok {
		info.Status = controlplanev1.TaskStatusRunning
		info.StartedAt = time.Now().UnixNano()
		info.AgentID = agentID
	}

	return nil
}

// Start initializes the task manager.
func (m *MemoryTaskManager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.started {
		return nil
	}

	m.logger.Info("Starting memory task manager")

	// Start cleanup goroutine
	m.wg.Add(1)
	go m.cleanupLoop()

	m.started = true
	return nil
}

// Close releases resources.
func (m *MemoryTaskManager) Close() error {
	m.mu.Lock()
	if !m.started {
		m.mu.Unlock()
		return nil
	}
	m.started = false
	m.mu.Unlock()

	close(m.stopChan)
	m.wg.Wait()

	return nil
}

// cleanupLoop periodically cleans up expired tasks and old results.
func (m *MemoryTaskManager) cleanupLoop() {
	defer m.wg.Done()

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopChan:
			return
		case <-ticker.C:
			m.cleanup()
		}
	}
}

// cleanup removes expired tasks and old results.
func (m *MemoryTaskManager) cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UnixNano()
	resultTTL := m.config.ResultTTL.Nanoseconds()
	if resultTTL <= 0 {
		resultTTL = (24 * time.Hour).Nanoseconds()
	}

	// Clean up old results
	for taskID, result := range m.results {
		if now-result.CompletedAtUnixNano > resultTTL {
			delete(m.results, taskID)
			delete(m.taskDetails, taskID)
			delete(m.cancelledTasks, taskID)
		}
	}
}

// taskItem wraps a task for the priority queue.
type taskItem struct {
	task  *controlplanev1.Task
	index int
}

// taskPriorityQueue implements heap.Interface for task priority.
type taskPriorityQueue []*taskItem

func (pq taskPriorityQueue) Len() int { return len(pq) }

func (pq taskPriorityQueue) Less(i, j int) bool {
	// Higher priority first
	if pq[i].task.Priority != pq[j].task.Priority {
		return pq[i].task.Priority > pq[j].task.Priority
	}
	// Earlier creation time first (FIFO for same priority)
	return pq[i].task.CreatedAtUnixNano < pq[j].task.CreatedAtUnixNano
}

func (pq taskPriorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

func (pq *taskPriorityQueue) Push(x any) {
	n := len(*pq)
	item := x.(*taskItem)
	item.index = n
	*pq = append(*pq, item)
}

func (pq *taskPriorityQueue) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	item.index = -1
	*pq = old[0 : n-1]
	return item
}
