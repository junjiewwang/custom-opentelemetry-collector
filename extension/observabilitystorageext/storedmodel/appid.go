// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package storedmodel

import (
	"strings"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

// SanitizeOptions controls provider-specific AppID sanitization behavior.
type SanitizeOptions struct {
	// Lowercase forces the result to lowercase. ES does NOT require this
	// (verified against production cluster, see
	// docs/2026-07-09/appid-sanitize-unification-design.md §2.1: ES 7.10.1
	// allows uppercase index names; production Trace indices are stored
	// with mixed-case AppIDs). Kept as an option for forward-compatibility
	// with providers that do enforce lowercase.
	Lowercase bool
}

// appIDReplacer replaces characters that are unsafe for storage provider
// identifiers (index names, table names, etc.). The character set is the
// union of the two historical implementations this replaces:
// storedmodel's own (space / \ * ? " < >) and elasticsearch/model.go's
// extra (| # ,), so no coverage is lost when they are unified.
var appIDReplacer = strings.NewReplacer(
	" ", "-", "/", "-", "\\", "-",
	"*", "-", "?", "-",
	"\"", "", "<", "", ">", "",
	"|", "-", "#", "-", ",", "-",
)

// SanitizeAppID replaces characters that are unsafe for storage provider
// identifiers (index names, table names, etc.) and optionally lowercases
// the result. This is the single, shared implementation used by all
// Providers (ES, PostgreSQL, ...) — do not re-implement per provider.
func SanitizeAppID(id string, opts SanitizeOptions) string {
	if opts.Lowercase {
		id = strings.ToLower(id)
	}
	return appIDReplacer.Replace(id)
}

// ExtractAppID reads the app_id/app.id resource attribute without any
// sanitization. Callers needing a storage-safe form should call
// SanitizeAppID explicitly on the result.
func ExtractAppID(attrs pcommon.Map) string {
	for _, key := range []string{"app_id", "app.id"} {
		if val, ok := attrs.Get(key); ok {
			if id := val.AsString(); id != "" {
				return id
			}
		}
	}
	return ""
}
