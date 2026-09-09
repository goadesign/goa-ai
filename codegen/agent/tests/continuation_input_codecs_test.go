package tests

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	"goa.design/goa-ai/codegen/testhelpers"
)

// TestGeneratedContinuationInputCodecs constructs the real generated tool
// definition, including its authored example, before any model call is made.
func TestGeneratedContinuationInputCodecs(t *testing.T) {
	files := testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", testscenarios.ContinuationInputCodecs())
	root := writeGeneratedModule(t, files)
	writeGeneratedPackageTest(t, root, "alpha/toolsets/lookup/http/validate.go", fileContent(t, files, "gen/alpha/toolsets/lookup/http/validate.go"))
	writeGeneratedPackageTest(t, root, "alpha/toolsets/lookup/continuation_input_test.go", `package lookup

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/runtime"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestContinuationExampleMatchesModelInput(t *testing.T) {
	for _, spec := range []tools.ToolSpec{SpecContinueSearch(), SpecContinueList()} {
		require.JSONEq(t, "{}", string(spec.Payload.ExampleJSON))
		_, err := model.NewToolDefinitionFromSpec(spec)
		require.NoError(t, err)
		value, err := spec.Payload.Codec.FromJSON([]byte("{}"))
		require.NoError(t, err)
		encoded, err := spec.Payload.Codec.ToJSON(value)
		require.NoError(t, err)
		require.JSONEq(t, "{}", string(encoded))
		for _, invalid := range []string{"null", "[]", "{}{}", "{\"query\":\"records\"}", "{\"cursor\":\"page\"}", "{\"session_id\":\"session\"}"} {
			_, err := spec.Payload.Codec.FromJSON([]byte(invalid))
			require.Error(t, err, "%s accepted %s", spec.Name, invalid)
		}
	}
}

func TestExecutionInputRemainsComplete(t *testing.T) {
	for _, test := range []struct {
		spec tools.ToolSpec
		valid string
		invalid []string
	}{
		{SpecContinueSearch(), "{\"query\":\"records\",\"cursor\":\"page\"}", []string{"{}", "{\"query\":\"records\"}", "{\"cursor\":\"page\"}"}},
		{SpecContinueList(), "{\"next_position\":\"page\"}", []string{"{}", "{\"next_position\":1}", "{\"nextPosition\":\"page\"}"}},
		{SpecSearch(), "{\"query\":\"records\"}", []string{"{}", "{\"query\":\"records\",\"cursor\":\"page\"}", "{\"query\":\"records\",\"session_id\":\"session\"}"}},
	} {
		value, err := test.spec.ExecutionPayloadCodec.FromJSON([]byte(test.valid))
		require.NoError(t, err)
		encoded, err := test.spec.ExecutionPayloadCodec.ToJSON(value)
		require.NoError(t, err)
		require.JSONEq(t, test.valid, string(encoded))
		for _, invalid := range test.invalid {
			_, err := test.spec.ExecutionPayloadCodec.FromJSON([]byte(invalid))
			require.Error(t, err, "%s accepted %s", test.spec.Name, invalid)
		}
	}
}

func TestRuntimeRegistersGeneratedContinuationExamples(t *testing.T) {
	rt := runtime.New(storageinmem.New())
	require.NoError(t, rt.RegisterToolset(runtime.ToolsetRegistration{
		Name: "lookup",
		Specs: Specs(),
		Execute: func(context.Context, *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
			panic("registration must not execute tools")
		},
	}))
}

func TestOrdinaryPagingKeepsModelOwnedArguments(t *testing.T) {
	spec := SpecSearchManual()
	for _, codec := range []tools.JSONCodec[any]{spec.Payload.Codec, spec.ExecutionPayloadCodec} {
		for _, valid := range []string{"{\"query\":\"records\"}", "{\"query\":\"records\",\"cursor\":\"page\"}"} {
			value, err := codec.FromJSON([]byte(valid))
			require.NoError(t, err)
			encoded, err := codec.ToJSON(value)
			require.NoError(t, err)
			require.JSONEq(t, valid, string(encoded))
		}
		_, err := codec.FromJSON([]byte("{}"))
		require.Error(t, err)
	}
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "./alpha/toolsets/lookup"))
}
