// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEventFieldPath verifies event-scoped keys resolve to their ES path inside
// the nested events document. Attributes live under events.attributes.<key> and
// come from the dynamic text+keyword template, so exact matching needs the
// .keyword sub-field. Only name and the timestamp are explicitly-mapped
// top-level nested fields.
func TestEventFieldPath(t *testing.T) {
	assert.Equal(t, "events.attributes.exception.type.keyword", eventFieldPath("exception.type"))
	assert.Equal(t, "events.attributes.exception.message.keyword", eventFieldPath("exception.message"))
	assert.Equal(t, "events.name", eventFieldPath("name"))
	assert.Equal(t, "events.timeUnixNano", eventFieldPath(FieldLogTimeUnixNano))
}

// TestBuildTraceSearchQuery_EventTagPath guards two regressions that each broke
// event filters: targeting events.<key> instead of events.attributes.<key>, and
// omitting .keyword so a term query ran against the analyzed text field and
// never matched a dotted class name.
func TestBuildTraceSearchQuery_EventTagPath(t *testing.T) {
	r := newTestTraceReader(&fakeSearcher{})
	q := TraceQuery{EventTags: []map[string]string{{"exception.type": "java.lang.IllegalStateException"}}}

	got := r.buildTraceSearchQuery(q)
	raw, err := json.Marshal(got)
	require.NoError(t, err)

	assert.Contains(t, string(raw), `"events.attributes.exception.type.keyword":"java.lang.IllegalStateException"`)
	assert.NotContains(t, string(raw), `"events.exception.type"`)
	// The clause must still be wrapped in a nested query on the events path.
	assert.Contains(t, string(raw), `"nested"`)
	assert.Contains(t, string(raw), `"path":"events"`)
}

// TestBuildTraceSearchQuery_EventTagsOrPath verifies the OR-group path gets the
// same field resolution as the AND path.
func TestBuildTraceSearchQuery_EventTagsOrPath(t *testing.T) {
	r := newTestTraceReader(&fakeSearcher{})
	q := TraceQuery{
		EventTagsOr: [][][]map[string]string{
			{
				{{"exception.type": "A"}},
				{{"exception.type": "B"}},
			},
		},
	}

	got := r.buildTraceSearchQuery(q)
	raw, err := json.Marshal(got)
	require.NoError(t, err)

	assert.Contains(t, string(raw), `"events.attributes.exception.type.keyword":"A"`)
	assert.Contains(t, string(raw), `"events.attributes.exception.type.keyword":"B"`)
	assert.NotContains(t, string(raw), `"events.exception.type"`)
}

// TestBuildTraceSearchQuery_EventNameStaysTopLevel verifies event:name is not
// pushed under attributes.
func TestBuildTraceSearchQuery_EventNameStaysTopLevel(t *testing.T) {
	r := newTestTraceReader(&fakeSearcher{})
	q := TraceQuery{EventTags: []map[string]string{{"name": "exception"}}}

	got := r.buildTraceSearchQuery(q)
	raw, err := json.Marshal(got)
	require.NoError(t, err)

	assert.Contains(t, string(raw), `"events.name":"exception"`)
	assert.NotContains(t, string(raw), `"events.attributes.name"`)
}
