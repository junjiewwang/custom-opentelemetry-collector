// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package appmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

// ═══════════════════════════════════════════════════
// Sequential generators for deterministic multi-entity tests
// ═══════════════════════════════════════════════════

// sequentialGen generates incrementing IDs/Tokens with a prefix.
// Enables deterministic tests that create multiple entities.
type sequentialGen struct {
	prefix string
	n      int
}

func (g *sequentialGen) Generate() (string, error) {
	g.n++
	return fmt.Sprintf("%s-%03d", g.prefix, g.n), nil
}

// newTestAppService creates a fresh AppService with a MemoryAppRepository
// and sequential ID/Token generators. Each test function gets its own instance
// and sequential generators ensure unique IDs/tokens across multiple creates.
func newTestAppService(t *testing.T) *AppService {
	t.Helper()
	return NewAppService(
		NewMemoryAppRepository(),
		&sequentialGen{prefix: "app"},
		&sequentialGen{prefix: "tok"},
		DefaultRetentionLimits(),
		nil, // no seed apps in tests
		zaptest.NewLogger(t),
	)
}

// createTestApp is a convenience helper for tests.
func createTestApp(t *testing.T, svc *AppService, name string) *AppInfo {
	t.Helper()
	app, err := svc.CreateApp(context.Background(), &CreateAppRequest{Name: name})
	require.NoError(t, err)
	return app
}

// ═══════════════════════════════════════════════════
// CreateApp tests
// ═══════════════════════════════════════════════════

func TestAppService_CreateApp(t *testing.T) {
	svc := newTestAppService(t)
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		app, err := svc.CreateApp(ctx, &CreateAppRequest{
			Name:        "my-app",
			Description: "A test app",
			Metadata:    map[string]string{"env": "test"},
		})
		require.NoError(t, err)
		assert.NotEmpty(t, app.ID)
		assert.Contains(t, app.ID, "app-")
		assert.Equal(t, "my-app", app.Name)
		assert.NotEmpty(t, app.Token)
		assert.Equal(t, "A test app", app.Description)
		assert.Equal(t, "active", app.Status)
		assert.Equal(t, "test", app.Metadata["env"])
		assert.False(t, app.CreatedAt.IsZero())
		assert.True(t, app.Retention.IsZero())
	})

	t.Run("duplicate_name", func(t *testing.T) {
		createTestApp(t, svc, "unique-app")
		_, err := svc.CreateApp(ctx, &CreateAppRequest{Name: "unique-app"})
		assert.ErrorIs(t, err, ErrAppNameExists)
	})

	t.Run("empty_name", func(t *testing.T) {
		_, err := svc.CreateApp(ctx, &CreateAppRequest{Name: ""})
		assert.Error(t, err)
	})

	t.Run("custom_token", func(t *testing.T) {
		app, err := svc.CreateApp(ctx, &CreateAppRequest{
			Name:  "custom-token-app",
			Token: "my-custom-token",
		})
		require.NoError(t, err)
		assert.Equal(t, "my-custom-token", app.Token)
	})

	t.Run("duplicate_custom_token", func(t *testing.T) {
		createTestApp(t, svc, "app-ct-1")
		// tok-001 was generated for app-ct-1; try to reuse it
		_, err := svc.CreateApp(ctx, &CreateAppRequest{
			Name:  "app-ct-2",
			Token: "tok-001",
		})
		assert.ErrorIs(t, err, ErrTokenExists)
	})

	t.Run("nil_request", func(t *testing.T) {
		_, err := svc.CreateApp(ctx, nil)
		assert.Error(t, err)
	})
}

// ═══════════════════════════════════════════════════
// GetApp tests
// ═══════════════════════════════════════════════════

func TestAppService_GetApp(t *testing.T) {
	svc := newTestAppService(t)
	ctx := context.Background()

	created := createTestApp(t, svc, "get-test")

	t.Run("found", func(t *testing.T) {
		app, err := svc.GetApp(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, "get-test", app.Name)
	})

	t.Run("not_found", func(t *testing.T) {
		_, err := svc.GetApp(ctx, "no-such-id")
		assert.ErrorIs(t, err, ErrNotFound)
	})
}

// ═══════════════════════════════════════════════════
// UpdateApp tests
// ═══════════════════════════════════════════════════

func TestAppService_UpdateApp(t *testing.T) {
	svc := newTestAppService(t)
	ctx := context.Background()

	created := createTestApp(t, svc, "update-test")

	t.Run("rename", func(t *testing.T) {
		updated, err := svc.UpdateApp(ctx, created.ID, &UpdateAppRequest{Name: "new-name"})
		require.NoError(t, err)
		assert.Equal(t, "new-name", updated.Name)
	})

	t.Run("rename_conflict", func(t *testing.T) {
		// Create a second app with a different name
		createTestApp(t, svc, "other-app")
		_, err := svc.UpdateApp(ctx, created.ID, &UpdateAppRequest{Name: "other-app"})
		assert.ErrorIs(t, err, ErrAppNameExists)
	})

	t.Run("status_change", func(t *testing.T) {
		updated, err := svc.UpdateApp(ctx, created.ID, &UpdateAppRequest{Status: "disabled"})
		require.NoError(t, err)
		assert.Equal(t, "disabled", updated.Status)
	})

	t.Run("invalid_status", func(t *testing.T) {
		_, err := svc.UpdateApp(ctx, created.ID, &UpdateAppRequest{Status: "deleted"})
		assert.ErrorIs(t, err, ErrInvalidStatus)
	})

	t.Run("not_found", func(t *testing.T) {
		_, err := svc.UpdateApp(ctx, "no-such-id", &UpdateAppRequest{Name: "x"})
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("nil_request", func(t *testing.T) {
		_, err := svc.UpdateApp(ctx, created.ID, nil)
		assert.Error(t, err)
	})
}

// ═══════════════════════════════════════════════════
// DeleteApp tests
// ═══════════════════════════════════════════════════

func TestAppService_DeleteApp(t *testing.T) {
	svc := newTestAppService(t)
	ctx := context.Background()

	created := createTestApp(t, svc, "delete-test")

	t.Run("success", func(t *testing.T) {
		err := svc.DeleteApp(ctx, created.ID)
		require.NoError(t, err)

		_, err = svc.GetApp(ctx, created.ID)
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("not_found", func(t *testing.T) {
		err := svc.DeleteApp(ctx, "no-such-id")
		assert.ErrorIs(t, err, ErrNotFound)
	})
}

// ═══════════════════════════════════════════════════
// ListApps tests
// ═══════════════════════════════════════════════════

func TestAppService_ListApps(t *testing.T) {
	svc := newTestAppService(t)
	ctx := context.Background()

	t.Run("empty", func(t *testing.T) {
		apps, err := svc.ListApps(ctx)
		require.NoError(t, err)
		assert.Empty(t, apps)
	})

	t.Run("with_apps", func(t *testing.T) {
		createTestApp(t, svc, "app-a")
		createTestApp(t, svc, "app-b")

		apps, err := svc.ListApps(ctx)
		require.NoError(t, err)
		assert.Len(t, apps, 2)
	})
}

// ═══════════════════════════════════════════════════
// RegenerateToken tests
// ═══════════════════════════════════════════════════

func TestAppService_RegenerateToken(t *testing.T) {
	svc := newTestAppService(t)
	ctx := context.Background()

	created := createTestApp(t, svc, "regen-test")
	oldToken := created.Token
	assert.Equal(t, "tok-001", oldToken)

	t.Run("new_token_differs", func(t *testing.T) {
		updated, err := svc.RegenerateToken(ctx, created.ID)
		require.NoError(t, err)
		assert.NotEqual(t, oldToken, updated.Token)
		// Sequential generator → next is tok-002
		assert.Equal(t, "tok-002", updated.Token)
	})

	t.Run("old_token_is_invalid", func(t *testing.T) {
		result, err := svc.ValidateToken(ctx, oldToken)
		require.NoError(t, err)
		assert.False(t, result.Valid, "old token should be invalid after regeneration")
	})

	t.Run("not_found", func(t *testing.T) {
		_, err := svc.RegenerateToken(ctx, "no-such-id")
		assert.ErrorIs(t, err, ErrNotFound)
	})
}

// ═══════════════════════════════════════════════════
// SetToken tests
// ═══════════════════════════════════════════════════

func TestAppService_SetToken(t *testing.T) {
	ctx := context.Background()

	t.Run("set_custom_token", func(t *testing.T) {
		svc := newTestAppService(t)
		created := createTestApp(t, svc, "set-token-test")

		app, err := svc.SetToken(ctx, created.ID, &SetTokenRequest{Token: "brand-new-token"})
		require.NoError(t, err)
		assert.Equal(t, "brand-new-token", app.Token)
	})

	t.Run("token_conflict_with_other_app", func(t *testing.T) {
		svc := newTestAppService(t)
		appA := createTestApp(t, svc, "app-a")   // tok-001
		createTestApp(t, svc, "app-b")            // tok-002

		// appA tries to claim appB's token
		_, err := svc.SetToken(ctx, appA.ID, &SetTokenRequest{Token: "tok-002"})
		var conflict *ErrTokenConflict
		require.True(t, errors.As(err, &conflict))
		assert.Contains(t, conflict.Error(), "token already in use")
	})

	t.Run("not_found", func(t *testing.T) {
		svc := newTestAppService(t)
		_, err := svc.SetToken(ctx, "no-such-id", &SetTokenRequest{Token: "new-tok"})
		assert.ErrorIs(t, err, ErrNotFound)
	})
}

// ═══════════════════════════════════════════════════
// ValidateToken tests
// ═══════════════════════════════════════════════════

func TestAppService_ValidateToken(t *testing.T) {
	svc := newTestAppService(t)
	ctx := context.Background()

	created := createTestApp(t, svc, "validate-test")

	t.Run("valid", func(t *testing.T) {
		result, err := svc.ValidateToken(ctx, created.Token)
		require.NoError(t, err)
		assert.True(t, result.Valid)
		assert.Equal(t, created.ID, result.AppID)
		assert.Equal(t, "validate-test", result.AppName)
	})

	t.Run("empty_token", func(t *testing.T) {
		result, err := svc.ValidateToken(ctx, "")
		require.NoError(t, err)
		assert.False(t, result.Valid)
		assert.Equal(t, "token is empty", result.Reason)
	})

	t.Run("not_found", func(t *testing.T) {
		result, err := svc.ValidateToken(ctx, "no-such-token")
		require.NoError(t, err)
		assert.False(t, result.Valid)
		assert.Equal(t, "token not found", result.Reason)
	})

	t.Run("disabled_app", func(t *testing.T) {
		disabled := createTestApp(t, svc, "disabled-app") // tok-002
		_, err := svc.UpdateApp(ctx, disabled.ID, &UpdateAppRequest{Status: "disabled"})
		require.NoError(t, err)

		result, err := svc.ValidateToken(ctx, disabled.Token)
		require.NoError(t, err)
		assert.False(t, result.Valid)
		assert.Equal(t, "app is disabled", result.Reason)
	})
}

// ═══════════════════════════════════════════════════
// GetRetention tests
// ═══════════════════════════════════════════════════

func TestAppService_GetRetention(t *testing.T) {
	ctx := context.Background()

	t.Run("zero_policy_for_new_app", func(t *testing.T) {
		svc := newTestAppService(t)
		created := createTestApp(t, svc, "ret-get")

		policy, err := svc.GetRetention(ctx, created.ID)
		require.NoError(t, err)
		assert.True(t, policy.IsZero())
	})

	t.Run("not_found", func(t *testing.T) {
		svc := newTestAppService(t)
		_, err := svc.GetRetention(ctx, "no-such-id")
		assert.ErrorIs(t, err, ErrNotFound)
	})
}

// ═══════════════════════════════════════════════════
// SetRetention tests
// ═══════════════════════════════════════════════════

func TestAppService_SetRetention(t *testing.T) {
	ctx := context.Background()

	t.Run("set_and_readback", func(t *testing.T) {
		svc := newTestAppService(t)
		created := createTestApp(t, svc, "ret-set")

		err := svc.SetRetention(ctx, created.ID, SignalTrace, 30*24*time.Hour)
		require.NoError(t, err)

		policy, err := svc.GetRetention(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, 30*24*time.Hour, policy.Trace)
		assert.Equal(t, time.Duration(0), policy.Metric)
		assert.Equal(t, time.Duration(0), policy.Log)
	})

	t.Run("set_multiple_signals", func(t *testing.T) {
		svc := newTestAppService(t)
		created := createTestApp(t, svc, "ret-multi")

		err := svc.SetRetention(ctx, created.ID, SignalTrace, 14*24*time.Hour)
		require.NoError(t, err)
		err = svc.SetRetention(ctx, created.ID, SignalMetric, 30*24*time.Hour)
		require.NoError(t, err)
		err = svc.SetRetention(ctx, created.ID, SignalLog, 7*24*time.Hour)
		require.NoError(t, err)

		policy, err := svc.GetRetention(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, 14*24*time.Hour, policy.Trace)
		assert.Equal(t, 30*24*time.Hour, policy.Metric)
		assert.Equal(t, 7*24*time.Hour, policy.Log)
	})

	t.Run("out_of_range_below_min", func(t *testing.T) {
		svc := newTestAppService(t)
		created := createTestApp(t, svc, "ret-min")

		err := svc.SetRetention(ctx, created.ID, SignalTrace, 1*time.Hour)
		assert.ErrorIs(t, err, ErrRetentionOutOfRange)
	})

	t.Run("out_of_range_above_max", func(t *testing.T) {
		svc := newTestAppService(t)
		created := createTestApp(t, svc, "ret-max")

		err := svc.SetRetention(ctx, created.ID, SignalTrace, 400*24*time.Hour)
		assert.ErrorIs(t, err, ErrRetentionOutOfRange)
	})

	t.Run("zero_is_valid", func(t *testing.T) {
		svc := newTestAppService(t)
		created := createTestApp(t, svc, "ret-zero")

		err := svc.SetRetention(ctx, created.ID, SignalTrace, 30*24*time.Hour)
		require.NoError(t, err)

		err = svc.SetRetention(ctx, created.ID, SignalTrace, 0)
		require.NoError(t, err)

		policy, err := svc.GetRetention(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, time.Duration(0), policy.Trace)
	})

	t.Run("not_found", func(t *testing.T) {
		svc := newTestAppService(t)
		err := svc.SetRetention(ctx, "no-such-id", SignalTrace, 7*24*time.Hour)
		assert.ErrorIs(t, err, ErrNotFound)
	})
}

// ═══════════════════════════════════════════════════
// DeleteRetention tests
// ═══════════════════════════════════════════════════

func TestAppService_DeleteRetention(t *testing.T) {
	ctx := context.Background()

	t.Run("removes_override", func(t *testing.T) {
		svc := newTestAppService(t)
		created := createTestApp(t, svc, "ret-del")

		err := svc.SetRetention(ctx, created.ID, SignalTrace, 30*24*time.Hour)
		require.NoError(t, err)

		err = svc.DeleteRetention(ctx, created.ID, SignalTrace)
		require.NoError(t, err)

		policy, err := svc.GetRetention(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, time.Duration(0), policy.Trace)
	})

	t.Run("delete_non_existent_is_noop", func(t *testing.T) {
		svc := newTestAppService(t)
		created := createTestApp(t, svc, "ret-nodel")

		err := svc.DeleteRetention(ctx, created.ID, SignalTrace)
		require.NoError(t, err)

		policy, err := svc.GetRetention(ctx, created.ID)
		require.NoError(t, err)
		assert.True(t, policy.IsZero())
	})
}

// ═══════════════════════════════════════════════════
// RetentionPolicy JSON serialization
// ═══════════════════════════════════════════════════

func TestRetentionPolicy_JSON(t *testing.T) {
	t.Run("marshal_non_zero", func(t *testing.T) {
		p := RetentionPolicy{
			Trace:  720 * time.Hour,  // 30d
			Metric: 168 * time.Hour,  // 7d
		}
		data, err := json.Marshal(p)
		require.NoError(t, err)

		jsonStr := string(data)
		assert.Contains(t, jsonStr, `"trace":"720h0m0s"`)
		assert.Contains(t, jsonStr, `"metric":"168h0m0s"`)
		assert.NotContains(t, jsonStr, "86400000000000") // not nanosecond int
	})

	t.Run("marshal_zero", func(t *testing.T) {
		p := RetentionPolicy{} // all zero
		data, err := json.Marshal(p)
		require.NoError(t, err)
		assert.Equal(t, "{}", string(data))
	})

	t.Run("roundtrip", func(t *testing.T) {
		original := RetentionPolicy{
			Trace:  30 * 24 * time.Hour,
			Metric: 14 * 24 * time.Hour,
			Log:    7 * 24 * time.Hour,
		}
		data, err := json.Marshal(original)
		require.NoError(t, err)

		var restored RetentionPolicy
		err = json.Unmarshal(data, &restored)
		require.NoError(t, err)
		assert.Equal(t, original, restored)
	})

	t.Run("roundtrip_zero", func(t *testing.T) {
		original := RetentionPolicy{}
		data, err := json.Marshal(original)
		require.NoError(t, err)

		var restored RetentionPolicy
		err = json.Unmarshal(data, &restored)
		require.NoError(t, err)
		assert.True(t, restored.IsZero())
	})

	t.Run("unmarshal_backward_compat", func(t *testing.T) {
		// Old format with nanosecond integers (should still parse via AppInfo unmarshal)
		// But RetentionPolicy JSON won't hit this — it only accepts string format.
		data := []byte(`{"trace":"24h0m0s","metric":"168h0m0s"}`)
		var p RetentionPolicy
		err := json.Unmarshal(data, &p)
		require.NoError(t, err)
		assert.Equal(t, 24*time.Hour, p.Trace)
		assert.Equal(t, 168*time.Hour, p.Metric)
	})
}

// ═══════════════════════════════════════════════════
// Consumer interface compile-time checks
// ═══════════════════════════════════════════════════

func TestConsumerInterfaces(t *testing.T) {
	svc := newTestAppService(t)

	var _ AppManager = svc
	var _ TokenValidator = svc
	var _ AppRetentionProvider = svc
}
