// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentgatewayreceiver

import (
	"net/http"

	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
	"go.uber.org/zap"
)

// controlPlaneHandler handles control plane HTTP requests (Protobuf binary only).
//
// Wire format:
// - Request:  application/x-protobuf (bytes)
// - Response: application/x-protobuf (bytes), optional gzip
//
// Error model:
// - Business errors are returned as 200 with ResponseStatus in the protobuf payload.
// - Auth/transport errors use HTTP status codes (e.g. 401/429/503).
//
// This is aligned with the Java agent Transport (HttpTransport/GrpcTransport).

type controlPlaneHandler struct {
	logger *zap.Logger
	svc    *controlPlaneService
}

func newControlPlaneHandler(logger *zap.Logger, svc *controlPlaneService) *controlPlaneHandler {
	return &controlPlaneHandler{logger: logger, svc: svc}
}

func (h *controlPlaneHandler) unifiedPoll(w http.ResponseWriter, r *http.Request) {
	req := &controlplanev1.UnifiedPollRequest{}
	if err := decodeProtobuf(r, req); err != nil {
		h.logger.Debug("Invalid UnifiedPoll request", zap.Error(err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp := h.svc.UnifiedPoll(r.Context(), req)
	writeProtobuf(w, r, http.StatusOK, resp)
}

func (h *controlPlaneHandler) getConfig(w http.ResponseWriter, r *http.Request) {
	req := &controlplanev1.ConfigRequest{}
	if err := decodeProtobuf(r, req); err != nil {
		h.logger.Debug("Invalid GetConfig request", zap.Error(err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp := h.svc.GetConfig(r.Context(), req)
	writeProtobuf(w, r, http.StatusOK, resp)
}

func (h *controlPlaneHandler) getTasks(w http.ResponseWriter, r *http.Request) {
	req := &controlplanev1.TaskRequest{}
	if err := decodeProtobuf(r, req); err != nil {
		h.logger.Debug("Invalid GetTasks request", zap.Error(err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp := h.svc.GetTasks(r.Context(), req)
	writeProtobuf(w, r, http.StatusOK, resp)
}

func (h *controlPlaneHandler) reportStatus(w http.ResponseWriter, r *http.Request) {
	req := &controlplanev1.StatusRequest{}
	if err := decodeProtobuf(r, req); err != nil {
		h.logger.Debug("Invalid ReportStatus request", zap.Error(err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp := h.svc.ReportStatus(r.Context(), req)
	writeProtobuf(w, r, http.StatusOK, resp)
}

func (h *controlPlaneHandler) reportTaskResult(w http.ResponseWriter, r *http.Request) {
	req := &controlplanev1.TaskResultRequest{}
	if err := decodeProtobuf(r, req); err != nil {
		h.logger.Debug("Invalid ReportTaskResult request", zap.Error(err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp := h.svc.ReportTaskResult(r.Context(), req)
	writeProtobuf(w, r, http.StatusOK, resp)
}

func (h *controlPlaneHandler) uploadChunkedResult(w http.ResponseWriter, r *http.Request) {
	req := &controlplanev1.ChunkedTaskResult{}
	if err := decodeProtobuf(r, req); err != nil {
		h.logger.Debug("Invalid UploadChunkedResult request", zap.Error(err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp := h.svc.UploadChunkedResult(r.Context(), req)
	writeProtobuf(w, r, http.StatusOK, resp)
}
