// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/instrumentationmanager"
)

func TestInstrumentationErrorMapsRevisionConflictTo409(t *testing.T) {
	wrapped := fmt.Errorf("save rule: %w", instrumentationmanager.ErrRevisionConflict)
	apiErr := instrumentationError(wrapped)
	assert.Equal(t, http.StatusConflict, apiErr.Status)

	apiErr = instrumentationError(instrumentationmanager.ErrRuleNotFound)
	assert.Equal(t, http.StatusNotFound, apiErr.Status)

	apiErr = instrumentationError(errors.New("bad request"))
	assert.Equal(t, http.StatusBadRequest, apiErr.Status)
}
