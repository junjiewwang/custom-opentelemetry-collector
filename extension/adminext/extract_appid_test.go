// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractAppID(t *testing.T) {
	t.Run("app_id underscore", func(t *testing.T) {
		appID, labels := extractAppID(map[string]string{"app_id": "CIKmanlvG3sfdnpr", "env": "prod"})
		assert.Equal(t, "CIKmanlvG3sfdnpr", appID)
		assert.Equal(t, map[string]string{"env": "prod"}, labels, "app_id must be removed")
	})

	t.Run("appId camelCase", func(t *testing.T) {
		appID, labels := extractAppID(map[string]string{"appId": "X", "env": "prod"})
		assert.Equal(t, "X", appID)
		assert.Equal(t, map[string]string{"env": "prod"}, labels)
	})

	t.Run("no app id", func(t *testing.T) {
		appID, labels := extractAppID(map[string]string{"env": "prod"})
		assert.Equal(t, "", appID)
		assert.Equal(t, map[string]string{"env": "prod"}, labels)
	})

	t.Run("nil labels", func(t *testing.T) {
		appID, labels := extractAppID(nil)
		assert.Equal(t, "", appID)
		assert.Nil(t, labels)
	})

	t.Run("empty app_id value", func(t *testing.T) {
		// Empty value is not a real appID; leave it (no extraction).
		appID, labels := extractAppID(map[string]string{"app_id": ""})
		assert.Equal(t, "", appID)
		assert.Equal(t, map[string]string{"app_id": ""}, labels)
	})
}
