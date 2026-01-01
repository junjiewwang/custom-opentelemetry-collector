// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package controlplaneext

import (
	"context"
	"encoding/json"
	"runtime"
	"runtime/pprof"
	"bytes"
	"time"

	"go.uber.org/zap"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// heapDumpHandler handles heap dump tasks.
type heapDumpHandler struct {
	logger *zap.Logger
}

func (h *heapDumpHandler) Type() string {
	return "heap_dump"
}

func (h *heapDumpHandler) Execute(ctx context.Context, task *controlplanev1.Task) (*controlplanev1.TaskResult, error) {
	h.logger.Debug("Executing heap dump", zap.String("task_id", task.TaskID))

	var buf bytes.Buffer
	if err := pprof.WriteHeapProfile(&buf); err != nil {
		return &controlplanev1.TaskResult{
			TaskID:            task.TaskID,
			Status:            controlplanev1.TaskStatusFailed,
			ErrorMessage:      "failed to write heap profile: " + err.Error(),
			CompletedAtMillis: time.Now().UnixMilli(),
		}, nil
	}

	return &controlplanev1.TaskResult{
		TaskID:            task.TaskID,
		Status:            controlplanev1.TaskStatusSuccess,
		ResultData:        buf.Bytes(),
		CompletedAtMillis: time.Now().UnixMilli(),
	}, nil
}

// threadDumpHandler handles thread dump tasks.
type threadDumpHandler struct {
	logger *zap.Logger
}

func (h *threadDumpHandler) Type() string {
	return "thread_dump"
}

func (h *threadDumpHandler) Execute(ctx context.Context, task *controlplanev1.Task) (*controlplanev1.TaskResult, error) {
	h.logger.Debug("Executing thread dump", zap.String("task_id", task.TaskID))

	// Get all goroutine stacks
	buf := make([]byte, 1024*1024) // 1MB buffer
	n := runtime.Stack(buf, true)  // true = all goroutines
	
	return &controlplanev1.TaskResult{
		TaskID:            task.TaskID,
		Status:            controlplanev1.TaskStatusSuccess,
		ResultData:        buf[:n],
		CompletedAtMillis: time.Now().UnixMilli(),
	}, nil
}

// configExportHandler handles config export tasks.
type configExportHandler struct {
	logger *zap.Logger
}

func (h *configExportHandler) Type() string {
	return "config_export"
}

func (h *configExportHandler) Execute(ctx context.Context, task *controlplanev1.Task) (*controlplanev1.TaskResult, error) {
	h.logger.Debug("Executing config export", zap.String("task_id", task.TaskID))

	// Export runtime information
	info := map[string]any{
		"go_version":    runtime.Version(),
		"num_cpu":       runtime.NumCPU(),
		"num_goroutine": runtime.NumGoroutine(),
		"go_os":         runtime.GOOS,
		"go_arch":       runtime.GOARCH,
	}

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	info["memory"] = map[string]any{
		"alloc":       memStats.Alloc,
		"total_alloc": memStats.TotalAlloc,
		"sys":         memStats.Sys,
		"num_gc":      memStats.NumGC,
	}

	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return &controlplanev1.TaskResult{
			TaskID:            task.TaskID,
			Status:            controlplanev1.TaskStatusFailed,
			ErrorMessage:      "failed to marshal config: " + err.Error(),
			CompletedAtMillis: time.Now().UnixMilli(),
		}, nil
	}

	return &controlplanev1.TaskResult{
		TaskID:            task.TaskID,
		Status:            controlplanev1.TaskStatusSuccess,
		ResultData:        data,
		CompletedAtMillis: time.Now().UnixMilli(),
	}, nil
}
