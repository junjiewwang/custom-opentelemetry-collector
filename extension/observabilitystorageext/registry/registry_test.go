// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

type fakeFactory struct{ name string }

func (f fakeFactory) Name() string { return f.name }
func (f fakeFactory) Create(any, FactoryContext) (LifecycleProvider, error) {
	return fakeProvider{}, nil
}

type fakeProvider struct{}

func (fakeProvider) Name() string { return "fake" }
func (fakeProvider) Start(context.Context) error { return nil }
func (fakeProvider) Shutdown(context.Context) error { return nil }
func (fakeProvider) HealthCheck(context.Context) (bool, string, map[string]any) { return true, "ok", nil }
func (fakeProvider) WriteSpans(context.Context, []storedmodel.StoredSpan) error { return nil }
func (fakeProvider) WriteTraces(context.Context, ptrace.Traces) error { return nil }
func (fakeProvider) WriteMetrics(context.Context, pmetric.Metrics) error { return nil }
func (fakeProvider) WriteLogs(context.Context, plog.Logs) error { return nil }
func (fakeProvider) FlushTraces(context.Context) error { return nil }
func (fakeProvider) FlushMetrics(context.Context) error { return nil }
func (fakeProvider) FlushLogs(context.Context) error { return nil }

func TestRegisterAndGet(t *testing.T) {
	Register(fakeFactory{name: "test-backend"})
	f, err := Get("test-backend")
	require.NoError(t, err)
	assert.Equal(t, "test-backend", f.Name())

	_, err = Get("nonexistent")
	assert.ErrorContains(t, err, "unsupported provider type")
}

func TestRegister_DuplicatePanics(t *testing.T) {
	assert.Panics(t, func() {
		Register(fakeFactory{name: "dup-backend"})
		Register(fakeFactory{name: "dup-backend"})
	})
}
