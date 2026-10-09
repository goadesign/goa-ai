// These checks generate strict codecs and schemas from the same raw union
// declarations. Each accepted wire value must retain its selected typed branch.
package jsoncodec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	contractschema "goa.design/goa-ai/codegen/jsonschema"
	validator "goa.design/goa-ai/internal/jsonschema"
	"goa.design/goa/v3/dsl"
	"goa.design/goa/v3/expr"
)

func TestGeneratedUntaggedUnionCodecAndSchema(t *testing.T) {
	var entry expr.UserType
	files, err := generate(t, func() {
		file := located("File", func() {
			dsl.Attribute("uri", dsl.String, "Exact file address.", func() {
				dsl.Meta("struct:field:name", "Address")
				dsl.MinLength(1)
			})
			dsl.Required("uri")
		})
		entry = located("Entry", func() {
			dsl.OneOf("resources", "A complete manifest or dynamic content.", func() {
				dsl.TypeName("Resources")
				dsl.Meta("oneof:json:untagged")
				dsl.Attribute("manifest", dsl.ArrayOfRequired(file))
				dsl.Attribute("dynamic", dsl.String, func() { dsl.Enum("dynamic") })
			})
			dsl.Required("resources")
		})
		located("Selection", func() {
			dsl.OneOf("value", "One value selected by JSON kind.", func() {
				dsl.TypeName("Choice")
				dsl.Meta("oneof:json:untagged")
				dsl.Attribute("text", dsl.String)
				dsl.Attribute("number", dsl.Int64)
				dsl.Attribute("enabled", dsl.Boolean)
				dsl.Attribute("record", file)
				dsl.Attribute("files", dsl.ArrayOfRequired(file))
			})
			dsl.Required("value")
		})
		located("Binary", func() {
			dsl.OneOf("content", "Bytes or a string-keyed map.", func() {
				dsl.TypeName("BinaryChoice")
				dsl.Meta("oneof:json:untagged")
				dsl.Attribute("data", dsl.Bytes)
				dsl.Attribute("mapping", dsl.MapOf(dsl.String, dsl.Int64))
			})
			dsl.Required("content")
		})
	})
	require.NoError(t, err)
	method := &expr.MethodExpr{Name: "read", Service: &expr.ServiceExpr{Name: "records"}}
	schema, err := contractschema.Build(expr.Root.API, entry.Attribute(), expr.MethodResultExampleIdentity(method))
	require.NoError(t, err)
	compiled, err := validator.Compile(schema)
	require.NoError(t, err)
	for _, test := range []struct {
		value string
		valid bool
	}{
		{`{"resources":"dynamic"}`, true},
		{`{"resources":[]}`, true},
		{`{"resources":[{"uri":"file://record"}]}`, true},
		{`{"resources":"other"}`, false},
		{`{"resources":null}`, false},
		{`{"resources":true}`, false},
		{`{"resources":[null]}`, false},
		{`{"resources":[{"uri":""}]}`, false},
		{`{"resources":[{"uri":"file://record","extra":true}]}`, false},
		{`{"resources":{"type":"dynamic","value":"dynamic"}}`, false},
	} {
		err := validator.Validate(compiled, []byte(test.value))
		if test.valid {
			require.NoError(t, err, test.value)
		} else {
			require.Error(t, err, test.value)
		}
	}
	root := compileModule(t, files)
	source, err := os.ReadFile("testdata/untagged_union_test.go")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/types/untagged_union_test.go"), source, 0o600)) // #nosec G703 -- root is this fixture's private directory.
	runGo(t, root, "test", "-count=1", "./gen/...")
}
