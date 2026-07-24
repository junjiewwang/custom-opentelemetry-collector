package storedmodel

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// No dots
		{"kind", "kind"},
		{"serviceName", "serviceName"},
		{"unknown", "unknown"},

		// 2 segments → unchanged
		{"http.method", "http.method"},
		{"peer.service", "peer.service"},
		{"service.name", "service.name"},
		{"db.system", "db.system"},
		{"span.kind", "span.kind"},
		{"net.host", "net.host"},

		// 3 segments → compress to 2
		{"peer.service.source", "peer.service_source"},
		{"rpc.grpc.status_code", "rpc.grpc_status_code"},
		{"db.operation.name", "db.operation_name"},
		{"net.peer.port", "net.peer_port"},
		{"code.namespace.name", "code.namespace_name"},

		// 4 segments
		{"a.b.c.d", "a.b_c_d"},
		{"net.host.connection.subtype", "net.host_connection_subtype"},

		// Edge cases
		{".", "."},
		{"..", "._"},
		{"...", ".__"},
		{".a", ".a"},
		{"a.", "a."},
		{"a.b.", "a.b_"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, SanitizeKey(tt.input))
		})
	}
}

func TestSanitizeKey_Idempotent(t *testing.T) {
	keys := []string{"kind", "http.method", "peer.service.source", "a.b.c.d"}
	for _, k := range keys {
		first := SanitizeKey(k)
		second := SanitizeKey(first)
		assert.Equal(t, first, second, "SanitizeKey should be idempotent for %q", k)
	}
}

func TestSanitizeKey_PreservesFirstSegment(t *testing.T) {
	tests := []struct {
		input      string
		wantPrefix string
	}{
		{"http.method", "http.method"},
		{"peer.service.source", "peer.service"},
		{"a.b.c.d", "a.b"},
	}
	for _, tt := range tests {
		got := SanitizeKey(tt.input)
		if len(got) < len(tt.wantPrefix) || got[:len(tt.wantPrefix)] != tt.wantPrefix {
			t.Errorf("SanitizeKey(%q) = %q, first segment should be %q", tt.input, got, tt.wantPrefix)
		}
	}
}

func TestSanitizeMetricKey(t *testing.T) {
	tests := []struct{ input, want string }{
		{"server", "server"},
		{"server.address", "server_address"},
		{"server.port", "server_port"},
		{"http.method", "http_method"},
		{"http.status_code", "http_status_code"},
		{"peer.service", "peer_service"},
		{"peer.service.source", "peer_service_source"},
		{"rpc.grpc.status_code", "rpc_grpc_status_code"},
		{"service.name", "service_name"},
		{"status.code", "status_code"},
		{"connection_type", "connection_type"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := SanitizeMetricKey(tt.input)
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, ".", "zero dots")
		})
	}
}
