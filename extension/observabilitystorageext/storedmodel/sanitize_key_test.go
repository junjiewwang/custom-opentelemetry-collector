// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package storedmodel

import "testing"

func TestSanitizeKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// 0 segments
		{"", ""},
		// 1 segment — no dots
		{"kind", "kind"},
		{"serviceName", "serviceName"},
		{"unknown", "unknown"},
		// 2 segments — unchanged
		{"http.method", "http.method"},
		{"peer.service", "peer.service"},
		{"service.name", "service.name"},
		{"db.system", "db.system"},
		{"span.kind", "span.kind"},
		{"net.host", "net.host"},
		// 3 segments — keep first dot, convert rest
		{"peer.service.source", "peer.service_source"},
		{"rpc.grpc.status_code", "rpc.grpc_status_code"},
		{"db.operation.name", "db.operation_name"},
		{"net.peer.port", "net.peer_port"},
		{"code.namespace.name", "code.namespace_name"},
		// 4 segments — compress
		{"a.b.c.d", "a.b_c_d"},
		{"net.host.connection.subtype", "net.host_connection_subtype"},
		// Edge cases
		{".", "."},                     // single dot
		{"..", "._"},                   // two dots → first dot kept, rest → _
		{"...", ".__"},                 // three dots → first dot kept, rest → _ _
		{".a", ".a"},                   // dot prefix, 2-segment equivalent
		{"a.", "a."},                   // trailing dot, 2-segment equivalent
		{"a.b.", "a.b_"},              // trailing after 2nd dot
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := SanitizeKey(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeKey(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSanitizeKey_Idempotent(t *testing.T) {
	// SanitizeKey should be idempotent: applying it twice gives the same result.
	keys := []string{
		"kind",
		"http.method",
		"peer.service.source",
		"a.b.c.d",
		"rpc.grpc.status_code",
	}
	for _, k := range keys {
		once := SanitizeKey(k)
		twice := SanitizeKey(once)
		if once != twice {
			t.Errorf("SanitizeKey(%q) not idempotent: %q → %q", k, once, twice)
		}
	}
}

func TestSanitizeKey_PreservesFirstSegment(t *testing.T) {
	// The first segment (before the first dot) should always be preserved.
	tests := []struct {
		input     string
		wantPrefix string
	}{
		{"peer.service.source", "peer"},
		{"http.method", "http"},
		{"a.b.c.d", "a"},
		{"db.operation.name", "db"},
	}
	for _, tt := range tests {
		got := SanitizeKey(tt.input)
		if len(got) < len(tt.wantPrefix) || got[:len(tt.wantPrefix)] != tt.wantPrefix {
			t.Errorf("SanitizeKey(%q) = %q, first segment should be %q", tt.input, got, tt.wantPrefix)
		}
	}
}
