// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// Config defines the configuration for the Arthas tunnel extension.
type Config struct {
	// ===== Arthas-URI compat mode options =====

	// CompatConnectTimeout is the timeout for waiting agent openTunnel after connectArthas.
	// Default: 20s (aligned with official tunnel-server)
	CompatConnectTimeout time.Duration `mapstructure:"compat_connect_timeout"`

	// StrictIngressMethodAllowlist restricts which method is allowed on each ingress.
	// - agentgateway ingress: agentRegister/openTunnel
	// - admin ingress: connectArthas
	// Default: true
	StrictIngressMethodAllowlist bool `mapstructure:"strict_ingress_method_allowlist"`

	// MaxPendingConnections limits the number of pending connectArthas waiting for openTunnel.
	// 0 means unlimited.
	// Default: 10000
	MaxPendingConnections int `mapstructure:"max_pending_connections"`

	// ===== Keepalive options (mainly for agentRegister control connections) =====

	// PingInterval is the interval between ping control frames.
	// Default: 20s
	PingInterval time.Duration `mapstructure:"ping_interval"`

	// PongTimeout is the base timeout for pong response.
	// Default: 60s
	PongTimeout time.Duration `mapstructure:"pong_timeout"`

	// LivenessGrace is the additional grace period added to PongTimeout
	// to determine the actual liveness timeout (used for ReadDeadline and ListAgents filter).
	// livenessTimeout = PongTimeout + LivenessGrace
	// Default: 30s
	LivenessGrace time.Duration `mapstructure:"liveness_grace"`

	// ===== Distributed mode options =====

	// Distributed contains configuration for multi-replica deployment.
	Distributed DistributedConfig `mapstructure:"distributed"`

	// ===== Auto detach options =====
	// AutoDetach controls automatic detaching Arthas when control connection is idle.
	AutoDetach AutoDetachConfig `mapstructure:"auto_detach"`

	// ===== Legacy fields (kept for compatibility; unused in compat mode) =====

	MaxSessionsPerAgent       int           `mapstructure:"max_sessions_per_agent"`
	SessionIdleTimeout        time.Duration `mapstructure:"session_idle_timeout"`
	SessionMaxDuration        time.Duration `mapstructure:"session_max_duration"`
	OutputBufferSize          int           `mapstructure:"output_buffer_size"`
	OutputFlushInterval       time.Duration `mapstructure:"output_flush_interval"`
	TerminalOpenTimeout       time.Duration `mapstructure:"terminal_open_timeout"`
	TerminalOpenRetryInterval time.Duration `mapstructure:"terminal_open_retry_interval"`
	ArthasStartCooldown       time.Duration `mapstructure:"arthas_start_cooldown"`
	MaxReconnectAttempts      int           `mapstructure:"max_reconnect_attempts"`
}

// AutoDetachConfig contains settings for automatic detaching Arthas on idle.
//
// Semantics (local node only): if an agentRegister control connection is healthy but
// has no active tunnel sessions (and, optionally, no pending connects) for a long time,
// the server will submit an "arthas_detach" task to the probe-side agent.
//
// NOTE: This feature requires a controlplane task submitter to be available.
type AutoDetachConfig struct {
	// Enabled enables the auto-detach background loop.
	// Default: true
	Enabled bool `mapstructure:"enabled"`

	// IdleThreshold is the duration since last activity to consider an agent idle.
	// Default: 15m
	IdleThreshold time.Duration `mapstructure:"idle_threshold"`

	// SweepInterval is how often to scan local registered agents.
	// Default: 90s
	SweepInterval time.Duration `mapstructure:"sweep_interval"`

	// MinRegisterAge is the minimum duration since agent register before auto-detach may trigger.
	// This prevents detaching a newly registered agent that hasn't been used yet.
	// Default: 10m
	MinRegisterAge time.Duration `mapstructure:"min_register_age"`

	// RequireNoPending requires there to be no pending connectArthas attempts before detaching.
	// Default: true
	RequireNoPending bool `mapstructure:"require_no_pending"`

	// Cooldown is the minimum time between auto-detach submissions for the same agent.
	// Default: 10m
	Cooldown time.Duration `mapstructure:"cooldown"`

	// TaskTimeout is the timeout_millis field for the submitted arthas_detach task.
	// Default: 60s
	TaskTimeout time.Duration `mapstructure:"task_timeout"`

	// MaxTasksPerSweep caps how many tasks can be submitted in a single sweep.
	// Default: 200
	MaxTasksPerSweep int `mapstructure:"max_tasks_per_sweep"`
}

// DistributedConfig contains configuration for distributed/multi-replica mode.
type DistributedConfig struct {
	// Enabled enables distributed mode with Redis-based agent registry.
	// When disabled, only local agents are visible.
	// Default: false
	Enabled bool `mapstructure:"enabled"`

	// StorageExtension is the name of the storage extension to depend on.
	// This ensures the storage extension is started before arthas_tunnel.
	// Default: "storage"
	StorageExtension string `mapstructure:"storage_extension"`

	// NodeID is the unique identifier for this collector replica.
	// If empty, defaults to HOSTNAME or POD_NAME environment variable.
	// Default: "" (auto-detect)
	NodeID string `mapstructure:"node_id"`

	// Advertise contains settings for how this node advertises itself to other nodes.
	Advertise AdvertiseConfig `mapstructure:"advertise"`

	// RedisName is the name of the Redis connection from storageext to use.
	// Default: "default"
	RedisName string `mapstructure:"redis_name"`

	// KeyPrefix is the prefix for all Redis keys used by arthas tunnel.
	// Should include environment/cluster info for isolation.
	// Default: "arthas:tunnel"
	KeyPrefix string `mapstructure:"key_prefix"`

	// IndexTTL is the TTL for agent index entries in Redis.
	// Should be greater than livenessTimeout to avoid premature expiration.
	// Default: 120s
	IndexTTL time.Duration `mapstructure:"index_ttl"`

	// PendingTTL is the TTL for pending connection entries in Redis.
	// Should be greater than CompatConnectTimeout.
	// Default: 60s
	PendingTTL time.Duration `mapstructure:"pending_ttl"`

	// LivenessUpdateInterval is the interval for batching liveness updates to Redis.
	// Reduces Redis write pressure from frequent pong updates.
	// Default: 10s
	LivenessUpdateInterval time.Duration `mapstructure:"liveness_update_interval"`

	// NodeHeartbeatInterval is the interval for node self-registration heartbeat.
	// Default: 10s
	NodeHeartbeatInterval time.Duration `mapstructure:"node_heartbeat_interval"`

	// InternalPathPrefix is the path prefix for internal cross-node proxy endpoints.
	// Default: "/internal/v1/arthas"
	InternalPathPrefix string `mapstructure:"internal_path_prefix"`

	// InternalAuth contains authentication settings for internal cross-node communication.
	InternalAuth InternalAuthConfig `mapstructure:"internal_auth"`

	// MaxProxySessions limits the number of concurrent proxy sessions per node.
	// 0 means unlimited.
	// Default: 1000
	MaxProxySessions int `mapstructure:"max_proxy_sessions"`

	// ProxyWriteTimeout is the write timeout for proxy connections.
	// Default: 10s
	ProxyWriteTimeout time.Duration `mapstructure:"proxy_write_timeout"`

	// PendingClaimRetries is the number of retries when claiming a pending connection.
	// Handles race condition where openTunnel arrives before pending is written to Redis.
	// Default: 3
	PendingClaimRetries int `mapstructure:"pending_claim_retries"`

	// PendingClaimRetryInterval is the interval between pending claim retries.
	// Default: 100ms
	PendingClaimRetryInterval time.Duration `mapstructure:"pending_claim_retry_interval"`
}

// AdvertiseConfig contains settings for node address advertisement.
type AdvertiseConfig struct {
	// Mode determines how the node address is discovered.
	// - "auto": Try static_addr -> POD_IP env -> network interface detection
	// - "pod_ip": Use POD_IP environment variable
	// - "pod_dns": Use POD_NAME.HEADLESS_SERVICE.NAMESPACE.svc format
	// - "host_ip": Use HOST_IP environment variable
	// - "static": Use StaticAddr directly
	// Default: "auto"
	Mode string `mapstructure:"mode"`

	// StaticAddr is the static address to advertise when Mode is "static" or as fallback.
	// Format: "host:port"
	// Default: ""
	StaticAddr string `mapstructure:"static_addr"`

	// Port is the port to use for internal communication.
	// If 0, uses the same port as the admin HTTP server.
	// Default: 0 (reuse listener port)
	Port int `mapstructure:"port"`

	// HeadlessService is the headless service name for pod_dns mode.
	// Default: ""
	HeadlessService string `mapstructure:"headless_service"`

	// PodIPEnvKey is the environment variable name for POD_IP.
	// Default: "POD_IP"
	PodIPEnvKey string `mapstructure:"pod_ip_env_key"`

	// PodNameEnvKey is the environment variable name for POD_NAME.
	// Default: "POD_NAME"
	PodNameEnvKey string `mapstructure:"pod_name_env_key"`

	// PodNamespaceEnvKey is the environment variable name for POD_NAMESPACE.
	// Default: "POD_NAMESPACE"
	PodNamespaceEnvKey string `mapstructure:"pod_namespace_env_key"`
}

// InternalAuthConfig contains authentication settings for internal communication.
type InternalAuthConfig struct {
	// Token is the pre-shared key for authenticating internal cross-node requests.
	// Required when distributed mode is enabled.
	// Default: ""
	Token string `mapstructure:"token"`

	// HeaderName is the HTTP header name for the internal token.
	// Default: "X-Internal-Token"
	HeaderName string `mapstructure:"header_name"`
}

// ResolveNodeID returns the effective node ID.
func (c *DistributedConfig) ResolveNodeID() string {
	if c.NodeID != "" {
		return c.NodeID
	}
	// Try POD_NAME first (K8s), then HOSTNAME
	if podName := os.Getenv("POD_NAME"); podName != "" {
		return podName
	}
	if hostname, err := os.Hostname(); err == nil {
		return hostname
	}
	return "unknown"
}

// ResolveNodeAddr returns the effective node address for internal communication.
func (c *DistributedConfig) ResolveNodeAddr(listenerPort int) string {
	port := c.Advertise.Port
	if port == 0 {
		port = listenerPort
	}

	switch c.Advertise.Mode {
	case "static":
		if c.Advertise.StaticAddr != "" {
			return c.Advertise.StaticAddr
		}
	case "pod_ip":
		if ip := c.getPodIP(); ip != "" {
			return fmt.Sprintf("%s:%d", ip, port)
		}
	case "pod_dns":
		if dns := c.getPodDNS(); dns != "" {
			return fmt.Sprintf("%s:%d", dns, port)
		}
	case "host_ip":
		if ip := os.Getenv("HOST_IP"); ip != "" {
			return fmt.Sprintf("%s:%d", ip, port)
		}
	case "auto", "":
		// Try in order: static -> pod_ip -> network interface
		if c.Advertise.StaticAddr != "" {
			return c.Advertise.StaticAddr
		}
		if ip := c.getPodIP(); ip != "" {
			return fmt.Sprintf("%s:%d", ip, port)
		}
		if ip := c.detectLocalIP(); ip != "" {
			return fmt.Sprintf("%s:%d", ip, port)
		}
	}

	// Fallback to localhost
	return fmt.Sprintf("127.0.0.1:%d", port)
}

func (c *DistributedConfig) getPodIP() string {
	envKey := c.Advertise.PodIPEnvKey
	if envKey == "" {
		envKey = "POD_IP"
	}
	return os.Getenv(envKey)
}

func (c *DistributedConfig) getPodDNS() string {
	podNameKey := c.Advertise.PodNameEnvKey
	if podNameKey == "" {
		podNameKey = "POD_NAME"
	}
	nsKey := c.Advertise.PodNamespaceEnvKey
	if nsKey == "" {
		nsKey = "POD_NAMESPACE"
	}

	podName := os.Getenv(podNameKey)
	namespace := os.Getenv(nsKey)
	service := c.Advertise.HeadlessService

	if podName == "" || namespace == "" || service == "" {
		return ""
	}
	return fmt.Sprintf("%s.%s.%s.svc", podName, service, namespace)
}

func (c *DistributedConfig) detectLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			if ipNet.IP.To4() != nil {
				return ipNet.IP.String()
			}
		}
	}
	return ""
}

// Validate validates the configuration.
func (cfg *Config) Validate() error {
	if cfg.AutoDetach.Enabled {
		if cfg.AutoDetach.IdleThreshold <= 0 {
			return fmt.Errorf("auto_detach.idle_threshold must be > 0")
		}
		if cfg.AutoDetach.SweepInterval <= 0 {
			return fmt.Errorf("auto_detach.sweep_interval must be > 0")
		}
		if cfg.AutoDetach.MinRegisterAge < 0 {
			return fmt.Errorf("auto_detach.min_register_age must be >= 0")
		}
		if cfg.AutoDetach.Cooldown < 0 {
			return fmt.Errorf("auto_detach.cooldown must be >= 0")
		}
		if cfg.AutoDetach.TaskTimeout <= 0 {
			return fmt.Errorf("auto_detach.task_timeout must be > 0")
		}
		if cfg.AutoDetach.MaxTasksPerSweep <= 0 {
			return fmt.Errorf("auto_detach.max_tasks_per_sweep must be > 0")
		}
		if cfg.AutoDetach.SweepInterval >= cfg.AutoDetach.IdleThreshold {
			return fmt.Errorf("auto_detach.sweep_interval (%v) should be less than auto_detach.idle_threshold (%v)", cfg.AutoDetach.SweepInterval, cfg.AutoDetach.IdleThreshold)
		}
	}

	if cfg.Distributed.Enabled {
		if cfg.Distributed.InternalAuth.Token == "" {
			return fmt.Errorf("distributed.internal_auth.token is required when distributed mode is enabled")
		}
		if cfg.Distributed.IndexTTL > 0 && cfg.Distributed.IndexTTL <= cfg.PongTimeout+cfg.LivenessGrace {
			return fmt.Errorf("distributed.index_ttl (%v) should be greater than liveness_timeout (%v)",
				cfg.Distributed.IndexTTL, cfg.PongTimeout+cfg.LivenessGrace)
		}
	}
	return nil
}

// createDefaultConfig creates the default configuration.
func createDefaultConfig() *Config {
	return &Config{
		CompatConnectTimeout:         20 * time.Second,
		StrictIngressMethodAllowlist: true,
		MaxPendingConnections:        10000,

		PingInterval:  20 * time.Second,
		PongTimeout:   60 * time.Second,
		LivenessGrace: 30 * time.Second,

		AutoDetach: AutoDetachConfig{
			Enabled:          true,
			IdleThreshold:    15 * time.Minute,
			SweepInterval:    90 * time.Second,
			MinRegisterAge:   10 * time.Minute,
			RequireNoPending: true,
			Cooldown:         10 * time.Minute,
			TaskTimeout:      60 * time.Second,
			MaxTasksPerSweep: 200,
		},

		Distributed: DistributedConfig{
			Enabled:                   false,
			StorageExtension:          "storage",
			NodeID:                    "",
			RedisName:                 "default",
			KeyPrefix:                 "arthas:tunnel",
			IndexTTL:                  120 * time.Second,
			PendingTTL:                60 * time.Second,
			LivenessUpdateInterval:    10 * time.Second,
			NodeHeartbeatInterval:     10 * time.Second,
			InternalPathPrefix:        "/internal/v1/arthas",
			MaxProxySessions:          1000,
			ProxyWriteTimeout:         10 * time.Second,
			PendingClaimRetries:       3,
			PendingClaimRetryInterval: 100 * time.Millisecond,
			Advertise: AdvertiseConfig{
				Mode:               "auto",
				Port:               0,
				PodIPEnvKey:        "POD_IP",
				PodNameEnvKey:      "POD_NAME",
				PodNamespaceEnvKey: "POD_NAMESPACE",
			},
			InternalAuth: InternalAuthConfig{
				HeaderName: "X-Internal-Token",
			},
		},

		// Legacy defaults
		MaxSessionsPerAgent:       5,
		SessionIdleTimeout:        30 * time.Minute,
		SessionMaxDuration:        4 * time.Hour,
		OutputBufferSize:          65536,
		OutputFlushInterval:       50 * time.Millisecond,
		TerminalOpenTimeout:       60 * time.Second,
		TerminalOpenRetryInterval: 1 * time.Second,
		ArthasStartCooldown:       3 * time.Second,
		MaxReconnectAttempts:      0,
	}
}

// IsDistributedEnabled returns whether distributed mode is enabled.
func (cfg *Config) IsDistributedEnabled() bool {
	return cfg.Distributed.Enabled
}

// GetKeyPrefix returns the Redis key prefix with trailing colon.
func (c *DistributedConfig) GetKeyPrefix() string {
	prefix := c.KeyPrefix
	if prefix == "" {
		prefix = "arthas:tunnel"
	}
	if !strings.HasSuffix(prefix, ":") {
		prefix += ":"
	}
	return prefix
}
