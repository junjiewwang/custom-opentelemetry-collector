// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newTestRedisRuleClient(t *testing.T) *redis.Client {
	t.Helper()

	_, err := exec.LookPath("redis-server")
	if err != nil {
		t.Skip("redis-server not found in PATH, skipping Redis integration test")
	}

	port := reserveLocalRuleStorePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	var serverOutput bytes.Buffer
	cmd := exec.Command(
		"redis-server",
		"--save", "",
		"--appendonly", "no",
		"--bind", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--dir", t.TempDir(),
		"--loglevel", "warning",
	)
	cmd.Stdout = &serverOutput
	cmd.Stderr = &serverOutput
	require.NoError(t, cmd.Start())

	t.Cleanup(func() {
		if cmd.Process == nil {
			return
		}
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() {
		_ = client.Close()
	})

	require.Eventually(t, func() bool {
		return client.Ping(context.Background()).Err() == nil
	}, 5*time.Second, 50*time.Millisecond, "redis-server did not start successfully: %s", serverOutput.String())

	return client
}

func reserveLocalRuleStorePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()
	addr, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)
	return addr.Port
}

func sanitizeRuleStoreKeyPart(name string) string {
	replacer := strings.NewReplacer("/", "-", " ", "-", ":", "-")
	return replacer.Replace(name)
}

func newTestRedisRuleStore(t *testing.T) (*RedisRuleStore, *redis.Client, string) {
	t.Helper()
	client := newTestRedisRuleClient(t)
	prefix := fmt.Sprintf("otel:test:instrumentation:%s", sanitizeRuleStoreKeyPart(t.Name()))
	store := NewRedisRuleStore(zap.NewNop(), client, prefix)
	require.NoError(t, store.Start(context.Background()))
	t.Cleanup(func() {
		_ = store.Close()
	})
	return store, client, prefix
}

func newTestRule(ruleID string) *Rule {
	now := time.Now().UnixMilli()
	return &Rule{
		ID:              ruleID,
		Name:            "trace demo.OrderService.submit",
		AppID:           "app-a",
		ServiceName:     "svc-a",
		ScopeType:       ScopeTypeService,
		ClassName:       "demo.OrderService",
		MethodName:      "submit",
		InstrumentType:  InstrumentTypeTrace,
		DesiredState:    RuleDesiredStateActive,
		CreatedAtMillis: now,
		UpdatedAtMillis: now,
		Summary:         RuleSummary{Status: OperationStatusPending},
		RecentAudits: []*RuleAuditEntry{{
			AuditID:         "audit-1",
			Source:          AuditSourceManual,
			Action:          AuditActionApply,
			Status:          AuditStatusSuccess,
			AgentID:         "agent-1",
			CreatedAtMillis: now,
		}},
	}
}

func newTestTargets(ruleID string) []*RuleTargetStatus {
	now := time.Now().UnixMilli()
	return []*RuleTargetStatus{
		{
			RuleID:               ruleID,
			AgentID:              "agent-1",
			Hostname:             "host-1",
			IP:                   "127.0.0.1",
			DesiredState:         RuleDesiredStateActive,
			State:                TargetStateApplied,
			TaskID:               "task-1",
			TaskType:             "dynamic_instrument",
			TaskStatus:           "success",
			LastDispatchAtMillis: now,
			UpdatedAtMillis:      now,
		},
	}
}

func TestRedisRuleStore_PersistsAcrossRestartAndMultiInstance(t *testing.T) {
	storeA, client, prefix := newTestRedisRuleStore(t)
	ctx := context.Background()

	rule := newTestRule("rule-1")
	require.NoError(t, storeA.SaveRule(ctx, rule, true))
	require.NoError(t, storeA.SaveTargetStatuses(ctx, rule.ID, newTestTargets(rule.ID)))

	storeB := NewRedisRuleStore(zap.NewNop(), client, prefix)
	require.NoError(t, storeB.Start(ctx))
	defer func() { _ = storeB.Close() }()

	loadedRule, err := storeB.GetRule(ctx, rule.ID)
	require.NoError(t, err)
	assert.Equal(t, rule.ID, loadedRule.ID)
	assert.Equal(t, rule.ServiceName, loadedRule.ServiceName)
	require.Len(t, loadedRule.RecentAudits, 1)
	assert.Equal(t, AuditActionApply, loadedRule.RecentAudits[0].Action)

	loadedTargets, err := storeB.ListTargetStatuses(ctx, rule.ID)
	require.NoError(t, err)
	require.Len(t, loadedTargets, 1)
	assert.Equal(t, "agent-1", loadedTargets[0].AgentID)

	loadedRule.Description = "updated by second instance"
	loadedRule.UpdatedAtMillis = time.Now().Add(time.Second).UnixMilli()
	require.NoError(t, storeB.SaveRule(ctx, loadedRule, false))

	reloadedRule, err := storeA.GetRule(ctx, rule.ID)
	require.NoError(t, err)
	assert.Equal(t, "updated by second instance", reloadedRule.Description)

	require.NoError(t, storeA.Close())
	storeRestarted := NewRedisRuleStore(zap.NewNop(), client, prefix)
	require.NoError(t, storeRestarted.Start(ctx))
	defer func() { _ = storeRestarted.Close() }()

	restartedRule, err := storeRestarted.GetRule(ctx, rule.ID)
	require.NoError(t, err)
	assert.Equal(t, "updated by second instance", restartedRule.Description)

	restartedTargets, err := storeRestarted.ListTargetStatuses(ctx, rule.ID)
	require.NoError(t, err)
	require.Len(t, restartedTargets, 1)
	assert.Equal(t, TargetStateApplied, restartedTargets[0].State)
}

func TestRedisRuleStore_StartupValidationCleansInvalidData(t *testing.T) {
	client := newTestRedisRuleClient(t)
	prefix := fmt.Sprintf("otel:test:instrumentation:%s", sanitizeRuleStoreKeyPart(t.Name()))
	ctx := context.Background()

	require.NoError(t, client.HSet(ctx, fmt.Sprintf(keyRulesHash, prefix), "broken-rule", "{not-json").Err())
	require.NoError(t, client.Set(ctx, fmt.Sprintf(keyTargetsJSON, prefix, "broken-rule"), "[]", 0).Err())
	require.NoError(t, client.Set(ctx, fmt.Sprintf(keyTargetsJSON, prefix, "orphan-rule"), "[]", 0).Err())

	store := NewRedisRuleStore(zap.NewNop(), client, prefix)
	require.NoError(t, store.Start(ctx))
	defer func() { _ = store.Close() }()

	exists, err := client.HExists(ctx, fmt.Sprintf(keyRulesHash, prefix), "broken-rule").Result()
	require.NoError(t, err)
	assert.False(t, exists)

	orphanExists, err := client.Exists(ctx, fmt.Sprintf(keyTargetsJSON, prefix, "orphan-rule")).Result()
	require.NoError(t, err)
	assert.EqualValues(t, 0, orphanExists)

	brokenTargetsExists, err := client.Exists(ctx, fmt.Sprintf(keyTargetsJSON, prefix, "broken-rule")).Result()
	require.NoError(t, err)
	assert.EqualValues(t, 0, brokenTargetsExists)
}

func TestRedisRuleStore_ConcurrentCreateSameRule(t *testing.T) {
	store, _, _ := newTestRedisRuleStore(t)
	ctx := context.Background()

	const concurrency = 32
	var (
		wg            sync.WaitGroup
		createdCount  atomic.Int32
		conflictCount atomic.Int32
	)

	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func(idx int) {
			defer wg.Done()
			rule := newTestRule("rule-concurrent")
			rule.Name = fmt.Sprintf("trace-%d", idx)
			err := store.SaveRule(ctx, rule, true)
			if err == nil {
				createdCount.Add(1)
				return
			}
			if strings.Contains(err.Error(), "already exists") {
				conflictCount.Add(1)
				return
			}
			assert.NoError(t, err)
		}(i)
	}
	wg.Wait()

	assert.Equal(t, int32(1), createdCount.Load())
	assert.Equal(t, int32(concurrency-1), conflictCount.Load())

	rules, err := store.ListRules(ctx, ListRulesQuery{IncludeDeleted: true})
	require.NoError(t, err)
	require.Len(t, rules, 1)
	assert.Equal(t, "rule-concurrent", rules[0].ID)
}
