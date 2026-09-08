// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEnsureSumByLe_InsertsCorrectGrouping(t *testing.T) {
	got := ensureSumByLe(`histogram_quantile(0.99, sum(rate(m[5m])))`)
	// Must be `sum by (le) (...)`, not the malformed `sum(by (le) ...)`.
	assert.Equal(t, `histogram_quantile(0.99, sum by (le) (rate(m[5m])))`, got)
}

func TestEnsureSumByLe_AlreadyGrouped(t *testing.T) {
	got := ensureSumByLe(`histogram_quantile(0.99, sum by (le) (rate(m[5m])))`)
	assert.Equal(t, `histogram_quantile(0.99, sum by (le) (rate(m[5m])))`, got)
}
