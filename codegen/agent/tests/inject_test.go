package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	codegen "goa.design/goa-ai/codegen/agent"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	"goa.design/goa-ai/codegen/testhelpers"
	gcodegen "goa.design/goa/v3/codegen"
)

// buildWithPrepare runs the full generation pipeline (codegen Prepare, which
// hides Inject()-ed fields from the model schema, followed by Generate),
// matching the real `goa gen` sequence. buildAndGenerate skips Prepare, which
// would silently hide the schema-hiding regression these tests guard.
func buildWithPrepare(t *testing.T, design func()) []*gcodegen.File {
	t.Helper()
	genpkg, roots := testhelpers.RunDesign(t, design)
	require.NoError(t, codegen.Prepare(genpkg, roots))
	files, err := codegen.Generate(genpkg, roots, nil)
	require.NoError(t, err)
	return files
}

// TestInjectMetaBackedBoundToolBackwardCompatible proves the non-negotiable
// constraint: a BindTo tool injecting session_id (the historical, only
// supported case) regenerates to identical provider.go behavior (this task
// leaves provider generation untouched) while also gaining the new,
// topology-shared InjectGetData function.
func TestInjectMetaBackedBoundToolBackwardCompatible(t *testing.T) {
	files := buildWithPrepare(t, testscenarios.InjectBoundMetaExample())

	inject := fileContent(t, files, "gen/atlas/toolsets/helpers/inject.go")
	require.Contains(t, inject, "func InjectGetData(p *GetDataPayload, meta runtime.ToolCallMeta, labels map[string]string) error {")
	require.Contains(t, inject, "p.SessionID = meta.SessionID")

	provider := fileContent(t, files, "gen/atlas/toolsets/helpers/provider.go")
	require.Contains(t, provider, "methodIn.SessionID = msg.Meta.SessionID",
		"provider.go generation must stay untouched this task; Task 2 refactors it to call InjectGetData")

	specs := fileContent(t, files, "gen/atlas/toolsets/helpers/specs.go")
	require.NotContains(t, specs, `"session_id"`, "session_id must stay hidden from the model-facing schema")
}

// TestInjectLabelBackedWithValidation proves label-backed injection: a
// missing label produces a precise compiled error, a present label is
// converted and validated using the field's own declared validation (reused
// via goa's AttributeValidationCode, not duplicated by hand), and the
// toolset's RequiredLabels surface reflects the label key.
func TestInjectLabelBackedWithValidation(t *testing.T) {
	files := buildWithPrepare(t, testscenarios.InjectLabelExample())

	inject := fileContent(t, files, "gen/calc/toolsets/helpers/inject.go")
	require.Contains(t, inject, "func InjectLookupHousehold(p *LookupHouseholdPayload, meta runtime.ToolCallMeta, labels map[string]string) error {")
	require.Contains(t, inject, `v, ok := labels["household_id"]`)
	require.Contains(t, inject, `return fmt.Errorf("tool %q: required label %q is missing; call WithLabels(%q, ...) at run start", "helpers.lookup_household", "household_id", "household_id")`)
	require.Contains(t, inject, `goa.ValidatePattern("household_id", v, "^[a-z0-9-]+$")`)
	require.Contains(t, inject, "p.HouseholdID = v")
	require.Contains(t, inject, "p.SessionID = meta.SessionID", "mixed tool: session_id stays meta-backed alongside the label-backed field")

	specs := fileContent(t, files, "gen/calc/toolsets/helpers/specs.go")
	require.Contains(t, specs, `var RequiredLabels = []string{
    "household_id",
}`)
	require.NotContains(t, specs, `"household_id"`+":", "household_id must stay hidden from the model-facing schema")
	require.NotContains(t, specs, `\"session_id\"`, "session_id must stay hidden from the model-facing schema")
}

// TestInjectNoLabelsToolsetHasEmptyRequiredLabels proves RequiredLabels is
// always present (even empty) so the runtime can union it across every
// toolset without existence checks.
func TestInjectNoLabelsToolsetHasEmptyRequiredLabels(t *testing.T) {
	files := buildWithPrepare(t, testscenarios.AuthoredPayloadExample())

	specs := fileContent(t, files, "gen/calc/toolsets/helpers/specs.go")
	require.Contains(t, specs, "var RequiredLabels = []string{\n}")
	require.False(t, fileExists(files, "gen/calc/toolsets/helpers/inject.go"), "no Inject() fields means no generated inject.go")
}
