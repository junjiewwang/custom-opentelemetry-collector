package elasticsearch

import (
	"encoding/json"
	"flag"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

var updateGolden = flag.Bool("update", false, "update golden files")

func TestBuildTraceSearchQuery_GoldenSnapshot(t *testing.T) {
	reader := &TraceReader{logger: zap.NewNop()}

	tests := []struct {
		name  string
		query TraceQuery
	}{
		{
			name: "root_error_spans",
			query: TraceQuery{
				IsRoot: true,
				Status: "error",
			},
		},
		{
			name: "server_kind_with_service",
			query: TraceQuery{
				SpanKind:    "server",
				ServiceName: "test-service",
			},
		},
		{
			name: "complex_filter",
			query: TraceQuery{
				ServiceName: "test-service",
				SpanKind:    "server",
				Status:      "error",
				IsRoot:      true,
				MinDuration: time.Second,
				MaxDuration: 10 * time.Second,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.query.TimeRange = TimeRange{
				Start: time.Unix(0, 1784793600000000000),
				End:   time.Unix(0, 1784797200000000000),
			}

			esQuery := reader.buildTraceSearchQuery(tt.query)
			got, err := json.MarshalIndent(esQuery, "", "  ")
			require.NoError(t, err)

			goldenPath := "testdata/trace_search_" + tt.name + ".json"
			if *updateGolden {
				err := os.WriteFile(goldenPath, got, 0644)
				require.NoError(t, err)
				t.Logf("updated golden: %s", goldenPath)
				return
			}

			want, err := os.ReadFile(goldenPath)
			require.NoError(t, err, "golden file not found: %s (run with -update to generate)", goldenPath)

			assert.JSONEq(t, string(want), string(got),
				"ES query structure changed for %s. Review diff, if intentional run: go test -update -run TestBuildTraceSearchQuery", tt.name)
		})
	}
}
