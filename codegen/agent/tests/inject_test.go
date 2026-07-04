package tests

import (
	"strings"
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
// supported case) regenerates to behaviorally identical population -- Task 2
// retires provider.go's inline `methodIn.SessionID = msg.Meta.SessionID`
// assignment in favor of calling the topology-shared InjectGetData function
// (the "one canonical implementation" goal), so session_id still resolves to
// the same runtime.ToolCallMeta.SessionID value.
func TestInjectMetaBackedBoundToolBackwardCompatible(t *testing.T) {
	files := buildWithPrepare(t, testscenarios.InjectBoundMetaExample())

	inject := fileContent(t, files, "gen/atlas/toolsets/helpers/inject.go")
	require.Contains(t, inject, "func InjectGetData(p *GetDataPayload, meta runtime.ToolCallMeta, labels map[string]string) error {")
	require.Contains(t, inject, "v := meta.SessionID")
	require.Contains(t, inject, "p.SessionID = &v",
		"injected fields are pointers on the tool payload (hidden fields are optional in the model-facing contract)")
	require.Contains(t, inject, "func DecodeGetData(payload []byte, meta runtime.ToolCallMeta, labels map[string]string) (*GetDataPayload, error) {",
		"the composed decode helper must exist beside Inject<Tool> for custom executors")
	require.Contains(t, inject, "p, err := GetDataPayloadCodec.FromJSON(payload)")
	require.Contains(t, inject, "if err := InjectGetData(p, meta, labels); err != nil {")

	provider := fileContent(t, files, "gen/atlas/toolsets/helpers/provider.go")
	require.NotContains(t, provider, "methodIn.SessionID = msg.Meta.SessionID",
		"provider.go must retire its own inline meta assignment in favor of the shared Inject<Tool> function")
	require.Contains(t, provider, "meta := runtime.ToolCallMeta{")
	require.Contains(t, provider, "if err := InjectGetData(args, meta, nil); err != nil {",
		"registry-served (bound) tools never carry labels, so the shared Inject fn is called with a nil labels map")

	specs := fileContent(t, files, "gen/atlas/toolsets/helpers/specs.go")
	require.NotContains(t, specs, `"session_id"`, "session_id must stay hidden from the model-facing schema")
}

// TestInjectLocalServiceExecutorCallsGeneratedInject proves the local
// topology's generated service executor (New<Agent><Toolset>Exec) retires its
// own inline meta assignment in favor of calling the shared InjectGetData
// function right after decode -- before either the WithClient dispatch
// branch or a user-supplied WithPayloadMapper hook sees the payload. This is
// the "one canonical implementation" placement: a single call site upstream
// of every downstream branch (built-in alias, Init<Tool>MethodPayload
// conversion, and custom cfg.mapPayload) instead of duplicating population
// per branch.
func TestInjectLocalServiceExecutorCallsGeneratedInject(t *testing.T) {
	files := buildWithPrepare(t, testscenarios.InjectBoundMetaExample())

	exec := fileContent(t, files, "gen/atlas/agents/scribe/helpers/service_executor.go")
	require.Contains(t, exec, "val, err := helpers.GetDataPayloadCodec.FromJSON(call.Payload)")
	require.Contains(t, exec, "if err := helpers.InjectGetData(val, *meta, call.Labels); err != nil {",
		"injection must run on the decoded tool payload, with call.Labels threaded to the shared Inject fn")
	require.NotContains(t, exec, "p.SessionID = meta.SessionID",
		"the per-branch inline meta assignment must be retired now that decode-time injection covers every branch")
	require.NotContains(t, exec, "meta.{{ goify",
		"template placeholder must not leak into generated output")

	// The single decode-time injection call must run before the mapPayload
	// customization hook, so a user-supplied WithPayloadMapper still observes
	// the injected field on toolArgs.
	injectIdx := strings.Index(exec, "InjectGetData(val")
	mapPayloadIdx := strings.Index(exec, "cfg.mapPayload != nil")
	require.NotEqual(t, -1, injectIdx)
	require.NotEqual(t, -1, mapPayloadIdx)
	require.Less(t, injectIdx, mapPayloadIdx, "Inject must run before the cfg.mapPayload customization hook")
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
	require.Contains(t, inject, "p.HouseholdID = &v",
		"injected fields are pointers on the tool payload (hidden fields are optional in the model-facing contract)")
	require.Contains(t, inject, "v := meta.SessionID", "mixed tool: session_id stays meta-backed alongside the label-backed field")
	require.Contains(t, inject, "p.SessionID = &v")
	require.Contains(t, inject, "func DecodeLookupHousehold(payload []byte, meta runtime.ToolCallMeta, labels map[string]string) (*LookupHouseholdPayload, error) {",
		"the composed decode helper must exist for unbound (custom-executor-eligible) injecting tools too")

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

// TestInjectAgentRequiredLabelsAggregation locks the agent-level
// RequiredLabels contract at the generation layer: the agent's aggregated
// specs package exposes the sorted, deduplicated union of every used
// toolset's RequiredLabels, and registry.go wires that var onto
// AgentRegistration so Runtime.Start/StartOneShot can enforce it before any
// workflow is scheduled.
func TestInjectAgentRequiredLabelsAggregation(t *testing.T) {
	files := buildWithPrepare(t, testscenarios.InjectMultiToolsetLabelsExample())

	// Per-toolset generated data: helpers requires household_id only; audit
	// requires both keys.
	helpers := fileContent(t, files, "gen/calc/toolsets/helpers/specs.go")
	require.Contains(t, helpers, "var RequiredLabels = []string{\n    \"household_id\",\n}")
	audit := fileContent(t, files, "gen/calc/toolsets/audit/specs.go")
	require.Contains(t, audit, "var RequiredLabels = []string{\n    \"household_id\",\n    \"tenant_id\",\n}")

	// Agent-level aggregate: union across both toolsets, sorted, and
	// deduplicated (household_id appears in both toolsets but only once here).
	agg := fileContent(t, files, "gen/calc/agents/scribe/specs/specs.go")
	require.Contains(t, agg, "RequiredLabels = []string{\n        \"household_id\",\n        \"tenant_id\",\n    }")
	require.Equal(t, 1, strings.Count(agg, `"household_id",`),
		"duplicate label keys across toolsets must be deduplicated in the aggregate")

	// Registry wiring: the aggregate var reaches AgentRegistration.
	registry := fileContent(t, files, "gen/calc/agents/scribe/registry.go")
	require.Contains(t, registry, "RequiredLabels: specs.RequiredLabels,")
}

// TestInjectMixedBoundUnboundProviderScopesMeta locks the provider-side
// compile regression at the section level: a toolset mixing a non-injecting
// BindTo tool with an injecting UNBOUND tool must NOT emit the
// runtime.ToolCallMeta declaration (or the runtime import) in provider.go --
// HandleToolCall only dispatches method-backed tools, so nothing would use
// the variable and the generated package would fail to compile.
// TestGeneratedMixedInjectPackagesCompile proves the same end to end with an
// actual go build of the generated tree.
func TestInjectMixedBoundUnboundProviderScopesMeta(t *testing.T) {
	files := buildWithPrepare(t, testscenarios.InjectMixedBoundUnboundExample())

	provider := fileContent(t, files, "gen/atlas/toolsets/helpers/provider.go")
	require.NotContains(t, provider, "meta := runtime.ToolCallMeta{",
		"no method-backed tool injects, so provider.go must not declare meta")
	require.NotContains(t, provider, `"goa.design/goa-ai/runtime/agent/runtime"`,
		"the runtime import must be gated together with the meta declaration")

	// The unbound tool's compiled injection still exists for local executors.
	inject := fileContent(t, files, "gen/atlas/toolsets/helpers/inject.go")
	require.Contains(t, inject, "func InjectLookupHousehold(p *LookupHouseholdPayload, meta runtime.ToolCallMeta, labels map[string]string) error {")
}
