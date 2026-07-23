package adminext

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

func TestMergeLogResults_SingleBranch(t *testing.T) {
	branch := &observabilitystorageext.LogSearchResult{
		Logs: []observabilitystorageext.LogRecord{
			{TimeUnixNano: "100", Body: "error 1"},
			{TimeUnixNano: "200", Body: "error 2"},
		},
		Total: 2,
	}
	merged := mergeLogResults([]*observabilitystorageext.LogSearchResult{branch}, 100, "backward")
	assert.Len(t, merged.Logs, 2)
	assert.Equal(t, int64(2), merged.Total)
}

func TestMergeLogResults_Dedup(t *testing.T) {
	b1 := &observabilitystorageext.LogSearchResult{
		Logs: []observabilitystorageext.LogRecord{
			{TimeUnixNano: "100", Body: "shared error"},
			{TimeUnixNano: "200", Body: "unique-b1"},
		},
	}
	b2 := &observabilitystorageext.LogSearchResult{
		Logs: []observabilitystorageext.LogRecord{
			{TimeUnixNano: "100", Body: "shared error"},
			{TimeUnixNano: "300", Body: "unique-b2"},
		},
	}
	merged := mergeLogResults([]*observabilitystorageext.LogSearchResult{b1, b2}, 100, "backward")
	assert.Len(t, merged.Logs, 3, fmt.Sprintf("got: %+v", merged.Logs))
	assert.Equal(t, int64(3), merged.Total)
}

func TestMergeLogResults_SortBackward(t *testing.T) {
	b1 := &observabilitystorageext.LogSearchResult{
		Logs: []observabilitystorageext.LogRecord{
			{TimeUnixNano: "100", Body: "old"},
		},
	}
	b2 := &observabilitystorageext.LogSearchResult{
		Logs: []observabilitystorageext.LogRecord{
			{TimeUnixNano: "300", Body: "new"},
		},
	}
	b3 := &observabilitystorageext.LogSearchResult{
		Logs: []observabilitystorageext.LogRecord{
			{TimeUnixNano: "200", Body: "mid"},
		},
	}
	merged := mergeLogResults([]*observabilitystorageext.LogSearchResult{b1, b2, b3}, 100, "backward")
	assert.Equal(t, "new", merged.Logs[0].Body)
	assert.Equal(t, "mid", merged.Logs[1].Body)
	assert.Equal(t, "old", merged.Logs[2].Body)
}

func TestMergeLogResults_SortForward(t *testing.T) {
	b1 := &observabilitystorageext.LogSearchResult{
		Logs: []observabilitystorageext.LogRecord{
			{TimeUnixNano: "300", Body: "new"},
		},
	}
	b2 := &observabilitystorageext.LogSearchResult{
		Logs: []observabilitystorageext.LogRecord{
			{TimeUnixNano: "100", Body: "old"},
		},
	}
	merged := mergeLogResults([]*observabilitystorageext.LogSearchResult{b1, b2}, 100, "forward")
	assert.Equal(t, "old", merged.Logs[0].Body)
	assert.Equal(t, "new", merged.Logs[1].Body)
}

func TestMergeLogResults_AllEmpty(t *testing.T) {
	b1 := &observabilitystorageext.LogSearchResult{}
	b2 := &observabilitystorageext.LogSearchResult{}
	merged := mergeLogResults([]*observabilitystorageext.LogSearchResult{b1, b2}, 100, "backward")
	assert.Len(t, merged.Logs, 0)
}

func TestMergeLogResults_NilBranches(t *testing.T) {
	merged := mergeLogResults([]*observabilitystorageext.LogSearchResult{nil, nil}, 100, "backward")
	assert.Len(t, merged.Logs, 0)
}

func TestMergeLogResults_EmptyInput(t *testing.T) {
	merged := mergeLogResults(nil, 100, "backward")
	assert.Len(t, merged.Logs, 0)
}

func TestMergeMetricResults_SingleBranch(t *testing.T) {
	series := observabilitystorageext.LogMetricSeries{
		Labels: map[string]string{"level": "ERROR"},
		Values: []observabilitystorageext.LogMetricValue{{TimestampNano: 100, Value: 5}},
	}
	branch := &observabilitystorageext.LogMetricResult{Series: []observabilitystorageext.LogMetricSeries{series}}
	merged := mergeMetricResults([]*observabilitystorageext.LogMetricResult{branch})
	assert.Len(t, merged.Series, 1)
}

func TestMergeMetricResults_MergeSeries(t *testing.T) {
	b1 := &observabilitystorageext.LogMetricResult{
		Series: []observabilitystorageext.LogMetricSeries{{
			Labels: map[string]string{"level": "ERROR"},
			Values: []observabilitystorageext.LogMetricValue{{TimestampNano: 100, Value: 10}},
		}},
	}
	b2 := &observabilitystorageext.LogMetricResult{
		Series: []observabilitystorageext.LogMetricSeries{{
			Labels: map[string]string{"level": "ERROR"},
			Values: []observabilitystorageext.LogMetricValue{
				{TimestampNano: 100, Value: 5},
				{TimestampNano: 200, Value: 3},
			},
		}},
	}
	merged := mergeMetricResults([]*observabilitystorageext.LogMetricResult{b1, b2})
	assert.Len(t, merged.Series, 1)
	assert.Len(t, merged.Series[0].Values, 2)
	assert.Equal(t, int64(100), merged.Series[0].Values[0].TimestampNano)
	assert.InDelta(t, float64(15), merged.Series[0].Values[0].Value, 0.01)
	assert.Equal(t, int64(200), merged.Series[0].Values[1].TimestampNano)
}

func TestMergeMetricResults_DifferentLabels(t *testing.T) {
	b1 := &observabilitystorageext.LogMetricResult{
		Series: []observabilitystorageext.LogMetricSeries{
			{Labels: map[string]string{"level": "ERROR"}, Values: []observabilitystorageext.LogMetricValue{{TimestampNano: 100, Value: 1}}},
		},
	}
	b2 := &observabilitystorageext.LogMetricResult{
		Series: []observabilitystorageext.LogMetricSeries{
			{Labels: map[string]string{"level": "WARN"}, Values: []observabilitystorageext.LogMetricValue{{TimestampNano: 200, Value: 2}}},
		},
	}
	merged := mergeMetricResults([]*observabilitystorageext.LogMetricResult{b1, b2})
	assert.Len(t, merged.Series, 2)
}

func TestMergeMetricResults_NilBranches(t *testing.T) {
	merged := mergeMetricResults([]*observabilitystorageext.LogMetricResult{nil, nil})
	assert.Len(t, merged.Series, 0)
}
