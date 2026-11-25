// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeActorID(t *testing.T) {
	assert.Equal(t, "mcp:key-abcd", normalizeActorID("  mcp:key-abcd  "))
	assert.Equal(t, "abc", normalizeActorID("a\nb\rc"))
	assert.Empty(t, normalizeActorID(" \t\n "))
	assert.Len(t, []rune(normalizeActorID(strings.Repeat("x", 200))), maxActorIDLength)
}

func TestWithActorAndActorFromContext(t *testing.T) {
	_, ok := ActorFromContext(context.Background())
	require.False(t, ok)
	_, ok = ActorFromContext(nil)
	require.False(t, ok)

	ctx := WithActor(context.Background(), Actor{
		ID:       "  mcp:key-1 \n",
		Type:     ActorTypeMCP,
		TenantID: " tenant-a ",
	})
	actor, ok := ActorFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "mcp:key-1", actor.ID)
	assert.Equal(t, ActorTypeMCP, actor.Type)
	assert.Equal(t, "tenant-a", actor.TenantID)

	empty := WithActor(context.Background(), Actor{ID: "  ", Type: ActorTypeMCP})
	_, ok = ActorFromContext(empty)
	require.False(t, ok)
}

func TestRuleActorIDPrefersContext(t *testing.T) {
	ctx := WithActor(context.Background(), Actor{ID: "mcp:key-1", Type: ActorTypeMCP})
	assert.Equal(t, "mcp:key-1", ruleActorID(ctx, "forged"))
	assert.Equal(t, "fallback", ruleActorID(context.Background(), " fallback "))
}

func TestAuditActorID(t *testing.T) {
	assert.Equal(t, SystemActorID, auditActorID(context.Background()))
	ctx := WithActor(context.Background(), Actor{ID: "mcp:key-1", Type: ActorTypeMCP})
	assert.Equal(t, "mcp:key-1", auditActorID(ctx))
}

func TestWithAuditOperator(t *testing.T) {
	audits := []*RuleAuditEntry{
		{AuditID: "a1"},
		nil,
		{AuditID: "a2", Operator: "preset"},
	}
	ctx := WithActor(context.Background(), Actor{ID: "mcp:key-1", Type: ActorTypeMCP})
	out := withAuditOperator(ctx, audits)

	assert.Equal(t, "mcp:key-1", out[0].Operator)
	assert.Nil(t, out[1])
	assert.Equal(t, "preset", out[2].Operator)

	system := withAuditOperator(context.Background(), []*RuleAuditEntry{{}})
	assert.Equal(t, SystemActorID, system[0].Operator)
}

func TestRuleAuditEntryLegacyJSON(t *testing.T) {
	var entry RuleAuditEntry
	require.NoError(t, json.Unmarshal(
		[]byte(`{"audit_id":"a1","source":"manual","action":"apply","status":"success"}`), &entry))
	assert.Equal(t, "a1", entry.AuditID)
	assert.Empty(t, entry.Operator)
}
