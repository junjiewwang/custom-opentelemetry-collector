// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"container/heap"
	"context"
	"errors"
	"sync"
	"time"

	"go.uber.org/zap"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane_legacy/v1"
)

// MemoryTaskStore implements TaskStore using in-memory storage.
type MemoryTaskStore struct {
	logger *zap.Logger
	ttl    time.Duration

	mu             sync.RWMutex
	taskInfos      map[string]*TaskInfo
	globalQueue    *taskPriorityQueue
	agentQueues    map[string]*taskPriorityQueue
	cancelledTasks map[string]bool
	results        map[string]*controlplanev1.TaskResult
	runningTasks   map[string]string // taskID -> agentID

	// Lifecycle
	started  bool
	stopChan chan struct{}
	wg       sync.WaitGroup
}

// NewMemoryTaskStore creates a new in-memory task store.
func NewMemoryTaskStore(logger *zap.Logger, ttl time.Duration) *MemoryTaskStore {
	s := &MemoryTaskStore{
		logger:         logger,
		ttl:            ttl,
		taskInfos:      make(map[string]*TaskInfo),
		globalQueue:    &taskPriorityQueue{},
		agentQueues:    make(map[string]*taskPriorityQueue),
		cancelledTasks: make(map[string]bool),
		results:        make(map[string]*controlplanev1.TaskResult),
		runningTasks:   make(map[string]string),
		stopChan:       make(chan struct{}),
	}
	heap.Init(s.globalQueue)
	return s
}

// Ensure MemoryTaskStore implements TaskStore.
var _ TaskStore = (*MemoryTaskStore)(nil)

// ===== Task Detail Operations =====

// SaveTaskInfo implements TaskStore.
func (s *MemoryTaskStore) SaveTaskInfo(ctx context.Context, info *TaskInfo, isNew bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	taskID := info.Task.TaskID
	if isNew {
		if _, exists := s.taskInfos[taskID]; exists {
			return errors.New("task already exists: " + taskID)
		}
	}

	// Deep copy to avoid external mutation
	copied := *info
	s.taskInfos[taskID] = &copied
	return nil
}

// GetTaskInfo implements TaskStore.
func (s *MemoryTaskStore) GetTaskInfo(ctx context.Context, taskID string) (*TaskInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	info, ok := s.taskInfos[taskID]
	if !ok {
		return nil, nil
	}

	// Return a copy
	copied := *info
	return &copied, nil
}

// UpdateTaskInfo implements TaskStore.
func (s *MemoryTaskStore) UpdateTaskInfo(ctx context.Context, taskID string, updater func(*TaskInfo) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	info, ok := s.taskInfos[taskID]
	if !ok {
		return TaskNotFound(taskID)
	}

	// Apply updater
	if err := updater(info); err != nil {
		// If updater says no update needed, return success
		if errors.Is(err, ErrNoUpdateNeeded) {
			return nil
		}
		return err
	}

	return nil
}

// ===== Atomic State Machine Operations (Authoritative) =====

func (s *MemoryTaskStore) ApplyTaskResult(ctx context.Context, taskID string, result *controlplanev1.TaskResult, nowMillis int64) (ApplyTaskUpdateResult, error) {
	if result == nil {
		return ApplyTaskUpdateResult{}, errors.New("result cannot be nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	info, ok := s.taskInfos[taskID]
	if !ok {
		return ApplyTaskUpdateResult{}, TaskNotFound(taskID)
	}

	cur := info.Status
	newStatus := result.Status

	// Once terminal, everything is a no-op (first terminal wins).
	if cur.IsTerminal() {
		return ApplyTaskUpdateResult{Code: ApplyTaskNoop, Status: cur, AgentID: info.AgentID}, nil
	}

	// Idempotent.
	if cur == newStatus {
		return ApplyTaskUpdateResult{Code: ApplyTaskNoop, Status: cur, AgentID: info.AgentID}, nil
	}

	// Reject rollback RUNNING -> PENDING.
	if cur == controlplanev1.TaskStatusRunning && newStatus == controlplanev1.TaskStatusPending {
		return ApplyTaskUpdateResult{Code: ApplyTaskRejected, Status: cur, AgentID: info.AgentID}, nil
	}

	// Apply update.
	info.Status = newStatus
	info.Result = result
	info.LastUpdatedAtMillis = nowMillis
	info.Version++

	if result.AgentID != "" && info.AgentID == "" {
		info.AgentID = result.AgentID
	}

	if newStatus == controlplanev1.TaskStatusRunning && info.StartedAtMillis == 0 {
		info.StartedAtMillis = nowMillis
	}

	return ApplyTaskUpdateResult{Code: ApplyTaskUpdated, Status: info.Status, AgentID: info.AgentID}, nil
}

func (s *MemoryTaskStore) ApplyCancel(ctx context.Context, taskID string, nowMillis int64) (ApplyTaskUpdateResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	info, ok := s.taskInfos[taskID]
	if !ok {
		return ApplyTaskUpdateResult{}, TaskNotFound(taskID)
	}

	cur := info.Status
	if cur == controlplanev1.TaskStatusCancelled {
		return ApplyTaskUpdateResult{Code: ApplyTaskNoop, Status: cur, AgentID: info.AgentID}, nil
	}

	// Reject cancelling a non-cancelled terminal task.
	if cur.IsTerminal() {
		return ApplyTaskUpdateResult{Code: ApplyTaskRejected, Status: cur, AgentID: info.AgentID}, nil
	}

	info.Status = controlplanev1.TaskStatusCancelled
	info.LastUpdatedAtMillis = nowMillis
	info.Version++

	return ApplyTaskUpdateResult{Code: ApplyTaskUpdated, Status: info.Status, AgentID: info.AgentID}, nil
}

func (s *MemoryTaskStore) ApplySetRunning(ctx context.Context, taskID string, agentID string, nowMillis int64) (ApplyTaskUpdateResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	info, ok := s.taskInfos[taskID]
	if !ok {
		return ApplyTaskUpdateResult{}, TaskNotFound(taskID)
	}

	cur := info.Status
	if cur == controlplanev1.TaskStatusRunning {
		return ApplyTaskUpdateResult{Code: ApplyTaskNoop, Status: cur, AgentID: info.AgentID}, nil
	}

	if cur.IsTerminal() {
		return ApplyTaskUpdateResult{Code: ApplyTaskRejected, Status: cur, AgentID: info.AgentID}, nil
	}

	info.Status = controlplanev1.TaskStatusRunning
	info.AgentID = agentID
	if info.StartedAtMillis == 0 {
		info.StartedAtMillis = nowMillis
	}
	info.LastUpdatedAtMillis = nowMillis
	info.Version++

	return ApplyTaskUpdateResult{Code: ApplyTaskUpdated, Status: info.Status, AgentID: info.AgentID}, nil
}

// ListTaskInfos implements TaskStore.
func (s *MemoryTaskStore) ListTaskInfos(ctx context.Context) ([]*TaskInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*TaskInfo, 0, len(s.taskInfos))
	for _, info := range s.taskInfos {
		copied := *info
		result = append(result, &copied)
	}
	return result, nil
}

// DeleteTaskInfo implements TaskStore.
func (s *MemoryTaskStore) DeleteTaskInfo(ctx context.Context, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.taskInfos, taskID)
	delete(s.cancelledTasks, taskID)
	delete(s.results, taskID)
	delete(s.runningTasks, taskID)
	return nil
}

// ===== Queue Operations =====

// EnqueueTask implements TaskStore.
func (s *MemoryTaskStore) EnqueueTask(ctx context.Context, queueID string, taskID string, priority int32, createdAtMillis int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	item := &taskItem{
		taskID:          taskID,
		priority:        priority,
		createdAtMillis: createdAtMillis,
	}

	if queueID == QueueGlobal {
		heap.Push(s.globalQueue, item)
	} else {
		queue, ok := s.agentQueues[queueID]
		if !ok {
			queue = &taskPriorityQueue{}
			heap.Init(queue)
			s.agentQueues[queueID] = queue
		}
		heap.Push(queue, item)
	}
	return nil
}

// DequeueTask implements TaskStore.
func (s *MemoryTaskStore) DequeueTask(ctx context.Context, queueID string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		s.mu.Lock()
		queue := s.getQueueLocked(queueID)
		if queue != nil && queue.Len() > 0 {
			item := heap.Pop(queue).(*taskItem)
			s.mu.Unlock()
			return item.taskID, nil
		}
		s.mu.Unlock()

		// Wait a bit before retrying
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return "", nil // Timeout
}

// DequeueTaskMulti implements TaskStore.
func (s *MemoryTaskStore) DequeueTaskMulti(ctx context.Context, queueIDs []string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		s.mu.Lock()
		for _, queueID := range queueIDs {
			queue := s.getQueueLocked(queueID)
			if queue != nil && queue.Len() > 0 {
				item := heap.Pop(queue).(*taskItem)
				s.mu.Unlock()
				return item.taskID, nil
			}
		}
		s.mu.Unlock()

		// Wait a bit before retrying
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return "", nil // Timeout
}

// PeekQueue implements TaskStore.
func (s *MemoryTaskStore) PeekQueue(ctx context.Context, queueID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	queue := s.getQueueLocked(queueID)
	if queue == nil {
		return nil, nil
	}

	result := make([]string, 0, queue.Len())
	for _, item := range *queue {
		result = append(result, item.taskID)
	}
	return result, nil
}

// RemoveFromQueue implements TaskStore.
func (s *MemoryTaskStore) RemoveFromQueue(ctx context.Context, queueID string, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	queue := s.getQueueLocked(queueID)
	if queue == nil {
		return nil
	}

	for i, item := range *queue {
		if item.taskID == taskID {
			heap.Remove(queue, i)
			return nil
		}
	}
	return nil
}

// RemoveFromAllQueues implements TaskStore.
func (s *MemoryTaskStore) RemoveFromAllQueues(ctx context.Context, taskID string, agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove from global queue
	s.removeFromQueueLocked(s.globalQueue, taskID)

	// Remove from agent-specific queue
	if agentID != "" {
		if queue, ok := s.agentQueues[agentID]; ok {
			s.removeFromQueueLocked(queue, taskID)
		}
	}

	// Also try all agent queues (in case agentID changed)
	for _, queue := range s.agentQueues {
		s.removeFromQueueLocked(queue, taskID)
	}

	return nil
}

func (s *MemoryTaskStore) getQueueLocked(queueID string) *taskPriorityQueue {
	if queueID == QueueGlobal {
		return s.globalQueue
	}
	return s.agentQueues[queueID]
}

func (s *MemoryTaskStore) removeFromQueueLocked(queue *taskPriorityQueue, taskID string) {
	for i, item := range *queue {
		if item.taskID == taskID {
			heap.Remove(queue, i)
			return
		}
	}
}

// ===== Result Operations =====

// SaveResult implements TaskStore.
func (s *MemoryTaskStore) SaveResult(ctx context.Context, result *controlplanev1.TaskResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.results[result.TaskID] = result
	return nil
}

// GetResult implements TaskStore.
func (s *MemoryTaskStore) GetResult(ctx context.Context, taskID string) (*controlplanev1.TaskResult, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result, ok := s.results[taskID]
	return result, ok, nil
}

// ===== Cancellation Operations =====

// SetCancelled implements TaskStore.
func (s *MemoryTaskStore) SetCancelled(ctx context.Context, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cancelledTasks[taskID] = true
	return nil
}

// IsCancelled implements TaskStore.
func (s *MemoryTaskStore) IsCancelled(ctx context.Context, taskID string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.cancelledTasks[taskID], nil
}

// ===== Running State Operations =====

// SetRunning implements TaskStore.
func (s *MemoryTaskStore) SetRunning(ctx context.Context, taskID string, agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.runningTasks[taskID] = agentID
	return nil
}

// GetRunning implements TaskStore.
func (s *MemoryTaskStore) GetRunning(ctx context.Context, taskID string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.runningTasks[taskID], nil
}

// ClearRunning implements TaskStore.
func (s *MemoryTaskStore) ClearRunning(ctx context.Context, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.runningTasks, taskID)
	return nil
}

// ===== Event Operations =====

// PublishEvent implements TaskStore.
// Memory store doesn't support pub/sub, so this is a no-op.
func (s *MemoryTaskStore) PublishEvent(ctx context.Context, eventType string, taskID string) error {
	return nil
}

// ===== Lifecycle =====

// Start implements TaskStore.
func (s *MemoryTaskStore) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started {
		return nil
	}

	s.logger.Info("Starting memory task store")

	// Start cleanup goroutine
	s.wg.Add(1)
	go s.cleanupLoop()

	s.started = true
	return nil
}

// Close implements TaskStore.
func (s *MemoryTaskStore) Close() error {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	s.started = false
	s.mu.Unlock()

	close(s.stopChan)
	s.wg.Wait()

	return nil
}

// cleanupLoop periodically cleans up expired tasks and old results.
func (s *MemoryTaskStore) cleanupLoop() {
	defer s.wg.Done()

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopChan:
			return
		case <-ticker.C:
			s.cleanup()
		}
	}
}

// cleanup removes expired tasks and old results.
func (s *MemoryTaskStore) cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UnixMilli()
	ttlMillis := s.ttl.Milliseconds()
	if ttlMillis <= 0 {
		ttlMillis = (24 * time.Hour).Milliseconds()
	}

	// Clean up old results
	for taskID, result := range s.results {
		if now-result.CompletedAtMillis > ttlMillis {
			delete(s.results, taskID)
			delete(s.taskInfos, taskID)
			delete(s.cancelledTasks, taskID)
		}
	}
}

// ===== Priority Queue Implementation =====

// taskItem wraps a task for the priority queue.
type taskItem struct {
	taskID          string
	priority        int32
	createdAtMillis int64
	index           int
}

// taskPriorityQueue implements heap.Interface for task priority.
type taskPriorityQueue []*taskItem

func (pq taskPriorityQueue) Len() int { return len(pq) }

func (pq taskPriorityQueue) Less(i, j int) bool {
	// Higher priority first
	if pq[i].priority != pq[j].priority {
		return pq[i].priority > pq[j].priority
	}
	// Earlier creation time first (FIFO for same priority)
	return pq[i].createdAtMillis < pq[j].createdAtMillis
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
