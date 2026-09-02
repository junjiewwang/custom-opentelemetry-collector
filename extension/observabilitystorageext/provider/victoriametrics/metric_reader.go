// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package victoriametrics

import (
	"go.uber.org/zap"
)

// MetricReader implements the public observabilitystorageext.MetricReader
// interface directly (no adapter layer): the VM Prometheus-compatible HTTP
// API maps 1:1 onto the public query types.
//
// Phase 1 skeleton: methods are added incrementally per the change's task
// list; unimplemented ones return a clear error until then.
type MetricReader struct {
	client   VMClient
	registry *typeRegistry
	logger   *zap.Logger
}

func newMetricReader(client VMClient, registry *typeRegistry, logger *zap.Logger) *MetricReader {
	return &MetricReader{client: client, registry: registry, logger: logger}
}
