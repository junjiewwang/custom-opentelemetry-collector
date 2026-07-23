// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"testing"
)

func TestConvertLokiRegex(t *testing.T) {
	tests := []struct {
		name             string
		input            string
		wantPattern      string
		wantCaseInsensitive bool
	}{
		{
			name:               "bare pattern",
			input:              "order",
			wantPattern:        "order",
			wantCaseInsensitive: false,
		},
		{
			name:               "(?i) prefix",
			input:              "(?i)order",
			wantPattern:        "order",
			wantCaseInsensitive: true,
		},
		{
			name:               "(?i) with special chars",
			input:              "(?i)error|warn",
			wantPattern:        "error|warn",
			wantCaseInsensitive: true,
		},
		{
			name:               "(?is) multiple flags",
			input:              "(?is)order.*",
			wantPattern:        "order.*",
			wantCaseInsensitive: true,
		},
		{
			name:               "(?si) reversed order",
			input:              "(?si)order.*",
			wantPattern:        "order.*",
			wantCaseInsensitive: true,
		},
		{
			name:               "no flags",
			input:              "order.*",
			wantPattern:        "order.*",
			wantCaseInsensitive: false,
		},
		{
			name:               "empty pattern",
			input:              "",
			wantPattern:        "",
			wantCaseInsensitive: false,
		},
		{
			name:               "pattern with special chars",
			input:              `\d+`,
			wantPattern:        `\d+`,
			wantCaseInsensitive: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPattern, gotCI := convertLokiRegex(tt.input)
			if gotPattern != tt.wantPattern {
				t.Errorf("pattern = %q, want %q", gotPattern, tt.wantPattern)
			}
			if gotCI != tt.wantCaseInsensitive {
				t.Errorf("caseInsensitive = %v, want %v", gotCI, tt.wantCaseInsensitive)
			}
		})
	}
}
