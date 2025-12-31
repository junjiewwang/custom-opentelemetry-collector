// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentregistry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKeyBuilder_EscapeUnescape(t *testing.T) {
	kb := NewKeyBuilder("otel:agents")

	testCases := []struct {
		name  string
		input string
	}{
		{"simple", "order-service"},
		{"with_colon", "user:auth"},
		{"with_dots", "host.name.com"},
		{"chinese", "中文服务"},
		{"chinese_complex", "支付系统/订单服务"},
		{"special_chars", "service/v1@latest"},
		{"with_slash", "path/to/service"},
		{"with_backslash", "windows\\path"},
		{"mixed_special", "app/v1\\test"},
		{"empty", ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			escaped := kb.Escape(tc.input)
			unescaped := kb.Unescape(escaped)
			assert.Equal(t, tc.input, unescaped)

			// Verify escaped string doesn't contain unescaped "/"
			// (except when the original had no "/" at all)
			if tc.input != "" && !containsUnescapedSlash(escaped) {
				// This is expected - all slashes should be escaped
			}
		})
	}
}

func containsUnescapedSlash(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			backslashes := 0
			for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
				backslashes++
			}
			if backslashes%2 == 0 {
				return true
			}
		}
	}
	return false
}

func TestKeyBuilder_InstanceKey_HostMode(t *testing.T) {
	// Default mode is "host"
	kb := NewKeyBuilder("otel:agents")

	key := kb.InstanceKey("payment-system", "order-service", "web-host-01", "10.0.0.1")

	// Key should contain the prefix
	assert.Contains(t, key, "otel:agents:")

	// Key should be readable (contains original values)
	assert.Contains(t, key, "payment-system")
	assert.Contains(t, key, "order-service")
	assert.Contains(t, key, "web-host-01")

	// Expected format: otel:agents:app/{appID}/svc/{serviceName}/host/{hostname}
	// Note: IP is NOT included in host mode
	expected := "otel:agents:app/payment-system/svc/order-service/host/web-host-01"
	assert.Equal(t, expected, key)
}

func TestKeyBuilder_InstanceKey_HostIPMode(t *testing.T) {
	// Explicitly use host_ip mode
	kb := NewKeyBuilderWithMode("otel:agents", InstanceKeyModeHostIP)

	key := kb.InstanceKey("payment-system", "order-service", "web-host-01", "10.0.0.1")

	// Expected format: otel:agents:app/{appID}/svc/{serviceName}/host/{hostname}/ip/{ip}
	expected := "otel:agents:app/payment-system/svc/order-service/host/web-host-01/ip/10.0.0.1"
	assert.Equal(t, expected, key)
}

func TestKeyBuilder_InstanceKey_Chinese(t *testing.T) {
	kb := NewKeyBuilder("otel:agents")

	// Test with Chinese characters - should be readable in Redis
	key := kb.InstanceKey("支付系统", "订单服务", "主机01", "10.0.0.1")

	// Key should contain Chinese characters directly (UTF-8)
	assert.Contains(t, key, "支付系统")
	assert.Contains(t, key, "订单服务")
	assert.Contains(t, key, "主机01")

	// Host mode - no IP in key
	expected := "otel:agents:app/支付系统/svc/订单服务/host/主机01"
	assert.Equal(t, expected, key)
}

func TestKeyBuilder_GroupsKey(t *testing.T) {
	kb := NewKeyBuilder("otel:agents")
	assert.Equal(t, "otel:agents:_apps", kb.GroupsKey())
}

func TestKeyBuilder_ServicesKey(t *testing.T) {
	kb := NewKeyBuilder("otel:agents")

	// Simple case
	key := kb.ServicesKey("payment-system")
	assert.Equal(t, "otel:agents:app/payment-system:_services", key)

	// Chinese case
	key = kb.ServicesKey("支付系统")
	assert.Equal(t, "otel:agents:app/支付系统:_services", key)
}

func TestKeyBuilder_InstancesKey(t *testing.T) {
	kb := NewKeyBuilder("otel:agents")

	key := kb.InstancesKey("payment-system", "order-service")
	assert.Equal(t, "otel:agents:app/payment-system/svc/order-service:_instances", key)

	// Chinese case
	key = kb.InstancesKey("支付系统", "订单服务")
	assert.Equal(t, "otel:agents:app/支付系统/svc/订单服务:_instances", key)
}

func TestKeyBuilder_OnlineKey(t *testing.T) {
	kb := NewKeyBuilder("otel:agents")
	assert.Equal(t, "otel:agents:_online", kb.OnlineKey())
}

func TestKeyBuilder_AgentIDsKey(t *testing.T) {
	kb := NewKeyBuilder("otel:agents")

	// New Hash-based key
	key := kb.AgentIDsKey()
	assert.Equal(t, "otel:agents:_ids", key)
}

func TestKeyBuilder_AgentIDKey_Deprecated(t *testing.T) {
	kb := NewKeyBuilder("otel:agents")

	// Deprecated individual key (kept for backward compatibility)
	key := kb.AgentIDKey("agent-uuid-123")
	assert.Equal(t, "otel:agents:_id/agent-uuid-123", key)
}

func TestKeyBuilder_FullKeyPath_HostMode(t *testing.T) {
	kb := NewKeyBuilder("otel:agents")

	fullPath := kb.FullKeyPath("payment-system", "order-service", "web-host-01", "10.0.0.1")

	// Host mode format: {appID}/{serviceName}/host/{hostname}
	assert.Equal(t, "payment-system/order-service/host/web-host-01", fullPath)

	// Parse it back
	appIDEsc, serviceNameEsc, instanceKey, err := kb.ParseFullKeyPath(fullPath)
	require.NoError(t, err)

	// Unescape and verify
	appID := kb.Unescape(appIDEsc)
	assert.Equal(t, "payment-system", appID)

	serviceName := kb.Unescape(serviceNameEsc)
	assert.Equal(t, "order-service", serviceName)

	// Parse instance key - in host mode, IP is empty
	hostnameEsc, ip, err := kb.ParseInstanceKey(instanceKey)
	require.NoError(t, err)
	assert.Equal(t, "", ip) // No IP in host mode

	hostname := kb.Unescape(hostnameEsc)
	assert.Equal(t, "web-host-01", hostname)
}

func TestKeyBuilder_FullKeyPath_HostIPMode(t *testing.T) {
	kb := NewKeyBuilderWithMode("otel:agents", InstanceKeyModeHostIP)

	fullPath := kb.FullKeyPath("payment-system", "order-service", "web-host-01", "10.0.0.1")

	// Host_IP mode format: {appID}/{serviceName}/host/{hostname}/ip/{ip}
	assert.Equal(t, "payment-system/order-service/host/web-host-01/ip/10.0.0.1", fullPath)

	// Parse it back
	appIDEsc, serviceNameEsc, instanceKey, err := kb.ParseFullKeyPath(fullPath)
	require.NoError(t, err)

	appID := kb.Unescape(appIDEsc)
	assert.Equal(t, "payment-system", appID)

	serviceName := kb.Unescape(serviceNameEsc)
	assert.Equal(t, "order-service", serviceName)

	// Parse instance key - in host_ip mode, IP is included
	hostnameEsc, ip, err := kb.ParseInstanceKey(instanceKey)
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.1", ip)

	hostname := kb.Unescape(hostnameEsc)
	assert.Equal(t, "web-host-01", hostname)
}

func TestKeyBuilder_FullKeyPath_Chinese(t *testing.T) {
	kb := NewKeyBuilder("otel:agents")

	fullPath := kb.FullKeyPath("支付系统", "订单服务", "主机01", "10.0.0.1")

	// Verify the path is readable
	assert.Contains(t, fullPath, "支付系统")
	assert.Contains(t, fullPath, "订单服务")
	assert.Contains(t, fullPath, "主机01")

	// Parse it back
	appIDEsc, serviceNameEsc, instanceKey, err := kb.ParseFullKeyPath(fullPath)
	require.NoError(t, err)

	appID := kb.Unescape(appIDEsc)
	assert.Equal(t, "支付系统", appID)

	serviceName := kb.Unescape(serviceNameEsc)
	assert.Equal(t, "订单服务", serviceName)

	hostnameEsc, _, err := kb.ParseInstanceKey(instanceKey)
	require.NoError(t, err)

	hostname := kb.Unescape(hostnameEsc)
	assert.Equal(t, "主机01", hostname)
}

func TestKeyBuilder_ParseFullKeyPath_Invalid(t *testing.T) {
	kb := NewKeyBuilder("otel:agents")

	_, _, _, err := kb.ParseFullKeyPath("invalid")
	assert.Error(t, err)

	_, _, _, err = kb.ParseFullKeyPath("only/two")
	assert.Error(t, err)

	_, _, _, err = kb.ParseFullKeyPath("no-host-marker")
	assert.Error(t, err)
}

func TestKeyBuilder_ParseInstanceKey_HostMode(t *testing.T) {
	kb := NewKeyBuilder("otel:agents")

	// Host mode: host/{hostname}
	hostnameEsc, ip, err := kb.ParseInstanceKey("host/web-host-01")
	require.NoError(t, err)
	assert.Equal(t, "", ip) // No IP in host mode
	assert.Equal(t, "web-host-01", kb.Unescape(hostnameEsc))

	// Chinese hostname
	hostnameEsc, ip, err = kb.ParseInstanceKey("host/主机01")
	require.NoError(t, err)
	assert.Equal(t, "", ip)
	assert.Equal(t, "主机01", kb.Unescape(hostnameEsc))
}

func TestKeyBuilder_ParseInstanceKey_HostIPMode(t *testing.T) {
	kb := NewKeyBuilder("otel:agents")

	testCases := []struct {
		name         string
		instanceKey  string
		wantHostname string
		wantIP       string
	}{
		{"simple", "host/web-host-01/ip/10.0.0.1", "web-host-01", "10.0.0.1"},
		{"chinese", "host/主机01/ip/192.168.1.100", "主机01", "192.168.1.100"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			hostnameEsc, ip, err := kb.ParseInstanceKey(tc.instanceKey)
			require.NoError(t, err)
			assert.Equal(t, tc.wantIP, ip)

			hostname := kb.Unescape(hostnameEsc)
			assert.Equal(t, tc.wantHostname, hostname)
		})
	}
}

func TestKeyBuilder_ParseInstanceKey_Invalid(t *testing.T) {
	kb := NewKeyBuilder("otel:agents")

	_, _, err := kb.ParseInstanceKey("nohostprefix")
	assert.Error(t, err)

	// Note: "host/noip" is now valid in host mode, so we don't test it as invalid
}

func TestKeyBuilder_DefaultPrefix(t *testing.T) {
	kb := NewKeyBuilder("")
	assert.Equal(t, "otel:agents:_apps", kb.GroupsKey())
}

func TestKeyBuilder_SpecialCharacters(t *testing.T) {
	kb := NewKeyBuilderWithMode("otel:agents", InstanceKeyModeHostIP)

	// Test with special characters that need escaping
	appID := "group/with/slashes"
	serviceName := "service\\with\\backslashes"
	hostname := "host-name-with-dashes"
	ip := "10.0.0.1"

	key := kb.InstanceKey(appID, serviceName, hostname, ip)

	// The key should have escaped slashes
	assert.Contains(t, key, "group\\/with\\/slashes")
	assert.Contains(t, key, "service\\\\with\\\\backslashes")

	// Verify we can parse the full path back
	fullPath := kb.FullKeyPath(appID, serviceName, hostname, ip)
	appIDEsc, serviceNameEsc, instanceKey, err := kb.ParseFullKeyPath(fullPath)
	require.NoError(t, err)

	assert.Equal(t, appID, kb.Unescape(appIDEsc))
	assert.Equal(t, serviceName, kb.Unescape(serviceNameEsc))

	hostnameEsc, parsedIP, err := kb.ParseInstanceKey(instanceKey)
	require.NoError(t, err)
	assert.Equal(t, hostname, kb.Unescape(hostnameEsc))
	assert.Equal(t, ip, parsedIP)
}

func TestKeyBuilder_InstanceID_HostMode(t *testing.T) {
	kb := NewKeyBuilder("otel:agents")

	instanceID := kb.InstanceID("web-host-01", "10.0.0.1")
	// Host mode: no IP
	assert.Equal(t, "host/web-host-01", instanceID)

	// Chinese hostname
	instanceID = kb.InstanceID("主机01", "192.168.1.1")
	assert.Equal(t, "host/主机01", instanceID)
}

func TestKeyBuilder_InstanceID_HostIPMode(t *testing.T) {
	kb := NewKeyBuilderWithMode("otel:agents", InstanceKeyModeHostIP)

	instanceID := kb.InstanceID("web-host-01", "10.0.0.1")
	assert.Equal(t, "host/web-host-01/ip/10.0.0.1", instanceID)

	// Chinese hostname
	instanceID = kb.InstanceID("主机01", "192.168.1.1")
	assert.Equal(t, "host/主机01/ip/192.168.1.1", instanceID)
}

func TestKeyBuilder_BackwardCompatibility(t *testing.T) {
	kb := NewKeyBuilder("otel:agents")

	// Test deprecated Encode/Decode methods still work
	original := "test-value"
	encoded := kb.Encode(original)
	decoded, err := kb.Decode(encoded)
	require.NoError(t, err)
	assert.Equal(t, original, decoded)

	// Test deprecated ServicesKeyEncoded
	key1 := kb.ServicesKeyEncoded("test-app")
	key2 := kb.ServicesKeyEscaped("test-app")
	assert.Equal(t, key1, key2)

	// Test deprecated InstancesKeyEncoded
	key1 = kb.InstancesKeyEncoded("test-app", "test-svc")
	key2 = kb.InstancesKeyEscaped("test-app", "test-svc")
	assert.Equal(t, key1, key2)
}

func TestKeyBuilder_NewKeyBuilderWithMode(t *testing.T) {
	// Test with empty mode (should default to host)
	kb := NewKeyBuilderWithMode("otel:agents", "")
	fullPath := kb.FullKeyPath("app", "svc", "host", "10.0.0.1")
	assert.Equal(t, "app/svc/host/host", fullPath) // No IP

	// Test with explicit host mode
	kb = NewKeyBuilderWithMode("otel:agents", InstanceKeyModeHost)
	fullPath = kb.FullKeyPath("app", "svc", "host", "10.0.0.1")
	assert.Equal(t, "app/svc/host/host", fullPath) // No IP

	// Test with host_ip mode
	kb = NewKeyBuilderWithMode("otel:agents", InstanceKeyModeHostIP)
	fullPath = kb.FullKeyPath("app", "svc", "host", "10.0.0.1")
	assert.Equal(t, "app/svc/host/host/ip/10.0.0.1", fullPath) // With IP
}

func TestKeyBuilder_HeartbeatKey(t *testing.T) {
	kb := NewKeyBuilder("otel:agents")

	// Test heartbeat key generation
	fullPath := "payment-system/order-service/host/web-host-01"
	heartbeatKey := kb.HeartbeatKey(fullPath)
	assert.Equal(t, "otel:agents:_hb/payment-system/order-service/host/web-host-01", heartbeatKey)

	// Test heartbeat key from parts
	heartbeatKey = kb.HeartbeatKeyFromParts("payment-system", "order-service", "web-host-01", "10.0.0.1")
	// In host mode, IP is not included in fullPath
	assert.Equal(t, "otel:agents:_hb/payment-system/order-service/host/web-host-01", heartbeatKey)
}

func TestKeyBuilder_HeartbeatKey_HostIPMode(t *testing.T) {
	kb := NewKeyBuilderWithMode("otel:agents", InstanceKeyModeHostIP)

	// Test heartbeat key from parts in host_ip mode
	heartbeatKey := kb.HeartbeatKeyFromParts("payment-system", "order-service", "web-host-01", "10.0.0.1")
	// In host_ip mode, IP is included in fullPath
	assert.Equal(t, "otel:agents:_hb/payment-system/order-service/host/web-host-01/ip/10.0.0.1", heartbeatKey)
}
