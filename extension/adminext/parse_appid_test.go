// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePromQL_AppIDInLabels(t *testing.T) {
	expr, err := parsePromQL(`kafka.consumer.commit_sync_time_ns_total{app_id="CIKmanlvG3sfdnpr"}`)
	require.NoError(t, err)
	t.Logf("MetricName=%q Labels=%v LabelMatch=%v", expr.MetricName, expr.Labels, expr.LabelMatch)
	assert.Equal(t, "kafka.consumer.commit_sync_time_ns_total", expr.MetricName)
	assert.Equal(t, "CIKmanlvG3sfdnpr", expr.Labels["app_id"], "app_id should be in Labels")
}

func TestParsePromQL_BareQuotedMetricWithAppID(t *testing.T) {
	// Grafana Explore Metrics form: {__ignore_usage__="", "metric.name", app_id="X"}
	expr, err := parsePromQL(`{__ignore_usage__="", "kafka.consumer.commit_sync_time_ns_total", app_id="CIKmanlvG3sfdnpr"}`)
	require.NoError(t, err)
	t.Logf("MetricName=%q Labels=%v LabelMatch=%v", expr.MetricName, expr.Labels, expr.LabelMatch)
	assert.Equal(t, "kafka.consumer.commit_sync_time_ns_total", expr.MetricName)
	assert.Equal(t, "CIKmanlvG3sfdnpr", expr.Labels["app_id"], "app_id should be in Labels")
}
