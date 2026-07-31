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
