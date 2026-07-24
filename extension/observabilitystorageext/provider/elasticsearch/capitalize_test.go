package elasticsearch

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCapitalizeFirst(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Edge cases
		{"", ""},
		{"a", "A"},
		{"A", "A"},
		{"ab", "Ab"},
		{"Ab", "Ab"},
		{"AB", "AB"},

		// TraceQL intrinsic values (lowercase → capitalized)
		{"server", "Server"},
		{"client", "Client"},
		{"producer", "Producer"},
		{"consumer", "Consumer"},
		{"internal", "Internal"},
		{"error", "Error"},
		{"ok", "Ok"},
		{"unset", "Unset"},

		// Multi-word values — only first letter capitalized
		{"SPAN_KIND_SERVER", "SPAN_KIND_SERVER"},
		{"STATUS_CODE_ERROR", "STATUS_CODE_ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := capitalizeFirst(tt.input)
			assert.Equal(t, tt.want, got, "capitalizeFirst(%q)", tt.input)
		})
	}
}

// TestCapitalizeFirst_Idempotent ensures second call is no-op.
func TestCapitalizeFirst_Idempotent(t *testing.T) {
	inputs := []string{"server", "error", "ok", "Server", "Error", "Ok"}
	for _, s := range inputs {
		first := capitalizeFirst(s)
		second := capitalizeFirst(first)
		assert.Equal(t, first, second, "capitalizeFirst should be idempotent for %q", s)
	}
}
