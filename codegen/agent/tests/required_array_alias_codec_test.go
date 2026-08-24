// Package tests verifies that generated model JSON codecs preserve null array
// elements until Goa validation runs, then return ordinary public values.
package tests

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"goa.design/goa-ai/codegen/testhelpers"
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// TestGeneratedCodecRejectsNullRequiredAliasArrayElement verifies that a model
// cannot send null where the Goa design requires a string alias value.
func TestGeneratedCodecRejectsNullRequiredAliasArrayElement(t *testing.T) {
	files := testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", requiredAliasArrayDesign())
	root := writeGeneratedModule(t, files)
	writeGeneratedPackageTest(
		t,
		root,
		"alpha/toolsets/aliases/http/validate.go",
		fileContent(t, files, "gen/alpha/toolsets/aliases/http/validate.go"),
	)
	writeGeneratedPackageTest(t, root, "alpha/toolsets/aliases/required_array_alias_test.go", `package aliases

import (
	"errors"
	"testing"

	"goa.design/goa-ai/runtime/agent/tools"
)

func TestRequiredAliasArray(t *testing.T) {
	payload, err := UnmarshalStorePayload([]byte("{\"values\":[\"\"]}"))
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Values) != 1 || payload.Values[0] != "" {
		t.Fatalf("unexpected public values: %#v", payload.Values)
	}
	payload.Values = append(payload.Values, "another")

	_, err = UnmarshalStorePayload([]byte("{\"values\":[null]}"))
	var validation *tools.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected validation error, got %v", err)
	}
	issues := validation.Issues()
	if len(issues) != 1 || issues[0].Field != "values" || issues[0].Constraint != "invalid_field_type" ||
		issues[0].ActualJSONType != "null" {
		if len(issues) == 0 {
			t.Fatal("expected one validation issue")
		}
		t.Fatalf(
			"unexpected validation issue: field=%q constraint=%q expected=%q actual=%q",
			issues[0].Field,
			issues[0].Constraint,
			issues[0].ExpectedJSONType,
			issues[0].ActualJSONType,
		)
	}
}
`)

	runGeneratedAliasesGoTest(t, root)
}

// requiredAliasArrayDesign defines one model input whose string alias values
// must be present even though the empty string remains valid.
func requiredAliasArrayDesign() func() {
	return func() {
		API("alpha", func() {})
		alias := Type("Alias", String, func() {
			Pattern("^[a-z]*$")
		})
		payload := Type("AliasPayload", func() {
			Field(1, "values", ArrayOfRequired(alias), "Values supplied by the model")
			Required("values")
		})
		Service("alpha", func() {
			aidsl.Agent("scribe", "Stores alias values", func() {
				aidsl.Use("aliases", func() {
					aidsl.Tool("store", "Store alias values", func() {
						aidsl.Args(payload)
						aidsl.Return(String, "Stored value")
					})
				})
			})
		})
	}
}

// runGeneratedAliasesGoTest compiles and runs the generated aliases toolset.
func runGeneratedAliasesGoTest(t *testing.T, root string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "./alpha/toolsets/aliases"))
}
