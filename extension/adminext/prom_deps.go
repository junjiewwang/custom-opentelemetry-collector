// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// promHandlers holds the dependencies required by the Prometheus-compatible API
// handlers, decoupling them from *Extension (P1 dependency-injection seam).
type promHandlers struct {
	metricReader observabilitystorageext.MetricReader
	logger       *zap.Logger
}

func newPromHandlers(e *Extension) *promHandlers {
	return &promHandlers{
		metricReader: e.storageMetricReader,
		logger:       e.logger,
	}
}
