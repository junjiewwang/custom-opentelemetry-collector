// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// Notifier defines the interface for notifying external analysis services.
// Implementations must be safe for concurrent use.
type Notifier interface {
	// Notify sends an artifact notification to the analysis service.
	// Returns a NotifyResult recording the outcome.
	Notify(ctx context.Context, n *ArtifactNotification) *NotifyResult

	// ShouldNotify returns true if the given task type should trigger notification.
	ShouldNotify(taskType string) bool
}

// httpNotifier sends notifications via HTTP POST to an analysis service.
type httpNotifier struct {
	logger     *zap.Logger
	client     *http.Client
	serviceURL string
	callbackURL string
	taskTypes  map[string]struct{}
}

// NewHTTPNotifier creates an HTTP-based notifier.
func NewHTTPNotifier(logger *zap.Logger, cfg Config) Notifier {
	typeSet := make(map[string]struct{}, len(cfg.TaskTypes))
	for _, t := range cfg.TaskTypes {
		typeSet[t] = struct{}{}
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	return &httpNotifier{
		logger:      logger.Named("artifact-notifier"),
		client:      &http.Client{Timeout: timeout},
		serviceURL:  cfg.AnalysisServiceURL,
		callbackURL: cfg.CallbackURL,
		taskTypes:   typeSet,
	}
}

func (n *httpNotifier) ShouldNotify(taskType string) bool {
	_, ok := n.taskTypes[taskType]
	return ok
}

// perfAnalysisRequest matches the perf-analysis flat task submission format.
// See: perf-analysis API doc — POST /tasks
type perfAnalysisRequest struct {
	TID         string            `json:"tid"`
	Profiler    string            `json:"profiler"`
	Event       string            `json:"event"`
	ResultFile  string            `json:"result_file,omitempty"`
	CallbackURL string            `json:"callback_url,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

func (n *httpNotifier) Notify(ctx context.Context, notification *ArtifactNotification) *NotifyResult {
	result := &NotifyResult{NotifiedAt: time.Now()}

	reqBody := perfAnalysisRequest{
		TID:         notification.TaskID,
		Profiler:    notification.Profiler,
		Event:       notification.Event,
		ResultFile:  notification.ArtifactRef,
		CallbackURL: n.callbackURL,
		Metadata: map[string]string{
			"origin":             "otel-collector",
			"original_task_type": notification.TaskType,
		},
	}

	// Merge artifact metadata
	for k, v := range notification.Metadata {
		reqBody.Metadata[k] = v
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("marshal request: %v", err)
		result.AttemptCount = 1
		return result
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.serviceURL, bytes.NewReader(body))
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("create request: %v", err)
		result.AttemptCount = 1
		return result
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	result.AttemptCount = 1
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("send request: %v", err)
		return result
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	result.StatusCode = resp.StatusCode
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		result.Success = true
	} else {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		result.ErrorMessage = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return result
}

// noopNotifier is a no-op implementation used when notification is disabled.
type noopNotifier struct{}

// NewNoopNotifier returns a notifier that does nothing.
func NewNoopNotifier() Notifier {
	return &noopNotifier{}
}

func (n *noopNotifier) Notify(_ context.Context, _ *ArtifactNotification) *NotifyResult {
	return &NotifyResult{Success: true, NotifiedAt: time.Now()}
}

func (n *noopNotifier) ShouldNotify(_ string) bool {
	return false
}
