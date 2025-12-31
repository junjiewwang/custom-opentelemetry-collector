// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentregistry

import (
	"fmt"
	"strings"
)

// keySeparator is the separator used in Redis keys.
// We use "/" instead of ":" for user-provided values to avoid conflicts with Redis key namespace convention.
const keySeparator = "/"

// KeyBuilder constructs Redis keys with UTF-8 encoding for better readability.
// Redis natively supports UTF-8, so we don't need Base64 encoding.
// Key structure uses ":" for namespace separation and "/" for user-provided value separation.
//
// Key format examples (InstanceKeyMode = "host"):
//   - Instance info: otel:agents:app/{appID}/svc/{serviceName}/host/{hostname}
//   - Heartbeat:     otel:agents:_hb/{fullPath} (short TTL for online detection)
//   - Agent IDs:     otel:agents:_ids (Hash: agentID -> fullPath)
//   - Apps index:    otel:agents:_apps
//   - Services:      otel:agents:app/{appID}:_services
//   - Instances:     otel:agents:app/{appID}/svc/{serviceName}:_instances
//   - Online set:    otel:agents:_online
//
// Key format examples (InstanceKeyMode = "host_ip"):
//   - Instance info: otel:agents:app/{appID}/svc/{serviceName}/host/{hostname}/ip/{ip}
type KeyBuilder struct {
	prefix          string
	instanceKeyMode string
}

// NewKeyBuilder creates a new KeyBuilder with the given prefix.
func NewKeyBuilder(prefix string) *KeyBuilder {
	if prefix == "" {
		prefix = "otel:agents"
	}
	return &KeyBuilder{
		prefix:          prefix,
		instanceKeyMode: InstanceKeyModeHost, // Default to simpler format
	}
}

// NewKeyBuilderWithMode creates a new KeyBuilder with the given prefix and instance key mode.
func NewKeyBuilderWithMode(prefix string, instanceKeyMode string) *KeyBuilder {
	if prefix == "" {
		prefix = "otel:agents"
	}
	if instanceKeyMode == "" {
		instanceKeyMode = InstanceKeyModeHost
	}
	return &KeyBuilder{
		prefix:          prefix,
		instanceKeyMode: instanceKeyMode,
	}
}

// Escape escapes special characters in user-provided values.
// Replaces "/" with "\/" and "\" with "\\" to ensure safe key construction.
func (k *KeyBuilder) Escape(s string) string {
	// First escape backslashes, then escape forward slashes
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "/", "\\/")
	return s
}

// Unescape reverses the escaping done by Escape.
func (k *KeyBuilder) Unescape(s string) string {
	// Reverse order: first unescape forward slashes, then backslashes
	s = strings.ReplaceAll(s, "\\/", "/")
	s = strings.ReplaceAll(s, "\\\\", "\\")
	return s
}

// InstanceKey returns the key for storing agent instance info.
// Format depends on instanceKeyMode:
//   - "host":    {prefix}:app/{appID}/svc/{serviceName}/host/{hostname}
//   - "host_ip": {prefix}:app/{appID}/svc/{serviceName}/host/{hostname}/ip/{ip}
func (k *KeyBuilder) InstanceKey(appID, serviceName, hostname, ip string) string {
	if k.instanceKeyMode == InstanceKeyModeHostIP {
		return fmt.Sprintf("%s:app/%s/svc/%s/host/%s/ip/%s",
			k.prefix,
			k.Escape(appID),
			k.Escape(serviceName),
			k.Escape(hostname),
			ip,
		)
	}
	// Default: host mode
	return fmt.Sprintf("%s:app/%s/svc/%s/host/%s",
		k.prefix,
		k.Escape(appID),
		k.Escape(serviceName),
		k.Escape(hostname),
	)
}

// InstanceKeyFromParts returns the key from escaped parts.
// appIDEsc, serviceNameEsc are already escaped, instanceKey is the instance identifier.
func (k *KeyBuilder) InstanceKeyFromParts(appIDEsc, serviceNameEsc, instanceKey string) string {
	return fmt.Sprintf("%s:app/%s/svc/%s/%s", k.prefix, appIDEsc, serviceNameEsc, instanceKey)
}

// GroupsKey returns the key for the set of all apps.
// Format: {prefix}:_apps
func (k *KeyBuilder) GroupsKey() string {
	return k.prefix + ":_apps"
}

// ServicesKey returns the key for the set of services under an app.
// Format: {prefix}:app/{appID}:_services
func (k *KeyBuilder) ServicesKey(appID string) string {
	return fmt.Sprintf("%s:app/%s:_services", k.prefix, k.Escape(appID))
}

// ServicesKeyEscaped returns the key using pre-escaped app ID.
func (k *KeyBuilder) ServicesKeyEscaped(appIDEsc string) string {
	return fmt.Sprintf("%s:app/%s:_services", k.prefix, appIDEsc)
}

// InstancesKey returns the key for the set of instances under a service.
// Format: {prefix}:app/{appID}/svc/{serviceName}:_instances
func (k *KeyBuilder) InstancesKey(appID, serviceName string) string {
	return fmt.Sprintf("%s:app/%s/svc/%s:_instances",
		k.prefix,
		k.Escape(appID),
		k.Escape(serviceName),
	)
}

// InstancesKeyEscaped returns the key using pre-escaped parts.
func (k *KeyBuilder) InstancesKeyEscaped(appIDEsc, serviceNameEsc string) string {
	return fmt.Sprintf("%s:app/%s/svc/%s:_instances", k.prefix, appIDEsc, serviceNameEsc)
}

// OnlineKey returns the key for the sorted set of online agents.
// Format: {prefix}:_online
func (k *KeyBuilder) OnlineKey() string {
	return k.prefix + ":_online"
}

// HeartbeatKey returns the key for agent heartbeat status.
// Format: {prefix}:_hb/{fullPath}
// This key has a short TTL (e.g., 30s) and is used to detect online/offline status.
// When this key expires, the agent is considered offline.
func (k *KeyBuilder) HeartbeatKey(fullPath string) string {
	return fmt.Sprintf("%s:_hb/%s", k.prefix, fullPath)
}

// HeartbeatKeyFromParts returns the heartbeat key from agent info parts.
func (k *KeyBuilder) HeartbeatKeyFromParts(appID, serviceName, hostname, ip string) string {
	fullPath := k.FullKeyPath(appID, serviceName, hostname, ip)
	return k.HeartbeatKey(fullPath)
}

// AgentIDsKey returns the key for the hash storing all agent ID mappings.
// Format: {prefix}:_ids
// This is a Hash where field=agentID, value=fullPath
func (k *KeyBuilder) AgentIDsKey() string {
	return k.prefix + ":_ids"
}

// Deprecated: AgentIDKey returns the key for agent ID reverse index.
// Use AgentIDsKey() with HGET/HSET instead for better key efficiency.
// Format: {prefix}:_id/{agentID}
func (k *KeyBuilder) AgentIDKey(agentID string) string {
	return fmt.Sprintf("%s:_id/%s", k.prefix, k.Escape(agentID))
}

// TasksKey returns the key for agent tasks hash.
// Format: {prefix}:_tasks
func (k *KeyBuilder) TasksKey() string {
	return k.prefix + ":_tasks"
}

// EventOnlineKey returns the Pub/Sub channel for online events.
func (k *KeyBuilder) EventOnlineKey() string {
	return k.prefix + ":_events:online"
}

// EventOfflineKey returns the Pub/Sub channel for offline events.
func (k *KeyBuilder) EventOfflineKey() string {
	return k.prefix + ":_events:offline"
}

// InstanceID generates a unique instance identifier from hostname and IP.
// Format depends on instanceKeyMode:
//   - "host":    host/{hostname}
//   - "host_ip": host/{hostname}/ip/{ip}
func (k *KeyBuilder) InstanceID(hostname, ip string) string {
	if k.instanceKeyMode == InstanceKeyModeHostIP {
		return fmt.Sprintf("host/%s/ip/%s", k.Escape(hostname), ip)
	}
	// Default: host mode
	return fmt.Sprintf("host/%s", k.Escape(hostname))
}

// FullKeyPath generates the full key path for an instance (without prefix).
// Format depends on instanceKeyMode:
//   - "host":    {appID}/{serviceName}/host/{hostname}
//   - "host_ip": {appID}/{serviceName}/host/{hostname}/ip/{ip}
// This is stored in the _online sorted set as the member.
func (k *KeyBuilder) FullKeyPath(appID, serviceName, hostname, ip string) string {
	if k.instanceKeyMode == InstanceKeyModeHostIP {
		return fmt.Sprintf("%s/%s/host/%s/ip/%s",
			k.Escape(appID),
			k.Escape(serviceName),
			k.Escape(hostname),
			ip,
		)
	}
	// Default: host mode
	return fmt.Sprintf("%s/%s/host/%s",
		k.Escape(appID),
		k.Escape(serviceName),
		k.Escape(hostname),
	)
}

// ParseFullKeyPath parses a full key path into its components.
// Returns: appIDEsc, serviceNameEsc, instanceKey, error
// The returned appIDEsc and serviceNameEsc are still escaped.
func (k *KeyBuilder) ParseFullKeyPath(fullPath string) (appIDEsc, serviceNameEsc, instanceKey string, err error) {
	// Format: {appID}/{serviceName}/host/{hostname}[/ip/{ip}]
	// Find "/host/" to split the path
	hostIdx := strings.Index(fullPath, "/host/")
	if hostIdx == -1 {
		return "", "", "", fmt.Errorf("invalid full key path (missing /host/): %s", fullPath)
	}

	// Everything before /host/ is appID/serviceName
	appServicePart := fullPath[:hostIdx]
	instanceKey = fullPath[hostIdx+1:] // "host/{hostname}" or "host/{hostname}/ip/{ip}"

	// Split appID and serviceName - need to handle escaped slashes
	// Find the first unescaped "/" that separates appID and serviceName
	slashIdx := k.findUnescapedSlash(appServicePart)
	if slashIdx == -1 {
		return "", "", "", fmt.Errorf("invalid full key path (missing service separator): %s", fullPath)
	}

	appIDEsc = appServicePart[:slashIdx]
	serviceNameEsc = appServicePart[slashIdx+1:]

	return appIDEsc, serviceNameEsc, instanceKey, nil
}

// findUnescapedSlash finds the index of the first unescaped "/" in the string.
// Returns -1 if not found.
func (k *KeyBuilder) findUnescapedSlash(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			// Count preceding backslashes
			backslashes := 0
			for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
				backslashes++
			}
			// If even number of backslashes, this slash is unescaped
			if backslashes%2 == 0 {
				return i
			}
		}
	}
	return -1
}

// ParseInstanceKey parses an instance key into hostname (escaped) and IP.
// Format: host/{hostname} or host/{hostname}/ip/{ip}
func (k *KeyBuilder) ParseInstanceKey(instanceKey string) (hostnameEsc, ip string, err error) {
	// Format: host/{hostname} or host/{hostname}/ip/{ip}
	if !strings.HasPrefix(instanceKey, "host/") {
		return "", "", fmt.Errorf("invalid instance key (missing host/ prefix): %s", instanceKey)
	}

	// Check if it contains /ip/
	ipIdx := strings.LastIndex(instanceKey, "/ip/")
	if ipIdx != -1 {
		// host_ip mode: host/{hostname}/ip/{ip}
		hostnameEsc = instanceKey[5:ipIdx] // Skip "host/"
		ip = instanceKey[ipIdx+4:]          // Skip "/ip/"
	} else {
		// host mode: host/{hostname}
		hostnameEsc = instanceKey[5:] // Skip "host/"
		ip = ""
	}

	return hostnameEsc, ip, nil
}

// Deprecated: Encode is kept for backward compatibility during migration.
// Use Escape instead for new code.
func (k *KeyBuilder) Encode(s string) string {
	return k.Escape(s)
}

// Deprecated: Decode is kept for backward compatibility during migration.
// Use Unescape instead for new code.
func (k *KeyBuilder) Decode(s string) (string, error) {
	return k.Unescape(s), nil
}

// Deprecated: ServicesKeyEncoded is kept for backward compatibility.
// Use ServicesKeyEscaped instead.
func (k *KeyBuilder) ServicesKeyEncoded(appIDEsc string) string {
	return k.ServicesKeyEscaped(appIDEsc)
}

// Deprecated: InstancesKeyEncoded is kept for backward compatibility.
// Use InstancesKeyEscaped instead.
func (k *KeyBuilder) InstancesKeyEncoded(appIDEsc, serviceNameEsc string) string {
	return k.InstancesKeyEscaped(appIDEsc, serviceNameEsc)
}
