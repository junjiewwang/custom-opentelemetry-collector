package elasticsearch

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestGetTagValues_FieldNameConstruction verifies that GetTagValues uses
// .keyword suffix correctly for text fields. This is a unit-level test
// of the field name logic — does NOT require ES connection.
func TestGetTagValues_FieldNameConstruction(t *testing.T) {
	tests := []struct {
		tagKey   string
		scope    string
		wantBase string
		wantKW   bool
	}{
		// Span attributes → always text → needs .keyword
		{tagKey: "db.name", scope: "span", wantBase: "attributes.db.name", wantKW: true},
		{tagKey: "http.method", scope: "span", wantBase: "attributes.http.method", wantKW: true},
		{tagKey: "peer.service", scope: "span", wantBase: "attributes.peer.service", wantKW: true},

		// Resource text fields → needs .keyword
		{tagKey: "app_id", scope: "resource", wantBase: "resource.app_id", wantKW: true},

		// Resource keyword fields → NO .keyword
		{tagKey: "service.name", scope: "resource", wantBase: "resource.service.name", wantKW: false},
		{tagKey: "host.name", scope: "resource", wantBase: "resource.host.name", wantKW: false},
		{tagKey: "process.pid", scope: "resource", wantBase: "resource.process.pid", wantKW: false},
	}

	for _, tt := range tests {
		t.Run(tt.scope+"."+tt.tagKey, func(t *testing.T) {
			fieldPrefix := FieldAttributes
			if tt.scope == "resource" {
				fieldPrefix = FieldResource
			}
			fieldName := fieldPrefix + "." + tt.tagKey

			assert.Equal(t, tt.wantBase, fieldName)

			// Same logic as GetTagValues (trace signal).
			fieldName = aggregatableField("trace", fieldName)

			if tt.wantKW {
				assert.Contains(t, fieldName, ".keyword",
					"text field must get .keyword suffix")
			} else {
				assert.NotContains(t, fieldName, ".keyword",
					"keyword field must NOT get .keyword suffix")
			}
			assert.NotContains(t, fieldName, ".keyword.keyword",
				"field name should not get double .keyword")
		})
	}
}
