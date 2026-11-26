// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAgentRegisteredLogDoesNotExposeAppName guards the two log sites where
// appName is the WS credential. The value must be a fixed marker, never the
// variable itself.
func TestAgentRegisteredLogDoesNotExposeAppName(t *testing.T) {
	src, err := os.ReadFile("arthasuri_compat.go")
	require.NoError(t, err)
	content := string(src)
	require.Contains(t, content, `zap.String("app_name", "<redacted>")`)
	require.Contains(t, content, `zap.String("agent_app_name", "<redacted>")`)
	require.NotContains(t, content, `zap.String("app_name", appName)`)
	require.NotContains(t, content, `zap.String("agent_app_name", agent.appName)`)
}

func TestSanitizeQueryForLog(t *testing.T) {
	raw := "method=connectArthas&token=SECRET&appName=SECRET&id=agent-1&service=svc"
	sanitized := sanitizeQueryForLog(raw)

	require.NotContains(t, sanitized, "SECRET")
	require.Contains(t, sanitized, "token=<redacted>")
	require.Contains(t, sanitized, "appName=<redacted>")
	require.Contains(t, sanitized, "method=connectArthas")
	require.Contains(t, sanitized, "id=agent-1")
	require.Contains(t, sanitized, "service=svc")
}

func TestSanitizeRequestURIForLog(t *testing.T) {
	uri := "/v1/arthas/ws?method=agentRegister&appName=SECRET&id=agent-1"
	sanitized := sanitizeRequestURIForLog(uri)

	require.NotContains(t, sanitized, "SECRET")
	require.Contains(t, sanitized, "/v1/arthas/ws?")
	require.Contains(t, sanitized, "appName=<redacted>")
	require.Equal(t, "/v1/arthas/ws", sanitizeRequestURIForLog("/v1/arthas/ws"))
}

func TestSanitizeQueryForLogUnparsable(t *testing.T) {
	require.Equal(t, "[unparsable query]", sanitizeQueryForLog("token=%ZZ"))
}
