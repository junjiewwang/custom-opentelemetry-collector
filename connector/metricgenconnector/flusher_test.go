// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package metricgenconnector

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/collector/pdata/pcommon"
)

// TestSetResourceAttr_ServiceName verifies the fix for the "unknown" serviceName
// bug: setResourceAttr must populate resource.service.name so the storage layer
// (ConvertOTLPMetric reads it → top-level serviceName) does not fall back to
// "unknown". Without it, spanmetrics metrics get serviceName="unknown" at the
// top level even though labels.service_name is correct.
func TestSetResourceAttr_ServiceName(t *testing.T) {
	t.Run("both app_id and service.name set", func(t *testing.T) {
		res := pcommon.NewResource()
		setResourceAttr(res, "app1", "svc1")
		appID, ok := res.Attributes().Get("app_id")
		assert.True(t, ok)
		assert.Equal(t, "app1", appID.Str())
		svc, ok := res.Attributes().Get("service.name")
		assert.True(t, ok, "service.name must be set on the resource")
		assert.Equal(t, "svc1", svc.Str())
	})

	t.Run("empty service.name omitted (no attribute)", func(t *testing.T) {
		res := pcommon.NewResource()
		setResourceAttr(res, "app1", "")
		_, ok := res.Attributes().Get("service.name")
		assert.False(t, ok, "empty service.name should not be set")
	})

	t.Run("empty app_id omitted", func(t *testing.T) {
		res := pcommon.NewResource()
		setResourceAttr(res, "", "svc1")
		_, ok := res.Attributes().Get("app_id")
		assert.False(t, ok)
		svc, ok := res.Attributes().Get("service.name")
		assert.True(t, ok)
		assert.Equal(t, "svc1", svc.Str())
	})
}

// TestDimensionSet_Lookup verifies the dimensionSet lookup used to extract
// service.name from a RED series' dims during flush.
func TestDimensionSet_Lookup(t *testing.T) {
	ds := newDimensionSet(map[string]string{
		"service.name": "svc1",
		"span.name":    "GET /",
		"span.kind":    "Server",
	})
	v, ok := ds.Lookup("service.name")
	assert.True(t, ok)
	assert.Equal(t, "svc1", v)

	v, ok = ds.Lookup("missing")
	assert.False(t, ok)
	assert.Equal(t, "", v)
}
