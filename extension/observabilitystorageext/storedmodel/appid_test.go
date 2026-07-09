// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package storedmodel

import (
	"testing"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

func TestSanitizeAppID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		opts SanitizeOptions
		want string
	}{
		{
			name: "empty string",
			id:   "",
			opts: SanitizeOptions{},
			want: "",
		},
		{
			name: "pure base62 unaffected",
			id:   "xUXCbjcSnSy5LZUJ",
			opts: SanitizeOptions{Lowercase: false},
			want: "xUXCbjcSnSy5LZUJ",
		},
		{
			name: "pure base62 with lowercase option",
			id:   "xUXCbjcSnSy5LZUJ",
			opts: SanitizeOptions{Lowercase: true},
			want: "xuxcbjcsnsy5lzuj",
		},
		{
			name: "space replaced",
			id:   "my app",
			opts: SanitizeOptions{},
			want: "my-app",
		},
		{
			name: "slashes and backslash replaced",
			id:   "a/b\\c",
			opts: SanitizeOptions{},
			want: "a-b-c",
		},
		{
			name: "wildcard characters replaced",
			id:   "a*b?c",
			opts: SanitizeOptions{},
			want: "a-b-c",
		},
		{
			name: "quotes and angle brackets removed",
			id:   `a"b<c>d`,
			opts: SanitizeOptions{},
			want: "abcd",
		},
		{
			name: "pipe hash comma replaced",
			id:   "a|b#c,d",
			opts: SanitizeOptions{},
			want: "a-b-c-d",
		},
		{
			name: "mixed special chars with lowercase",
			id:   `My App/Test*1`,
			opts: SanitizeOptions{Lowercase: true},
			want: "my-app-test-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeAppID(tt.id, tt.opts)
			if got != tt.want {
				t.Errorf("SanitizeAppID(%q, %+v) = %q, want %q", tt.id, tt.opts, got, tt.want)
			}
		})
	}
}

func TestExtractAppID(t *testing.T) {
	tests := []struct {
		name  string
		build func() pcommon.Map
		want  string
	}{
		{
			name: "no attributes",
			build: func() pcommon.Map {
				return pcommon.NewMap()
			},
			want: "",
		},
		{
			name: "app_id present",
			build: func() pcommon.Map {
				m := pcommon.NewMap()
				m.PutStr("app_id", "xUXCbjcSnSy5LZUJ")
				return m
			},
			want: "xUXCbjcSnSy5LZUJ",
		},
		{
			name: "app.id fallback when app_id absent",
			build: func() pcommon.Map {
				m := pcommon.NewMap()
				m.PutStr("app.id", "fallbackID")
				return m
			},
			want: "fallbackID",
		},
		{
			name: "app_id takes precedence over app.id",
			build: func() pcommon.Map {
				m := pcommon.NewMap()
				m.PutStr("app_id", "primaryID")
				m.PutStr("app.id", "fallbackID")
				return m
			},
			want: "primaryID",
		},
		{
			name: "empty app_id falls back to app.id",
			build: func() pcommon.Map {
				m := pcommon.NewMap()
				m.PutStr("app_id", "")
				m.PutStr("app.id", "fallbackID")
				return m
			},
			want: "fallbackID",
		},
		{
			name: "result not sanitized",
			build: func() pcommon.Map {
				m := pcommon.NewMap()
				m.PutStr("app_id", "my app/test")
				return m
			},
			want: "my app/test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractAppID(tt.build())
			if got != tt.want {
				t.Errorf("ExtractAppID() = %q, want %q", got, tt.want)
			}
		})
	}
}
