// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// lokiHandlers holds the dependencies required by the Loki-compatible API
// handlers, decoupling them from *Extension (P1 dependency-injection seam).
type lokiHandlers struct {
	logReader observabilitystorageext.LogReader
	logger    *zap.Logger
}

func newLokiHandlers(e *Extension) *lokiHandlers {
	return &lokiHandlers{
		logReader: e.storageLogReader,
		logger:    e.logger,
	}
}
