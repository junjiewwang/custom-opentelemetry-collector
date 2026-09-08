// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// bareReader embeds the MetricReader interface but does NOT implement
// MetricNameScheme — the historical ES/PG behavior, which the query layer
// treats as "dotted names" (underscore→dot reverse map applies).
type bareReader struct{ observabilitystorageext.MetricReader }

// underscoreReader declares UsesDottedMetricNames()=false, mirroring the
// VictoriaMetrics reader (text-format writer sanitizes dots to underscores).
type underscoreReader struct{ observabilitystorageext.MetricReader }

func (underscoreReader) UsesDottedMetricNames() bool { return false }

// dottedReader declares UsesDottedMetricNames()=true explicitly.
type dottedReader struct{ observabilitystorageext.MetricReader }

func (dottedReader) UsesDottedMetricNames() bool { return true }

func TestUsesDottedMetricNames_DefaultTrue(t *testing.T) {
	// A reader that does NOT implement MetricNameScheme is treated as a
	// dotted-name backend, preserving the historical ES reverse-map behavior.
	assert.True(t, usesDottedMetricNames(bareReader{}))
}

func TestUsesDottedMetricNames_ExplicitFalse(t *testing.T) {
	// VM (text-format) stores underscored names verbatim → no reverse map.
	assert.False(t, usesDottedMetricNames(underscoreReader{}))
}

func TestUsesDottedMetricNames_ExplicitTrue(t *testing.T) {
	assert.True(t, usesDottedMetricNames(dottedReader{}))
}
