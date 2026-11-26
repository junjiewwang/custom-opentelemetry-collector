// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package opsconfig

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// ErrValidation marks a client-side validation failure so the admin API can
// return 400 instead of 503.
var ErrValidation = errors.New("opsconfig validation failed")

// Validator checks a value before it is published. New runtime config keys are
// added by registering a validator, without changing Store or the admin API.
type Validator func(value string) error

// SchemaEntry describes a supported config key for the admin UI and validation.
type SchemaEntry struct {
	Key         string `json:"key"`
	Description string `json:"description"`
	ValueType   string `json:"value_type"`
}

var (
	schemaMu sync.RWMutex
	schema   = map[string]schemaItem{
		KeyTunnelBaseURL: {
			entry: SchemaEntry{
				Key:         KeyTunnelBaseURL,
				Description: "Base URL for deriving the tunnel endpoint: scheme://host[:port][/path prefix]. The probe appends /v1/arthas/ws; use it when control-plane and HTTP hosts differ",
				ValueType:   "base_url",
			},
			validate: validateTunnelBaseURL,
		},
	}
)

type schemaItem struct {
	entry    SchemaEntry
	validate Validator
}

// RegisterKey extends the runtime config schema. It is intended for startup
// registration; registering the same key twice is rejected to catch mistakes.
func RegisterKey(entry SchemaEntry, validate Validator) error {
	if entry.Key == "" || validate == nil {
		return errors.New("opsconfig: key and validator are required")
	}
	schemaMu.Lock()
	defer schemaMu.Unlock()
	if _, exists := schema[entry.Key]; exists {
		return fmt.Errorf("opsconfig: key %q already registered", entry.Key)
	}
	schema[entry.Key] = schemaItem{entry: entry, validate: validate}
	return nil
}

// Schema returns a stable, sorted snapshot of all supported keys.
func Schema() []SchemaEntry {
	schemaMu.RLock()
	defer schemaMu.RUnlock()
	out := make([]SchemaEntry, 0, len(schema))
	for _, item := range schema {
		out = append(out, item.entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// schemaHasKey reports whether the key is registered.
func schemaHasKey(key string) bool {
	schemaMu.RLock()
	_, ok := schema[key]
	schemaMu.RUnlock()
	return ok
}

// Validate dispatches to the validator registered for the key.
func Validate(key, value string) error {
	schemaMu.RLock()
	item, ok := schema[key]
	schemaMu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: unsupported key %q", ErrValidation, key)
	}
	if err := item.validate(value); err != nil {
		return fmt.Errorf("%w: %v", ErrValidation, err)
	}
	return nil
}

// validateTunnelBaseURL validates the single supported override. Unlike the full-URL key,
// an optional path prefix is allowed (reverse-proxy deployments put it here),
// but the fixed tunnel path must NOT be included — the probe appends it.
func validateTunnelBaseURL(value string) error {
	if len(value) == 0 || len(value) > maxValueLength {
		return errors.New("value must be non-empty and within the length limit")
	}
	u, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	switch u.Scheme {
	case "http", "https", "ws", "wss":
	default:
		return errors.New("base URL scheme must be http, https, ws or wss")
	}
	if u.Host == "" || u.Hostname() == "" || u.User != nil {
		return errors.New("base URL host is required and userinfo is not allowed")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("query and fragment are not allowed")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return errors.New("base URL port must be between 1 and 65535")
		}
	}
	if strings.HasSuffix(strings.TrimSuffix(u.Path, "/"), expectedTunnelPath) {
		return fmt.Errorf("base URL must not contain %s; the probe appends it", expectedTunnelPath)
	}
	if strings.HasSuffix(u.Path, "/") {
		return errors.New("base URL path prefix must not end with '/'")
	}
	return nil
}
