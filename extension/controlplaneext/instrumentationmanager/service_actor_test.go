// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubCapabilityProvider struct {
	allowed bool
	err     error
	calls   []string
}

func (p *stubCapabilityProvider) IsInstrumentationAllowed(_ context.Context, appID string) (bool, error) {
	p.calls = append(p.calls, appID)
	return p.allowed, p.err
}

func testActorContext() context.Context {
	return WithActor(context.Background(), Actor{
		ID:       "mcp:key-abcdef12",
		Type:     ActorTypeMCP,
		TenantID: "tenant-a",
	})
}

func testCreateRequest() *CreateRuleRequest {
	return &CreateRuleRequest{
		AppID:          "app-a",
		ServiceName:    "svc-a",
		ClassName:      "com.example.OrderService",
		MethodName:     "createOrder",
		InstrumentType: InstrumentTypeTrace,
		CreatedBy:      "forged",
	}
}

func testUpdateRequest() *UpdateRuleRequest {
	return &UpdateRuleRequest{
		ClassName:      "com.example.OrderService",
		MethodName:     "createOrder",
		InstrumentType: InstrumentTypeTrace,
		UpdatedBy:      "forged",
	}
}

func TestCreateRuleUsesContextActor(t *testing.T) {
	svc, agentReg, _ := newTestInstrumentationService(t)
	registerTestAgent(t, agentReg, "a1")

	rule, err := svc.CreateRule(testActorContext(), testCreateRequest())
	require.NoError(t, err)
	assert.Equal(t, "mcp:key-abcdef12", rule.CreatedBy)
	assert.Equal(t, "mcp:key-abcdef12", rule.UpdatedBy)
	require.NotEmpty(t, rule.RecentAudits)
	assert.Equal(t, "mcp:key-abcdef12", rule.RecentAudits[0].Operator)
}

func TestWriteMethodsUseContextActor(t *testing.T) {
	svc, agentReg, _ := newTestInstrumentationService(t)
	registerTestAgent(t, agentReg, "a1")
	ctx := testActorContext()

	rule, err := svc.CreateRule(ctx, testCreateRequest())
	require.NoError(t, err)

	updated, err := svc.UpdateRule(ctx, rule.ID, testUpdateRequest())
	require.NoError(t, err)
	assert.Equal(t, "mcp:key-abcdef12", updated.UpdatedBy)

	paused, err := svc.PauseRule(ctx, rule.ID)
	require.NoError(t, err)
	assert.Equal(t, "mcp:key-abcdef12", paused.UpdatedBy)

	resumed, err := svc.ResumeRule(ctx, rule.ID)
	require.NoError(t, err)
	assert.Equal(t, "mcp:key-abcdef12", resumed.UpdatedBy)

	deleted, err := svc.DeleteRule(ctx, rule.ID)
	require.NoError(t, err)
	assert.Equal(t, "mcp:key-abcdef12", deleted.UpdatedBy)
}

func TestWriteMethodsFallbackWithoutActor(t *testing.T) {
	svc, _, _ := newTestInstrumentationService(t)
	ctx := context.Background()

	rule, err := svc.CreateRule(ctx, testCreateRequest())
	require.NoError(t, err)
	assert.Equal(t, "forged", rule.CreatedBy)
	assert.Equal(t, "forged", rule.UpdatedBy)

	updated, err := svc.UpdateRule(ctx, rule.ID, testUpdateRequest())
	require.NoError(t, err)
	assert.Equal(t, "forged", updated.UpdatedBy)

	// Pause/Resume/Delete have no request body: without an actor the existing
	// value must be preserved rather than cleared.
	paused, err := svc.PauseRule(ctx, rule.ID)
	require.NoError(t, err)
	assert.Equal(t, "forged", paused.UpdatedBy)

	deleted, err := svc.DeleteRule(ctx, rule.ID)
	require.NoError(t, err)
	assert.Equal(t, "forged", deleted.UpdatedBy)
}

func TestCapabilityProviderCheckpoints(t *testing.T) {
	t.Run("denied create leaves no partial rule", func(t *testing.T) {
		svc, _, _ := newTestInstrumentationService(t)
		provider := &stubCapabilityProvider{allowed: false}
		WithCapabilityProvider(provider)(svc)

		_, err := svc.CreateRule(context.Background(), testCreateRequest())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "disabled by tenant capability")
		assert.Equal(t, []string{"app-a"}, provider.calls)

		rules, err := svc.ListRules(context.Background(), ListRulesQuery{})
		require.NoError(t, err)
		assert.Empty(t, rules)
	})

	t.Run("denied blocks update and resume but allows cleanup", func(t *testing.T) {
		svc, _, _ := newTestInstrumentationService(t)
		rule, err := svc.CreateRule(context.Background(), testCreateRequest())
		require.NoError(t, err)

		provider := &stubCapabilityProvider{allowed: false}
		WithCapabilityProvider(provider)(svc)

		_, err = svc.UpdateRule(context.Background(), rule.ID, testUpdateRequest())
		require.Error(t, err)
		_, err = svc.ResumeRule(context.Background(), rule.ID)
		require.Error(t, err)

		_, err = svc.PauseRule(context.Background(), rule.ID)
		require.NoError(t, err)
		_, err = svc.DeleteRule(context.Background(), rule.ID)
		require.NoError(t, err)
	})

	t.Run("provider error fails closed", func(t *testing.T) {
		svc, _, _ := newTestInstrumentationService(t)
		WithCapabilityProvider(&stubCapabilityProvider{err: errors.New("backend unavailable")})(svc)

		_, err := svc.CreateRule(context.Background(), testCreateRequest())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "backend unavailable")
	})

	t.Run("allowed provider and nil provider keep behavior", func(t *testing.T) {
		svc, _, _ := newTestInstrumentationService(t)
		provider := &stubCapabilityProvider{allowed: true}
		WithCapabilityProvider(provider)(svc)

		_, err := svc.CreateRule(context.Background(), testCreateRequest())
		require.NoError(t, err)
		assert.Equal(t, []string{"app-a"}, provider.calls)

		WithCapabilityProvider(nil)(svc)
		_, err = svc.CreateRule(context.Background(), testCreateRequest())
		require.NoError(t, err)
		assert.Len(t, provider.calls, 1, "nil provider must not be consulted")
	})
}
