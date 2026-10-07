// These tests compile complete-value codecs and the catalog schema from one
// flat object union. The codec must reject malformed input before constructing
// a service value, and encoding must leave the caller's selected branch intact.
package jsoncodec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	contractschema "goa.design/goa-ai/codegen/internal/jsonschema"
	validator "goa.design/goa-ai/internal/jsonschema"
	"goa.design/goa/v3/dsl"
	"goa.design/goa/v3/expr"
)

func TestGeneratedFlatUnionCodecAndSchema(t *testing.T) {
	var outcome expr.UserType
	files, err := generate(t, func() {
		complete := located("Complete", func() {
			dsl.Attribute("reference", dsl.String, func() { dsl.MinLength(1) })
			dsl.Required("reference")
		})
		pending := located("Pending", func() {
			dsl.Attribute("state", dsl.String, func() { dsl.MinLength(1) })
			dsl.Required("state")
		})
		outcome = located("Outcome", func() {
			dsl.OneOf("outcome", func() {
				dsl.TypeName("ResultChoice")
				dsl.Meta("oneof:json:flatten")
				dsl.Meta("oneof:type:field", "resultType")
				dsl.Attribute("complete", complete)
				dsl.Attribute("input_required", pending)
			})
			dsl.Required("outcome")
		})
		located("Results", func() {
			dsl.Attribute("items", dsl.ArrayOf(outcome))
			dsl.Attribute("named", dsl.MapOf(dsl.String, outcome))
			dsl.Required("items", "named")
		})
	})
	require.NoError(t, err)
	method := &expr.MethodExpr{Name: "finish", Service: &expr.ServiceExpr{Name: "operations"}}
	schema, err := contractschema.Build(expr.Root.API, outcome.Attribute(), expr.MethodResultExampleIdentity(method))
	require.NoError(t, err)
	compiled, err := validator.Compile(schema)
	require.NoError(t, err)
	for _, test := range []struct {
		value string
		valid bool
	}{
		{`{"outcome":{"resultType":"complete","reference":"done"}}`, true},
		{`{"outcome":{"resultType":"input_required","state":"next"}}`, true},
		{`{"outcome":{"resultType":"complete","reference":""}}`, false},
		{`{"outcome":{"resultType":"complete","state":"next"}}`, false},
		{`{"outcome":{"resultType":"unknown","reference":"done"}}`, false},
		{`{"outcome":{"resultType":"complete","reference":"done","extra":true}}`, false},
		{`{"outcome":{"resultType":"complete","value":{"reference":"done"}}}`, false},
	} {
		err := validator.Validate(compiled, []byte(test.value))
		if test.valid {
			require.NoError(t, err, test.value)
		} else {
			require.Error(t, err, test.value)
		}
	}
	root := compileModule(t, files)
	source, err := os.ReadFile("testdata/flat_union_test.go")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/types/flat_union_test.go"), source, 0o600)) // #nosec G703 -- root is this fixture's private directory.
	runGo(t, root, "test", "-count=1", "./gen/...")
}
