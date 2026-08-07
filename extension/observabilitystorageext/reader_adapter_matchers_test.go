package observabilitystorageext

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/elasticsearch"
)

// The metric adapters copy field-by-field into the Elasticsearch query structs,
// so a matcher added to one side is silently dropped until someone notices the
// filter does nothing. That is exactly how LabelNot/LabelNotMatch (and
// LabelMatch on the instant path) came to be ignored: the query parsed fine,
// the ES call succeeded, and the filter was simply absent.
//
// Assert structurally that every matcher field present on the public query type
// also exists on the Elasticsearch counterpart, so the copy has somewhere to go.
func TestMetricQueryStructs_CarryAllMatcherFields(t *testing.T) {
	matcherFields := []string{"Labels", "LabelMatch", "LabelNot", "LabelNotMatch"}

	pairs := []struct {
		name string
		pub  any
		es   any
	}{
		{"MetricQuery", MetricQuery{}, elasticsearch.MetricQuery{}},
		{"MetricRangeQuery", MetricRangeQuery{}, elasticsearch.MetricRangeQuery{}},
		{"MetricRawQuery", MetricRawQuery{}, elasticsearch.MetricRawQuery{}},
		{"MetricFlatQuery", MetricFlatQuery{}, elasticsearch.MetricFlatQuery{}},
	}

	for _, p := range pairs {
		t.Run(p.name, func(t *testing.T) {
			pubType := reflect.TypeOf(p.pub)
			esType := reflect.TypeOf(p.es)

			for _, f := range matcherFields {
				if _, ok := pubType.FieldByName(f); !ok {
					continue // not part of this query's contract
				}
				_, ok := esType.FieldByName(f)
				assert.True(t, ok,
					"%s has %s but elasticsearch.%s does not — the adapter cannot forward it",
					p.name, f, p.name)
			}
		})
	}
}

// Guard the copy itself, not just the shape: a present-but-unassigned field is
// the same silent-filter bug. metricReaderAdapter wraps a concrete
// *elasticsearch.MetricReader (not an interface), so the copy cannot be
// observed through a fake; assert on the adapter source instead — every
// matcher field must appear on both sides of each struct literal.
func TestMetricAdapters_AssignAllMatchers(t *testing.T) {
	src, err := os.ReadFile("reader_adapter.go")
	require.NoError(t, err)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "reader_adapter.go", src, 0)
	require.NoError(t, err)

	// Struct literal type name -> matcher fields it must assign.
	want := map[string][]string{
		"MetricQuery":      {"Labels", "LabelMatch", "LabelNot", "LabelNotMatch"},
		"MetricRangeQuery": {"Labels", "LabelMatch", "LabelNot", "LabelNotMatch"},
		"MetricRawQuery":   {"Labels", "LabelMatch", "LabelNot", "LabelNotMatch"},
		"MetricFlatQuery":  {"Labels", "LabelMatch", "LabelNot", "LabelNotMatch"},
	}
	found := map[string]map[string]bool{}

	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "elasticsearch" {
			return true
		}
		if _, tracked := want[sel.Sel.Name]; !tracked {
			return true
		}
		assigned := map[string]bool{}
		for _, elt := range lit.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				if k, ok := kv.Key.(*ast.Ident); ok {
					assigned[k.Name] = true
				}
			}
		}
		found[sel.Sel.Name] = assigned
		return true
	})

	for typeName, fields := range want {
		assigned, ok := found[typeName]
		require.True(t, ok, "no elasticsearch.%s literal found in reader_adapter.go", typeName)
		for _, f := range fields {
			assert.True(t, assigned[f],
				"elasticsearch.%s literal does not assign %s — the matcher is silently dropped",
				typeName, f)
		}
	}
}
