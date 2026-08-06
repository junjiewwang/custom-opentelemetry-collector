// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package observabilitystorageext

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestToStoredTraceQuery_CopiesEveryFilter guards the regression where the
// adapter's hand-written struct literal omitted EventTags/EventTagsOr (and
// several other filters). The filters were parsed and planned correctly, then
// silently dropped on the way to the provider, so event-scoped TraceQL queries
// behaved as match-all instead of filtering.
func TestToStoredTraceQuery_CopiesEveryFilter(t *testing.T) {
	in := TraceQuery{
		AppID:         "app",
		ServiceName:   "gateway",
		OperationName: "GET /checkout",
		Tags:          map[string]string{"http.method": "GET"},
		TagsOr:        [][]map[string]string{{{"a": "1"}}},
		TagsNot:       map[string]string{"env": "dev"},
		TagsNotOr:     [][]map[string]string{{{"b": "2"}}},
		TagsExists:    []string{"exists.key"},
		TagsNotExists: []string{"missing.key"},
		TagsRegex:     map[string]string{"name": ".*"},
		TagsRegexOr:   [][]map[string]string{{{"c": ".*"}}},
		EventTags:     []map[string]string{{"exception.type": "Boom"}},
		EventTagsOr:   [][][]map[string]string{{{{"exception.type": "A"}}}},
		MinDuration:   time.Second,
		MaxDuration:   2 * time.Second,
		TimeRange:     TimeRange{Start: time.Unix(1, 0), End: time.Unix(2, 0)},
		Limit:         20,
		Offset:        5,
		SpanKind:      "server",
		Status:        "error",
		IsRoot:        true,
		RootName:      "GET /api",
		RootService:   "gw",
	}

	got := toStoredTraceQuery(in)

	assert.Equal(t, in.EventTags, got.EventTags)
	assert.Equal(t, in.EventTagsOr, got.EventTagsOr)
	assert.Equal(t, in.TagsNotOr, got.TagsNotOr)
	assert.Equal(t, in.TagsRegexOr, got.TagsRegexOr)
	assert.Equal(t, in.TagsNotExists, got.TagsNotExists)
	assert.Equal(t, in.Tags, got.Tags)
	assert.Equal(t, in.RootService, got.RootService)
	assert.Equal(t, in.TimeRange.Start, got.TimeRange.Start)
	assert.Equal(t, in.TimeRange.End, got.TimeRange.End)

	// Catch fields added to the public struct but not wired into the converter:
	// with every input field set to a non-zero value, no output field may be zero.
	out := reflect.ValueOf(got)
	for i := 0; i < out.NumField(); i++ {
		name := out.Type().Field(i).Name
		require.Falsef(t, out.Field(i).IsZero(),
			"field %s is zero — toStoredTraceQuery is missing it", name)
	}
}

// TestToStoredTraceQuery_FieldParity fails when the two structs drift, so a new
// filter cannot be added to the public type without wiring it through.
func TestToStoredTraceQuery_FieldParity(t *testing.T) {
	pub := reflect.TypeOf(TraceQuery{})
	stored := reflect.TypeOf(toStoredTraceQuery(TraceQuery{}))

	storedFields := make(map[string]bool, stored.NumField())
	for i := 0; i < stored.NumField(); i++ {
		storedFields[stored.Field(i).Name] = true
	}
	for i := 0; i < pub.NumField(); i++ {
		name := pub.Field(i).Name
		assert.Truef(t, storedFields[name],
			"public TraceQuery.%s has no storedmodel counterpart", name)
	}
}
