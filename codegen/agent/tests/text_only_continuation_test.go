// This test executes generated and portable codecs to compare the cursor rules
// of first-page and continuation calls when optional UI output is disabled.
package tests

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	"goa.design/goa-ai/codegen/testhelpers"
)

func TestGeneratedTextOnlyContinuationExecutionContracts(t *testing.T) {
	files := testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", testscenarios.TextOnlyContinuationInputCodecs())
	root := writeGeneratedModule(t, files)
	writeGeneratedPackageTest(t, root, "alpha/toolsets/lookup/http/validate.go", fileContent(t, files, "gen/alpha/toolsets/lookup/http/validate.go"))
	writeGeneratedPackageTest(t, root, "alpha/toolsets/lookup/text_only_continuation_test.go", `package lookup

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry/contract"
)

func TestTextOnlyCursorRulesMatchOrdinaryExecution(t *testing.T) {
	remote := make(map[tools.Ident]tools.ToolSpec)
	for _, declaration := range ToolSchemas() {
		spec, err := contract.Compile(declaration)
		require.NoError(t, err)
		remote[spec.Name] = spec
	}
	for _, test := range []struct {
		name string
		spec tools.ToolSpec
		valid string
		textJSON string
		invalid []string
	}{
		{
			name: "source",
			spec: SpecRead(),
			valid: "{\"query\":\"records\"}",
			textJSON: "{\"query\":\"records\",\"render_ui\":false}",
			invalid: []string{"{\"query\":\"records\",\"cursor\":\"page\"}"},
		},
		{
			name: "continuation",
			spec: SpecContinueRead(),
			valid: "{\"query\":\"records\",\"cursor\":\"page\"}",
			textJSON: "{\"query\":\"records\",\"cursor\":\"page\",\"render_ui\":false}",
			invalid: []string{"{}", "{\"query\":\"records\"}"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			portable, found := remote[test.spec.Name]
			require.True(t, found)
			for _, spec := range []tools.ToolSpec{test.spec, portable} {
				for _, selected := range []struct {
					spec tools.ToolSpec
					expected string
				}{
					{spec, test.valid},
					{spec.ForTextOnly(), test.textJSON},
				} {
					value, err := selected.spec.ExecutionPayloadCodec.FromJSON([]byte(test.valid))
					require.NoError(t, err)
					encoded, err := selected.spec.ExecutionPayloadCodec.ToJSON(value)
					require.NoError(t, err)
					assert.JSONEq(t, selected.expected, string(encoded))
					for _, invalid := range test.invalid {
						_, err := selected.spec.ExecutionPayloadCodec.FromJSON([]byte(invalid))
						assert.Error(t, err, "%s accepted %s", selected.spec.Name, invalid)
					}
				}
			}
		})
	}
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "./alpha/toolsets/lookup"))
}
