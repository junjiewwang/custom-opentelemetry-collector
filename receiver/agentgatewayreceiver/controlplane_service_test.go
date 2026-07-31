// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentgatewayreceiver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
	"go.opentelemetry.io/collector/custom/receiver/agentgatewayreceiver/longpoll"
	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

// ═══════════════════════════════════════════════════════════════════════
// controlplane_convert.go — pure proto-extraction helpers
// ═══════════════════════════════════════════════════════════════════════

func TestAgentIDFromUnifiedPoll_PriorityChain(t *testing.T) {
	// nil → empty
	assert.Empty(t, agentIDFromUnifiedPoll(nil))

	// top-level agent_id wins
	assert.Equal(t, "top",
		agentIDFromUnifiedPoll(&controlplanev1.UnifiedPollRequest{AgentId: "top"}))

	// falls back to config_request.agent_id
	assert.Equal(t, "cfg",
		agentIDFromUnifiedPoll(&controlplanev1.UnifiedPollRequest{
			ConfigRequest: &controlplanev1.ConfigRequest{AgentId: "cfg"},
		}))

	// falls back to task_request.agent_id
	assert.Equal(t, "task",
		agentIDFromUnifiedPoll(&controlplanev1.UnifiedPollRequest{
			TaskRequest: &controlplanev1.TaskRequest{AgentId: "task"},
		}))

	// nothing → empty
	assert.Empty(t, agentIDFromUnifiedPoll(&controlplanev1.UnifiedPollRequest{}))
}

func TestTimeoutFromUnifiedPoll_PriorityChain(t *testing.T) {
	assert.Equal(t, int64(0), timeoutFromUnifiedPoll(nil))
	assert.Equal(t, int64(100), timeoutFromUnifiedPoll(&controlplanev1.UnifiedPollRequest{TimeoutMillis: 100}))
	// top-level 0 → fall back to config_request.long_poll_timeout
	assert.Equal(t, int64(200), timeoutFromUnifiedPoll(&controlplanev1.UnifiedPollRequest{
		ConfigRequest: &controlplanev1.ConfigRequest{LongPollTimeoutMillis: 200},
	}))
	// → fall back to task_request.long_poll_timeout
	assert.Equal(t, int64(300), timeoutFromUnifiedPoll(&controlplanev1.UnifiedPollRequest{
		TaskRequest: &controlplanev1.TaskRequest{LongPollTimeoutMillis: 300},
	}))
	assert.Equal(t, int64(0), timeoutFromUnifiedPoll(&controlplanev1.UnifiedPollRequest{}))
}

func TestConfigVersionFromUnifiedPoll(t *testing.T) {
	assert.Empty(t, configVersionFromUnifiedPoll(nil))
	assert.Empty(t, configVersionFromUnifiedPoll(&controlplanev1.UnifiedPollRequest{})) // no config_request

	// prefers CurrentVersion.Version
	assert.Equal(t, "v2", configVersionFromUnifiedPoll(&controlplanev1.UnifiedPollRequest{
		ConfigRequest: &controlplanev1.ConfigRequest{
			CurrentVersion: &controlplanev1.ConfigVersion{Version: "v2"},
		},
	}))
	// falls back to CurrentConfigVersion
	assert.Equal(t, "v1", configVersionFromUnifiedPoll(&controlplanev1.UnifiedPollRequest{
		ConfigRequest: &controlplanev1.ConfigRequest{CurrentConfigVersion: "v1"},
	}))
}

func TestConfigEtagFromUnifiedPoll(t *testing.T) {
	assert.Empty(t, configEtagFromUnifiedPoll(nil))
	assert.Empty(t, configEtagFromUnifiedPoll(&controlplanev1.UnifiedPollRequest{}))
	assert.Equal(t, "e2", configEtagFromUnifiedPoll(&controlplanev1.UnifiedPollRequest{
		ConfigRequest: &controlplanev1.ConfigRequest{
			CurrentVersion: &controlplanev1.ConfigVersion{Etag: "e2"},
		},
	}))
	assert.Equal(t, "e1", configEtagFromUnifiedPoll(&controlplanev1.UnifiedPollRequest{
		ConfigRequest: &controlplanev1.ConfigRequest{CurrentEtag: "e1"},
	}))
}

func TestServiceNameFromUnifiedPoll(t *testing.T) {
	assert.Empty(t, serviceNameFromUnifiedPoll(nil))
	assert.Empty(t, serviceNameFromUnifiedPoll(&controlplanev1.UnifiedPollRequest{}))
	assert.Equal(t, "svc", serviceNameFromUnifiedPoll(&controlplanev1.UnifiedPollRequest{
		ConfigRequest: &controlplanev1.ConfigRequest{ServiceName: "svc"},
	}))
}

func TestAgentIDFromConfigRequest(t *testing.T) {
	assert.Empty(t, agentIDFromConfigRequest(nil))
	assert.Equal(t, "a1", agentIDFromConfigRequest(&controlplanev1.ConfigRequest{AgentId: "a1"}))
}

func TestAgentIDFromTaskRequest(t *testing.T) {
	assert.Empty(t, agentIDFromTaskRequest(nil))
	assert.Equal(t, "a2", agentIDFromTaskRequest(&controlplanev1.TaskRequest{AgentId: "a2"}))
}

func TestAgentIDFromTaskResultRequest(t *testing.T) {
	assert.Empty(t, agentIDFromTaskResultRequest(nil))
	assert.Equal(t, "a3", agentIDFromTaskResultRequest(&controlplanev1.TaskResultRequest{AgentId: "a3"}))
}

func TestAgentIDFromStatusRequest(t *testing.T) {
	assert.Empty(t, agentIDFromStatusRequest(nil))
	// top-level agent_id first
	assert.Equal(t, "a4", agentIDFromStatusRequest(&controlplanev1.StatusRequest{AgentId: "a4"}))
	// falls back to AgentIdentity.AgentId
	assert.Equal(t, "a5", agentIDFromStatusRequest(&controlplanev1.StatusRequest{
		AgentIdentity: &controlplanev1.AgentIdentity{AgentId: "a5"},
	}))
	assert.Empty(t, agentIDFromStatusRequest(&controlplanev1.StatusRequest{}))
}

// ═══════════════════════════════════════════════════════════════════════
// controlplane_service.go — pure error helpers + classifier
// ═══════════════════════════════════════════════════════════════════════

func TestPickFirstNonEmpty(t *testing.T) {
	assert.Empty(t, pickFirstNonEmpty())
	assert.Empty(t, pickFirstNonEmpty("", "", ""))
	assert.Equal(t, "x", pickFirstNonEmpty("", "x", "y"))
	assert.Equal(t, "first", pickFirstNonEmpty("first", "second"))
}

func TestClassifyBusinessError(t *testing.T) {
	cases := []struct {
		err  string
		want controlplanev1.ResponseStatus_Code
	}{
		{"task not found", controlplanev1.ResponseStatus_CODE_NOT_FOUND},
		{"invalid argument", controlplanev1.ResponseStatus_CODE_INVALID_ARGUMENT},
		{"field is required", controlplanev1.ResponseStatus_CODE_INVALID_ARGUMENT},
		{"service unavailable", controlplanev1.ResponseStatus_CODE_UNAVAILABLE},
		{"something unexpected", controlplanev1.ResponseStatus_CODE_ERROR},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, classifyBusinessError(errors.New(c.err)), c.err)
	}
}

func TestErrorHelpers_PopulateStatus(t *testing.T) {
	up := unifiedPollError(controlplanev1.ResponseStatus_CODE_NOT_FOUND, "nf")
	require.NotNil(t, up.Status)
	assert.Equal(t, controlplanev1.ResponseStatus_CODE_NOT_FOUND, up.Status.Code)
	assert.Equal(t, "nf", up.Status.Message)

	cfg := configError(controlplanev1.ResponseStatus_CODE_INVALID_ARGUMENT, "ia")
	assert.Equal(t, controlplanev1.ResponseStatus_CODE_INVALID_ARGUMENT, cfg.Status.Code)

	tk := taskError(controlplanev1.ResponseStatus_CODE_ERROR, "e")
	assert.Equal(t, "e", tk.Status.Message)

	st := statusError(controlplanev1.ResponseStatus_CODE_OK, "ok")
	assert.Equal(t, controlplanev1.ResponseStatus_CODE_OK, st.Status.Code)
	assert.NotZero(t, st.ServerTimeMillis, "statusError must stamp ServerTimeMillis")

	tr := taskResultError(controlplanev1.ResponseStatus_CODE_ERROR, "fail")
	assert.False(t, tr.Acknowledged)
	assert.Equal(t, "fail", tr.Message)

	ch := chunkError(controlplanev1.ChunkUploadStatus_CHUNK_UPLOAD_STATUS_UPLOAD_COMPLETE, "done")
	assert.Equal(t, controlplanev1.ChunkUploadStatus_CHUNK_UPLOAD_STATUS_UPLOAD_COMPLETE, ch.Status)
	assert.Equal(t, "done", ch.ErrorMessage)
}

// ═══════════════════════════════════════════════════════════════════════
// controlPlaneService — business logic with a fake poller (no real Manager)
// ═══════════════════════════════════════════════════════════════════════

// fakePoller implements poller for service-level tests. It returns scripted
// responses and records the requests it received.
type fakePoller struct {
	pollResp        *longpoll.CombinedPollResponse
	pollErr         error
	pollSingleResp  *longpoll.PollResponse
	pollSingleErr   error
	lastPollReq     *longpoll.PollRequest
	lastSingleReq   *longpoll.PollRequest
	lastSingleType  longpoll.LongPollType
	pollSingleCalls int
}

func (f *fakePoller) Poll(_ context.Context, req *longpoll.PollRequest, _ ...longpoll.LongPollType) (*longpoll.CombinedPollResponse, error) {
	f.lastPollReq = req
	return f.pollResp, f.pollErr
}

func (f *fakePoller) PollSingle(_ context.Context, req *longpoll.PollRequest, pt longpoll.LongPollType) (*longpoll.PollResponse, error) {
	f.lastSingleReq = req
	f.lastSingleType = pt
	f.pollSingleCalls++
	return f.pollSingleResp, f.pollSingleErr
}

func newSvcWithPoller(p poller) *controlPlaneService {
	return newControlPlaneService(zap.NewNop(), nil, p)
}

func TestService_UnifiedPoll_NoManager_Unavailable(t *testing.T) {
	svc := newControlPlaneService(zap.NewNop(), nil, nil)
	resp := svc.UnifiedPoll(context.Background(), &controlplanev1.UnifiedPollRequest{})
	require.NotNil(t, resp.Status)
	assert.Equal(t, controlplanev1.ResponseStatus_CODE_UNAVAILABLE, resp.Status.Code)
}

func TestService_UnifiedPoll_ConfigMissingServiceName_Invalid(t *testing.T) {
	svc := newSvcWithPoller(&fakePoller{})
	resp := svc.UnifiedPoll(context.Background(), &controlplanev1.UnifiedPollRequest{
		ConfigRequest: &controlplanev1.ConfigRequest{}, // no service_name
	})
	assert.Equal(t, controlplanev1.ResponseStatus_CODE_INVALID_ARGUMENT, resp.Status.Code)
}

func TestService_UnifiedPoll_PollerError_Classified(t *testing.T) {
	svc := newSvcWithPoller(&fakePoller{pollErr: errors.New("backend unavailable")})
	resp := svc.UnifiedPoll(context.Background(), &controlplanev1.UnifiedPollRequest{
		ConfigRequest: &controlplanev1.ConfigRequest{ServiceName: "svc"},
	})
	assert.Equal(t, controlplanev1.ResponseStatus_CODE_ERROR, resp.Status.Code)
	assert.Contains(t, resp.Status.Message, "unavailable")
}

func TestService_UnifiedPoll_BuildsPollRequestFromProto(t *testing.T) {
	fp := &fakePoller{pollResp: &longpoll.CombinedPollResponse{}}
	svc := newSvcWithPoller(fp)

	ctx := context.WithValue(context.Background(), ContextKeyAppID, "app-1")
	svc.UnifiedPoll(ctx, &controlplanev1.UnifiedPollRequest{
		AgentId:       "agent-1",
		TimeoutMillis: 5000,
		ConfigRequest: &controlplanev1.ConfigRequest{
			ServiceName: "svc-x",
			CurrentVersion: &controlplanev1.ConfigVersion{
				Version: "v9",
				Etag:    "e9",
			},
		},
	})

	require.NotNil(t, fp.lastPollReq)
	assert.Equal(t, "agent-1", fp.lastPollReq.AgentID)
	assert.Equal(t, "app-1", fp.lastPollReq.AppID)
	assert.Equal(t, "svc-x", fp.lastPollReq.ServiceName)
	assert.Equal(t, "v9", fp.lastPollReq.CurrentConfigVersion)
	assert.Equal(t, "e9", fp.lastPollReq.CurrentConfigEtag)
	assert.Equal(t, int64(5000), fp.lastPollReq.TimeoutMillis)
}

func TestService_UnifiedPoll_AssemblesConfigAndTaskResponses(t *testing.T) {
	fp := &fakePoller{pollResp: &longpoll.CombinedPollResponse{
		HasAnyChanges: true,
		Results: map[longpoll.LongPollType]*longpoll.PollResponse{
			longpoll.LongPollTypeConfig: {
				HasChanges:    true,
				ConfigVersion: "v3",
				ConfigEtag:    "e3",
				Config:        &model.AgentConfig{Version: "v3"},
			},
			longpoll.LongPollTypeTask: {
				HasChanges: true,
				Tasks:      []*model.Task{{ID: "t1"}},
			},
		},
	}}
	svc := newSvcWithPoller(fp)

	resp := svc.UnifiedPoll(context.Background(), &controlplanev1.UnifiedPollRequest{
		ConfigRequest: &controlplanev1.ConfigRequest{ServiceName: "svc"},
	})
	assert.True(t, resp.HasAnyChanges)
	require.NotNil(t, resp.ConfigResponse)
	assert.True(t, resp.ConfigResponse.HasChanges)
	assert.Equal(t, "v3", resp.ConfigResponse.ConfigVersion)
	require.NotNil(t, resp.TaskResponse)
	require.Len(t, resp.TaskResponse.Tasks, 1)
}

func TestService_UnifiedPoll_NilResultsSkipped(t *testing.T) {
	fp := &fakePoller{pollResp: &longpoll.CombinedPollResponse{
		Results: map[longpoll.LongPollType]*longpoll.PollResponse{
			longpoll.LongPollTypeConfig: nil, // skipped
		},
	}}
	svc := newSvcWithPoller(fp)
	resp := svc.UnifiedPoll(context.Background(), &controlplanev1.UnifiedPollRequest{
		ConfigRequest: &controlplanev1.ConfigRequest{ServiceName: "svc"},
	})
	assert.Equal(t, controlplanev1.ResponseStatus_CODE_OK, resp.Status.Code)
	assert.Nil(t, resp.ConfigResponse)
}

func TestService_GetConfig_PollerError(t *testing.T) {
	svc := newSvcWithPoller(&fakePoller{pollSingleErr: errors.New("not found")})
	resp := svc.GetConfig(context.Background(), &controlplanev1.ConfigRequest{ServiceName: "svc"})
	assert.Equal(t, controlplanev1.ResponseStatus_CODE_ERROR, resp.Status.Code)
}

func TestService_GetConfig_OK(t *testing.T) {
	fp := &fakePoller{pollSingleResp: &longpoll.PollResponse{
		HasChanges:    true,
		ConfigVersion: "v1",
		Config:        &model.AgentConfig{Version: "v1"},
	}}
	svc := newSvcWithPoller(fp)
	resp := svc.GetConfig(context.Background(), &controlplanev1.ConfigRequest{ServiceName: "svc"})
	assert.True(t, resp.Success)
	assert.Equal(t, "v1", resp.ConfigVersion)
	assert.Equal(t, longpoll.LongPollTypeConfig, fp.lastSingleType, "GetConfig must poll the CONFIG type")
}

func TestService_GetTasks_OK(t *testing.T) {
	fp := &fakePoller{pollSingleResp: &longpoll.PollResponse{
		HasChanges: true,
		Tasks:      []*model.Task{{ID: "t1"}, {ID: "t2"}},
	}}
	svc := newSvcWithPoller(fp)
	resp := svc.GetTasks(context.Background(), &controlplanev1.TaskRequest{AgentId: "a1"})
	require.Len(t, resp.Tasks, 2)
	assert.Equal(t, longpoll.LongPollTypeTask, fp.lastSingleType, "GetTasks must poll the TASK type")
}

func TestService_GetConfig_NoManager_Unavailable(t *testing.T) {
	svc := newControlPlaneService(zap.NewNop(), nil, nil)
	resp := svc.GetConfig(context.Background(), &controlplanev1.ConfigRequest{})
	assert.Equal(t, controlplanev1.ResponseStatus_CODE_UNAVAILABLE, resp.Status.Code)
}

// ═══════════════════════════════════════════════════════════════════════
// request_helper.go — HTTP/proto helpers (using httptest)
// ═══════════════════════════════════════════════════════════════════════

func TestWantsGzip(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	assert.False(t, wantsGzip(req))
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	assert.True(t, wantsGzip(req))
}

func TestIsProtobufContentType(t *testing.T) {
	assert.False(t, isProtobufContentType(""))
	assert.False(t, isProtobufContentType("application/json"))
	assert.True(t, isProtobufContentType("application/x-protobuf"))
	assert.True(t, isProtobufContentType("APPLICATION/X-PROTOBUF")) // case-insensitive
}

func TestDecodeProtobuf_RejectsBadContentType(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x"))
	req.Header.Set("Content-Type", "application/json")
	err := decodeProtobuf(req, &controlplanev1.ConfigRequest{})
	assert.Error(t, err)
}

func TestDecodeProtobuf_RejectsEmptyBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("Content-Type", contentTypeProtobuf)
	err := decodeProtobuf(req, &controlplanev1.ConfigRequest{})
	assert.Error(t, err)
}

// ═══════════════════════════════════════════════════════════════════════
// grpc_auth.go — isOTLPMethod
// ═══════════════════════════════════════════════════════════════════════

func TestIsOTLPMethod(t *testing.T) {
	assert.True(t, isOTLPMethod(otlpTracesExportMethod))
	assert.True(t, isOTLPMethod(otlpMetricsExportMethod))
	assert.True(t, isOTLPMethod(otlpLogsExportMethod))
	assert.False(t, isOTLPMethod("/foo.Bar/Baz"))
}

// ═══════════════════════════════════════════════════════════════════════
// otlp_handler.go — pure format helpers
// ═══════════════════════════════════════════════════════════════════════

func TestDataFormatForContentType(t *testing.T) {
	assert.Equal(t, "protobuf", dataFormatForContentType(pbContentType))
	assert.Equal(t, "json", dataFormatForContentType("application/json"))
	assert.Equal(t, "json", dataFormatForContentType(""))
}

// ═══════════════════════════════════════════════════════════════════════
// controlPlaneService — ReportTaskResult / ReportStatus / UploadChunkedResult
//
// These exercise the controlPlane (ControlPlaneV2) dependency. ControlPlane is
// a 20-method interface; fakeCP embeds it and overrides only the 3 methods the
// service calls, so we don't hand-implement the other 17.
// ═══════════════════════════════════════════════════════════════════════

type fakeCP struct {
	controlplaneext.ControlPlane // embeds interface; unoverridden methods panic if called

	reportTaskResultErr     error
	uploadChunkResp         *model.ChunkUploadResponse
	uploadChunkErr          error
	registerOrHeartbeatErr  error

	reportTaskResultCalled  bool
	uploadChunkCalled       bool
	registerOrHeartbeatCalled bool
	lastReportedTaskResult  *model.TaskResult
	lastUploadedChunk       *model.ChunkUpload
	lastHeartbeatAgent      *agentregistry.AgentInfo
}

func (f *fakeCP) ReportTaskResult(_ context.Context, tr *model.TaskResult) error {
	f.reportTaskResultCalled = true
	f.lastReportedTaskResult = tr
	return f.reportTaskResultErr
}

func (f *fakeCP) UploadChunk(_ context.Context, req *model.ChunkUpload) (*model.ChunkUploadResponse, error) {
	f.uploadChunkCalled = true
	f.lastUploadedChunk = req
	return f.uploadChunkResp, f.uploadChunkErr
}

func (f *fakeCP) RegisterOrHeartbeatAgent(_ context.Context, ai *agentregistry.AgentInfo) error {
	f.registerOrHeartbeatCalled = true
	f.lastHeartbeatAgent = ai
	return f.registerOrHeartbeatErr
}

func newSvcWithCP(cp controlplaneext.ControlPlane) *controlPlaneService {
	return newControlPlaneService(zap.NewNop(), cp, nil)
}

// ── ReportTaskResult ───────────────────────────────────────────────────

func TestService_ReportTaskResult_NoControlPlane_Unavailable(t *testing.T) {
	svc := newControlPlaneService(zap.NewNop(), nil, nil)
	resp := svc.ReportTaskResult(context.Background(), &controlplanev1.TaskResultRequest{})
	assert.Equal(t, controlplanev1.ResponseStatus_CODE_UNAVAILABLE, resp.Status.Code)
	assert.False(t, resp.Acknowledged)
}

func TestService_ReportTaskResult_MissingTaskID_Invalid(t *testing.T) {
	svc := newSvcWithCP(&fakeCP{})
	// result nil → invalid
	resp := svc.ReportTaskResult(context.Background(), &controlplanev1.TaskResultRequest{})
	assert.Equal(t, controlplanev1.ResponseStatus_CODE_INVALID_ARGUMENT, resp.Status.Code)

	// result present but empty task_id → invalid
	resp = svc.ReportTaskResult(context.Background(), &controlplanev1.TaskResultRequest{
		Result: &controlplanev1.TaskResult{},
	})
	assert.Equal(t, controlplanev1.ResponseStatus_CODE_INVALID_ARGUMENT, resp.Status.Code)
}

func TestService_ReportTaskResult_OK_Acknowledged(t *testing.T) {
	fp := &fakeCP{}
	svc := newSvcWithCP(fp)

	resp := svc.ReportTaskResult(context.Background(), &controlplanev1.TaskResultRequest{
		AgentId: "agent-1",
		Result: &controlplanev1.TaskResult{
			TaskId:        "task-1",
			Status:        controlplanev1.TaskStatus_TASK_STATUS_SUCCESS,
			CompletedAtMillis: 12345,
		},
	})
	assert.Equal(t, controlplanev1.ResponseStatus_CODE_OK, resp.Status.Code)
	assert.True(t, resp.Acknowledged)

	// Verify the proto→model conversion reached the control plane with the
	// right taskID and agentID propagated from the request envelope.
	assert.True(t, fp.reportTaskResultCalled)
	require.NotNil(t, fp.lastReportedTaskResult)
	assert.Equal(t, "task-1", fp.lastReportedTaskResult.TaskID)
	assert.Equal(t, "agent-1", fp.lastReportedTaskResult.AgentID)
	assert.Equal(t, int64(12345), fp.lastReportedTaskResult.CompletedAtMillis)
}

func TestService_ReportTaskResult_FillsCompletedAtWhenZero(t *testing.T) {
	fp := &fakeCP{}
	svc := newSvcWithCP(fp)
	svc.ReportTaskResult(context.Background(), &controlplanev1.TaskResultRequest{
		Result: &controlplanev1.TaskResult{TaskId: "t", Status: controlplanev1.TaskStatus_TASK_STATUS_SUCCESS},
	})
	require.NotNil(t, fp.lastReportedTaskResult)
	assert.NotZero(t, fp.lastReportedTaskResult.CompletedAtMillis, "zero CompletedAtMillis must be stamped with now")
}

func TestService_ReportTaskResult_ControlPlaneError_Classified(t *testing.T) {
	svc := newSvcWithCP(&fakeCP{reportTaskResultErr: errors.New("task not found")})
	resp := svc.ReportTaskResult(context.Background(), &controlplanev1.TaskResultRequest{
		Result: &controlplanev1.TaskResult{TaskId: "t", Status: controlplanev1.TaskStatus_TASK_STATUS_SUCCESS},
	})
	assert.False(t, resp.Acknowledged)
	assert.Contains(t, resp.Status.Message, "not found")
}

// ── ReportStatus ───────────────────────────────────────────────────────

func TestService_ReportStatus_NoControlPlane_Unavailable(t *testing.T) {
	svc := newControlPlaneService(zap.NewNop(), nil, nil)
	resp := svc.ReportStatus(context.Background(), &controlplanev1.StatusRequest{})
	assert.Equal(t, controlplanev1.ResponseStatus_CODE_UNAVAILABLE, resp.Status.Code)
}

func TestService_ReportStatus_OK_HeartbeatsAgent(t *testing.T) {
	fp := &fakeCP{}
	svc := newSvcWithFP(fp)

	resp := svc.ReportStatus(context.Background(), &controlplanev1.StatusRequest{
		AgentIdentity: &controlplanev1.AgentIdentity{
			AgentId:     "agent-1",
			ServiceName: "svc",
			HostName:    "host-1",
		},
	})
	assert.Equal(t, controlplanev1.ResponseStatus_CODE_OK, resp.Status.Code)
	assert.NotZero(t, resp.ServerTimeMillis)

	assert.True(t, fp.registerOrHeartbeatCalled)
	require.NotNil(t, fp.lastHeartbeatAgent)
	assert.Equal(t, "agent-1", fp.lastHeartbeatAgent.AgentID)
	assert.Equal(t, "svc", fp.lastHeartbeatAgent.ServiceName)
	assert.Equal(t, "host-1", fp.lastHeartbeatAgent.Hostname)
}

func TestService_ReportStatus_NoAgentID_StillOK_NoHeartbeat(t *testing.T) {
	fp := &fakeCP{}
	svc := newSvcWithFP(fp)
	// No AgentIdentity → no agentID → no heartbeat call, but still OK.
	resp := svc.ReportStatus(context.Background(), &controlplanev1.StatusRequest{})
	assert.Equal(t, controlplanev1.ResponseStatus_CODE_OK, resp.Status.Code)
	assert.False(t, fp.registerOrHeartbeatCalled)
}

func TestService_ReportStatus_HeartbeatError_StillOK(t *testing.T) {
	// RegisterOrHeartbeatAgent failure is swallowed (logged); status stays OK.
	fp := &fakeCP{registerOrHeartbeatErr: errors.New("registry down")}
	svc := newSvcWithFP(fp)
	resp := svc.ReportStatus(context.Background(), &controlplanev1.StatusRequest{
		AgentIdentity: &controlplanev1.AgentIdentity{AgentId: "a1"},
	})
	assert.Equal(t, controlplanev1.ResponseStatus_CODE_OK, resp.Status.Code)
	assert.True(t, fp.registerOrHeartbeatCalled)
}

// ── UploadChunkedResult ────────────────────────────────────────────────

func TestService_UploadChunkedResult_NoControlPlane_Failed(t *testing.T) {
	svc := newControlPlaneService(zap.NewNop(), nil, nil)
	resp := svc.UploadChunkedResult(context.Background(), &controlplanev1.ChunkedTaskResult{})
	assert.Equal(t, controlplanev1.ChunkUploadStatus_CHUNK_UPLOAD_STATUS_UPLOAD_FAILED, resp.Status)
}

func TestService_UploadChunkedResult_NilRequest_Failed(t *testing.T) {
	// ChunkUploadFromProto(nil) → nil → "invalid chunk request".
	svc := newSvcWithCP(&fakeCP{})
	resp := svc.UploadChunkedResult(context.Background(), nil)
	assert.Equal(t, controlplanev1.ChunkUploadStatus_CHUNK_UPLOAD_STATUS_UPLOAD_FAILED, resp.Status)
}

func TestService_UploadChunkedResult_ControlPlaneError_Failed(t *testing.T) {
	svc := newSvcWithCP(&fakeCP{uploadChunkErr: errors.New("redis unavailable")})
	resp := svc.UploadChunkedResult(context.Background(), &controlplanev1.ChunkedTaskResult{TaskId: "t"})
	assert.Equal(t, controlplanev1.ChunkUploadStatus_CHUNK_UPLOAD_STATUS_UPLOAD_FAILED, resp.Status)
	assert.Contains(t, resp.ErrorMessage, "unavailable")
}

func TestService_UploadChunkedResult_NilResponse_Failed(t *testing.T) {
	svc := newSvcWithCP(&fakeCP{uploadChunkResp: nil})
	resp := svc.UploadChunkedResult(context.Background(), &controlplanev1.ChunkedTaskResult{TaskId: "t"})
	assert.Equal(t, controlplanev1.ChunkUploadStatus_CHUNK_UPLOAD_STATUS_UPLOAD_FAILED, resp.Status)
}

func TestService_UploadChunkedResult_OK_EchoesMissingFields(t *testing.T) {
	fp := &fakeCP{uploadChunkResp: &model.ChunkUploadResponse{
		Status: model.ChunkUploadStatusChunkReceived,
		// UploadID and ReceivedChunkIndex left zero → service must fill from request.
	}}
	svc := newSvcWithCP(fp)

	resp := svc.UploadChunkedResult(context.Background(), &controlplanev1.ChunkedTaskResult{
		TaskId:     "task-1",
		UploadId:   "up-1",
		ChunkIndex: 7,
	})
	assert.Equal(t, controlplanev1.ChunkUploadStatus_CHUNK_UPLOAD_STATUS_CHUNK_RECEIVED, resp.Status)
	assert.True(t, fp.uploadChunkCalled)

	// Empty UploadID echoed from req.UploadId (or TaskId fallback); zero index echoed from req.ChunkIndex.
	assert.Equal(t, "up-1", resp.UploadId, "empty UploadID must be filled from request")
	assert.Equal(t, int32(7), resp.ReceivedChunkIndex, "zero ReceivedChunkIndex must echo req.ChunkIndex")
}

func TestService_UploadChunkedResult_OK_PreservesResponseFields(t *testing.T) {
	fp := &fakeCP{uploadChunkResp: &model.ChunkUploadResponse{
		UploadID:           "kept-up",
		ReceivedChunkIndex: 9,
		Status:             model.ChunkUploadStatusUploadComplete,
	}}
	svc := newSvcWithCP(fp)
	resp := svc.UploadChunkedResult(context.Background(), &controlplanev1.ChunkedTaskResult{
		TaskId: "task-1", UploadId: "ignored", ChunkIndex: 1,
	})
	// Non-empty response fields are preserved (not overwritten by request).
	assert.Equal(t, "kept-up", resp.UploadId)
	assert.Equal(t, int32(9), resp.ReceivedChunkIndex)
	assert.Equal(t, controlplanev1.ChunkUploadStatus_CHUNK_UPLOAD_STATUS_UPLOAD_COMPLETE, resp.Status)
}

// newSvcWithFP wires a fakeCP-backed service. (Named separately from newSvcWithCP
// to keep the ReportStatus tests readable; both are equivalent.)
func newSvcWithFP(fp *fakeCP) *controlPlaneService {
	return newControlPlaneService(zap.NewNop(), fp, nil)
}
