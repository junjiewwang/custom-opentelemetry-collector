// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ═══════════════════════════════════════════════════════════════════════
// arthasuri_compat.go — pure helpers
// ═══════════════════════════════════════════════════════════════════════

func TestNormalizeMethod(t *testing.T) {
	cases := []struct {
		in   string
		want arthasURIMethod
	}{
		{"agentRegister", methodAgentRegister},
		{"AgentRegister", methodAgentRegister}, // case-insensitive
		{"  /connectArthas ", methodConnectArthas},
		{"openTunnel", methodOpenTunnel},
		{"unknown", arthasURIMethod("unknown")}, // preserved verbatim
		{"", arthasURIMethod("")},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, normalizeMethod(c.in), c.in)
	}
}

func TestIsMethodAllowed(t *testing.T) {
	// agentgateway ingress allows agentRegister + openTunnel, not connectArthas.
	assert.True(t, isMethodAllowed(ingressAgentGateway, methodAgentRegister))
	assert.True(t, isMethodAllowed(ingressAgentGateway, methodOpenTunnel))
	assert.False(t, isMethodAllowed(ingressAgentGateway, methodConnectArthas))

	// admin + internal only allow connectArthas.
	assert.True(t, isMethodAllowed(ingressAdmin, methodConnectArthas))
	assert.False(t, isMethodAllowed(ingressAdmin, methodAgentRegister))
	assert.True(t, isMethodAllowed(ingressInternal, methodConnectArthas))
	assert.False(t, isMethodAllowed(ingressInternal, methodOpenTunnel))

	// Unknown ingress → deny all.
	assert.False(t, isMethodAllowed(wsIngress("bogus"), methodConnectArthas))
}

func TestFormatTerminalStatus(t *testing.T) {
	// Each known status maps to a colored, icon-prefixed line ending with reset+CRLF.
	out := formatTerminalStatus(statusReady, "ok")
	assert.Contains(t, out, ansiGreen)
	assert.Contains(t, out, "[+]")
	assert.Contains(t, out, "ok")
	assert.True(t, strings.HasSuffix(out, ansiReset+"\r\n"))

	// Connecting uses cyan + [*].
	assert.Contains(t, formatTerminalStatus(statusConnecting, "c"), "[*]")
	assert.Contains(t, formatTerminalStatus(statusConnecting, "c"), ansiCyan)

	// Error uses red + [-].
	assert.Contains(t, formatTerminalStatus(statusError, "e"), "[-]")
	assert.Contains(t, formatTerminalStatus(statusError, "e"), ansiRed)

	// Unknown status → default icon [.] + reset color.
	def := formatTerminalStatus("bogus", "x")
	assert.Contains(t, def, "[.]")
	assert.Contains(t, def, ansiReset)
}

func TestFirstNonEmpty(t *testing.T) {
	assert.Empty(t, firstNonEmpty())
	assert.Empty(t, firstNonEmpty("", "", ""))
	assert.Equal(t, "x", firstNonEmpty("", "x", "y"))
	assert.Equal(t, "first", firstNonEmpty("first", "second"))
}

func TestBuildResponseFrame(t *testing.T) {
	v := url.Values{}
	v.Set("action", "agentRegister")
	v.Set("agent_id", "a1")
	frame := buildResponseFrame(v)
	assert.True(t, strings.HasPrefix(frame, "response:/?"))
	// url.Values.Encode sorts keys.
	assert.Contains(t, frame, "action=agentRegister")
	assert.Contains(t, frame, "agent_id=a1")
}

func TestStripANSI(t *testing.T) {
	assert.Equal(t, "hello", stripANSI("hello"))
	// \033[32mgreen\033[0m → green
	assert.Equal(t, "green", stripANSI("\033[32mgreen\033[0m"))
	// Multiple escapes.
	assert.Equal(t, "ab", stripANSI("\033[31ma\033[0mb"))
	// Unterminated escape (no 'm') → consumed to end.
	assert.Equal(t, "", stripANSI("\033[31abc"))
}

func TestRandomUpperAlphaNum(t *testing.T) {
	s := randomUpperAlphaNum(8)
	assert.Len(t, s, 8)
	// All chars uppercase hex [0-9A-F].
	for _, c := range s {
		assert.True(t, (c >= '0' && c <= '9') || (c >= 'A' && c <= 'F'), "unexpected char %q", c)
	}
	// Different calls produce different values (probabilistically).
	assert.NotEqual(t, randomUpperAlphaNum(8), randomUpperAlphaNum(8))
	assert.Empty(t, randomUpperAlphaNum(0))
}

func TestWriteClose_NilConn(t *testing.T) {
	err := writeClose(nil, 1000, "bye")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil conn")
}

// ═══════════════════════════════════════════════════════════════════════
// config.go — Validate / defaults / distributed resolution
// ═══════════════════════════════════════════════════════════════════════

func TestCreateDefaultConfig(t *testing.T) {
	cfg := createDefaultConfig()
	require.NotNil(t, cfg)
	assert.Equal(t, 20*time.Second, cfg.CompatConnectTimeout)
	assert.Equal(t, 10000, cfg.MaxPendingConnections)
	// Default config must pass validation.
	assert.NoError(t, cfg.Validate())
}

func TestConfig_Validate_AutoDetach(t *testing.T) {
	cfg := createDefaultConfig()

	// IdleThreshold must be > 0 when enabled.
	cfg.AutoDetach.IdleThreshold = 0
	assert.Error(t, cfg.Validate())

	// SweepInterval must be < IdleThreshold.
	cfg = createDefaultConfig()
	cfg.AutoDetach.SweepInterval = cfg.AutoDetach.IdleThreshold
	assert.Error(t, cfg.Validate())

	// TaskTimeout must be > 0.
	cfg = createDefaultConfig()
	cfg.AutoDetach.TaskTimeout = 0
	assert.Error(t, cfg.Validate())

	// Disabled auto-detach skips those checks.
	cfg = createDefaultConfig()
	cfg.AutoDetach.Enabled = false
	cfg.AutoDetach.IdleThreshold = 0
	assert.NoError(t, cfg.Validate())
}

func TestConfig_Validate_Distributed(t *testing.T) {
	// Distributed enabled without internal token → error.
	cfg := createDefaultConfig()
	cfg.Distributed.Enabled = true
	cfg.Distributed.InternalAuth.Token = ""
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "internal_auth.token is required")

	// With token + adequate IndexTTL → ok.
	cfg.Distributed.InternalAuth.Token = "secret"
	cfg.Distributed.IndexTTL = 10 * time.Minute
	assert.NoError(t, cfg.Validate())

	// IndexTTL <= liveness timeout → error.
	cfg.Distributed.IndexTTL = cfg.PongTimeout + cfg.LivenessGrace
	err = cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "index_ttl")
}

func TestConfig_IsDistributedEnabled(t *testing.T) {
	cfg := createDefaultConfig()
	assert.False(t, cfg.IsDistributedEnabled())
	cfg.Distributed.Enabled = true
	assert.True(t, cfg.IsDistributedEnabled())
}

func TestDistributedConfig_GetKeyPrefix(t *testing.T) {
	// Empty → default, always suffixed with colon.
	c := &DistributedConfig{}
	assert.Equal(t, "arthas:tunnel:", c.GetKeyPrefix())

	// Custom prefix gets a trailing colon.
	c.KeyPrefix = "myprefix"
	assert.Equal(t, "myprefix:", c.GetKeyPrefix())

	// Already suffixed → no double colon.
	c.KeyPrefix = "myprefix:"
	assert.Equal(t, "myprefix:", c.GetKeyPrefix())
}

func TestDistributedConfig_ResolveNodeID(t *testing.T) {
	// Configured NodeID wins.
	c := &DistributedConfig{NodeID: "node-1"}
	assert.Equal(t, "node-1", c.ResolveNodeID())

	// Falls back to POD_NAME env, then hostname.
	c = &DistributedConfig{}
	t.Setenv("POD_NAME", "pod-xyz")
	assert.Equal(t, "pod-xyz", c.ResolveNodeID())
}

func TestDistributedConfig_ResolveNodeAddr(t *testing.T) {
	const port = 4318

	// static mode uses StaticAddr.
	c := &DistributedConfig{Advertise: AdvertiseConfig{Mode: "static", StaticAddr: "1.2.3.4:9999", Port: port}}
	assert.Equal(t, "1.2.3.4:9999", c.ResolveNodeAddr(port))

	// pod_ip mode reads POD_IP (custom env key) + port.
	c = &DistributedConfig{Advertise: AdvertiseConfig{Mode: "pod_ip", Port: port}}
	t.Setenv("POD_IP", "10.0.0.5")
	assert.Equal(t, "10.0.0.5:4318", c.ResolveNodeAddr(port))

	// pod_dns mode assembles from POD_NAME/namespace/headless service.
	c = &DistributedConfig{Advertise: AdvertiseConfig{Mode: "pod_dns", HeadlessService: "tunnel", Port: port}}
	t.Setenv("POD_NAME", "pod-1")
	t.Setenv("POD_NAMESPACE", "ns1")
	assert.Equal(t, "pod-1.tunnel.ns1.svc:4318", c.ResolveNodeAddr(port))

	// host_ip mode reads HOST_IP.
	c = &DistributedConfig{Advertise: AdvertiseConfig{Mode: "host_ip", Port: port}}
	t.Setenv("HOST_IP", "192.168.1.1")
	assert.Equal(t, "192.168.1.1:4318", c.ResolveNodeAddr(port))

	// Empty static + no pod ip + no host env → localhost fallback (auto mode).
	c = &DistributedConfig{Advertise: AdvertiseConfig{Mode: "auto", Port: port}}
	t.Setenv("POD_IP", "")
	t.Setenv("HOST_IP", "")
	// detectLocalIP is environment-dependent; just assert it resolves to a :port form.
	addr := c.ResolveNodeAddr(port)
	assert.Contains(t, addr, ":4318")
}
