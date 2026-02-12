// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"encoding/json"
	"net/http"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/notification"
)

// ============================================================================
// Analysis Callback (from perf-analysis service)
// ============================================================================

// analysisCallbackRequest is the expected payload from perf-analysis.
// Matches perf-analysis callback format: {task_id, mode, status, view_url, error, summary, metadata}
type analysisCallbackRequest struct {
	TaskID   string `json:"task_id"`
	Mode     string `json:"mode"`              // "{profiler}-{event}", e.g., "async-profiler-cpu"
	Status   string `json:"status"`            // "completed", "failed", etc.
	ViewURL  string `json:"view_url"`          // URL to view analysis results
	ErrorMsg string `json:"error,omitempty"`
	Summary  *analysisCallbackSummary `json:"summary,omitempty"`
	Metadata map[string]string        `json:"metadata,omitempty"` // transparently passed through from submission
}

// analysisCallbackSummary contains analysis summary metrics.
type analysisCallbackSummary struct {
	TotalRecords int `json:"total_records"`
	Suggestions  int `json:"suggestions,omitempty"`
}

// handleAnalysisCallback handles POST /api/v2/callback/analysis
//
// Called by perf-analysis when artifact analysis completes.
// Merges analysis results into the TaskResult's ResultJSON field.
func (e *Extension) handleAnalysisCallback(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[analysisCallbackRequest](r)
	if err != nil {
		e.handleError(w, errBadRequest(err.Error()))
		return
	}

	if req.TaskID == "" {
		e.handleError(w, errBadRequest("task_id is required"))
		return
	}
	if req.Status == "" {
		e.handleError(w, errBadRequest("status is required"))
		return
	}

	e.logger.Info("Received analysis callback",
		zap.String("task_id", req.TaskID),
		zap.String("mode", req.Mode),
		zap.String("status", req.Status),
		zap.String("view_url", req.ViewURL),
	)

	// Retrieve existing TaskResult
	if e.taskMgr == nil {
		e.handleError(w, errInternal("task manager not available"))
		return
	}

	result, found, err := e.taskMgr.GetTaskResult(r.Context(), req.TaskID)
	if err != nil {
		e.logger.Error("Failed to get task result for analysis callback",
			zap.String("task_id", req.TaskID),
			zap.Error(err),
		)
		e.handleError(w, errInternal("failed to get task result"))
		return
	}
	if !found || result == nil {
		e.handleError(w, errNotFound("task result not found: "+req.TaskID))
		return
	}

	// Merge analysis fields into ResultJSON
	resultMap := make(map[string]any)
	if len(result.ResultJSON) > 0 {
		if err := json.Unmarshal(result.ResultJSON, &resultMap); err != nil {
			// If existing ResultJSON is not a valid map, wrap it
			resultMap = map[string]any{"_original": json.RawMessage(result.ResultJSON)}
		}
	}

	resultMap["analysis_status"] = req.Status
	if req.Mode != "" {
		resultMap["analysis_mode"] = req.Mode
	}
	if req.ViewURL != "" {
		resultMap["analysis_view_url"] = req.ViewURL
	}
	if req.ErrorMsg != "" {
		resultMap["analysis_error"] = req.ErrorMsg
	}
	if req.Summary != nil {
		resultMap["analysis_summary"] = req.Summary
	}
	if len(req.Metadata) > 0 {
		resultMap["analysis_metadata"] = req.Metadata
	}

	merged, err := json.Marshal(resultMap)
	if err != nil {
		e.logger.Error("Failed to marshal merged result JSON",
			zap.String("task_id", req.TaskID),
			zap.Error(err),
		)
		e.handleError(w, errInternal("failed to merge analysis result"))
		return
	}

	result.ResultJSON = merged

	if err := e.taskMgr.ReportTaskResult(r.Context(), result); err != nil {
		e.logger.Error("Failed to update task result with analysis data",
			zap.String("task_id", req.TaskID),
			zap.Error(err),
		)
		e.handleError(w, errInternal("failed to update task result"))
		return
	}

	// Update notification record status if store is available
	if e.notificationStore != nil {
		record, err := e.notificationStore.GetByTaskID(r.Context(), req.TaskID)
		if err == nil && record != nil {
			record.Status = notification.StatusCallbackReceived
			record.LastError = ""
			if err := e.notificationStore.Update(r.Context(), record); err != nil {
				e.logger.Warn("Failed to update notification record on callback",
					zap.String("task_id", req.TaskID),
					zap.Error(err),
				)
			}
		}
	}

	e.logger.Info("Analysis callback processed successfully",
		zap.String("task_id", req.TaskID),
		zap.String("status", req.Status),
	)

	e.writeJSON(w, http.StatusOK, successResponse("analysis callback processed", map[string]any{
		"task_id": req.TaskID,
	}))
}
