package tests

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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
	return testhelpers.BuildAndGenerate(t, design)
}

// TestInjectBoundToolUsesGeneratedContext proves a BindTo tool receives
// metadata-backed and label-backed fields through one generated InjectGetData
// implementation before the bound service method runs.
func TestInjectBoundToolUsesGeneratedContext(t *testing.T) {
	files := buildWithPrepare(t, testscenarios.InjectBoundMetaExample())

	inject := fileContent(t, files, "gen/catalog/toolsets/helpers/inject.go")
	require.Contains(t, inject, "func InjectGetData(p *GetDataPayload, meta runtime.ToolCallMeta, labels map[string]string) error {")
	require.Contains(t, inject, "v := meta.SessionID")
	require.Contains(t, inject, `v, ok := labels["household_id"]`)
	require.Contains(t, inject, "p.SessionID = v",
		"the runtime fills the required public tool input after model JSON is decoded")
	require.Contains(t, inject, "func DecodeGetData(payload []byte, meta runtime.ToolCallMeta, labels map[string]string) (*GetDataPayload, error) {",
		"the composed decode helper must exist beside Inject<Tool> for custom executors")
	require.Contains(t, inject, "p, err := GetDataPayloadCodec().FromJSON(payload)")
	require.Contains(t, inject, "if err := InjectGetData(p, meta, labels); err != nil {")

	provider := fileContent(t, files, "gen/catalog/toolsets/helpers/provider.go")
	require.NotContains(t, provider, "methodIn.SessionID = msg.Meta.SessionID",
		"provider.go must retire its own inline meta assignment in favor of the shared Inject<Tool> function")
	require.Contains(t, provider, "meta := runtime.ToolCallMeta{")
	require.Contains(t, provider, "Labels:           msg.Meta.Labels,")
	require.Contains(t, provider, "if err := InjectGetData(args, meta, meta.Labels); err != nil {",
		"registry-served bound tools receive the same immutable run labels as local executors")

	specs := fileContent(t, files, "gen/catalog/toolsets/helpers/specs.go")
	require.NotContains(t, specs, `"session_id"`, "session_id must stay hidden from the model-facing schema")
	require.NotContains(t, specs, `\"household_id\"`, "household_id must stay hidden from the model-facing schema")
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

	exec := fileContent(t, files, "gen/catalog/agents/scribe/helpers/service_executor.go")
	require.Contains(t, exec, "val, err := helpersspecs.GetDataPayloadCodec().FromJSON(call.Payload)")
	require.Contains(t, exec, "if err := helpersspecs.InjectGetData(val, *meta, call.Labels); err != nil {",
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
	require.Contains(t, inject, "p.HouseholdID = v",
		"the runtime fills the required public tool input after model JSON is decoded")
	require.Contains(t, inject, "v := meta.SessionID", "mixed tool: session_id stays meta-backed alongside the label-backed field")
	require.Contains(t, inject, "p.SessionID = v")
	require.Contains(t, inject, "func DecodeLookupHousehold(payload []byte, meta runtime.ToolCallMeta, labels map[string]string) (*LookupHouseholdPayload, error) {",
		"the composed decode helper must exist for unbound (custom-executor-eligible) injecting tools too")

	codecs := fileContent(t, files, "gen/calc/toolsets/helpers/codecs.go")
	require.Contains(t, codecs, "// Prefer DecodeLookupHousehold when decoding tool calls: FromJSON alone",
		"the injecting tool's payload codec GoDoc must steer custom executors to the composed Decode<Tool> helper")

	specs := fileContent(t, files, "gen/calc/toolsets/helpers/specs.go")
	require.Contains(t, specs, `func RequiredLabels() []string {
    return []string{
        "household_id",
    }
}`)
	require.NotContains(t, specs, `"household_id"`+":", "household_id must stay hidden from the model-facing schema")
	require.NotContains(t, specs, `\"session_id\"`, "session_id must stay hidden from the model-facing schema")
}

// TestInjectReusableExportUsesDefiningContract proves shared generated types
// use the prepared defining toolset rather than an unprepared consumer copy.
func TestInjectReusableExportUsesDefiningContract(t *testing.T) {
	files := buildWithPrepare(t, testscenarios.InjectReusableExportExample())

	types := fileContent(t, files, "gen/atlas/toolsets/helpers/types.go")
	require.Equal(t, 2, strings.Count(types, "SessionID string"))
	require.Equal(t, 2, strings.Count(types, "Query string"))

	inject := fileContent(t, files, "gen/atlas/toolsets/helpers/inject.go")
	require.Contains(t, inject, "func InjectInherited(p *InheritedPayload")
	require.Contains(t, inject, "func InjectExplicit(p *ExplicitPayload")
	require.Contains(t, inject, "p.SessionID = v")
	require.NotContains(t, inject, "p.SessionID = &v")

	specs := fileContent(t, files, "gen/atlas/toolsets/helpers/specs.go")
	require.Contains(t, specs, `\"query\"`)
	require.NotContains(t, specs, `\"session_id\"`)

	transport := fileContent(t, files, "gen/atlas/toolsets/helpers/http/types.go")
	require.Contains(t, transport, "SessionID *string `json:\"-\"`")
}

// TestInjectNoLabelsToolsetHasEmptyRequiredLabels proves RequiredLabels is
// always present (even empty) so the runtime can union it across every
// toolset without existence checks.
func TestInjectNoLabelsToolsetHasEmptyRequiredLabels(t *testing.T) {
	files := buildWithPrepare(t, testscenarios.AuthoredPayloadExample())

	specs := fileContent(t, files, "gen/calc/toolsets/helpers/specs.go")
	require.Contains(t, specs, "func RequiredLabels() []string {\n    return []string{\n    }\n}")
	require.False(t, fileExists(files, "gen/calc/toolsets/helpers/inject.go"), "no Inject() fields means no generated inject.go")
}

// TestInjectAgentRequiredLabelsAggregation locks the agent-level
// RequiredLabels contract at the generation layer: the agent's aggregated
// specs package exposes the sorted, deduplicated union of every used
// toolset's RequiredLabels, and agent.go stores it in the immutable definition
// used by both callers and workers.
func TestInjectAgentRequiredLabelsAggregation(t *testing.T) {
	files := buildWithPrepare(t, testscenarios.InjectMultiToolsetLabelsExample())

	// Per-toolset generated data: helpers requires household_id only; audit
	// requires both keys.
	helpers := fileContent(t, files, "gen/calc/toolsets/helpers/specs.go")
	require.Contains(t, helpers, "func RequiredLabels() []string {\n    return []string{\n        \"household_id\",\n    }\n}")
	audit := fileContent(t, files, "gen/calc/toolsets/audit/specs.go")
	require.Contains(t, audit, "func RequiredLabels() []string {\n    return []string{\n        \"household_id\",\n        \"tenant_id\",\n    }\n}")

	// Agent-level aggregate: union across both toolsets, sorted, and
	// deduplicated (household_id appears in both toolsets but only once here).
	agg := fileContent(t, files, "gen/calc/agents/scribe/specs/specs.go")
	require.Contains(t, agg, "func RequiredLabels() []string {\n    return []string{\n        \"household_id\",\n        \"tenant_id\",\n    }\n}")
	require.Equal(t, 1, strings.Count(agg, `"household_id",`),
		"duplicate label keys across toolsets must be deduplicated in the aggregate")

	// Definition wiring: the aggregate reaches the caller before any workflow is
	// scheduled.
	agent := fileContent(t, files, "gen/calc/agents/scribe/agent.go")
	require.Contains(t, agent, "specs.RequiredLabels(),")
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

	provider := fileContent(t, files, "gen/catalog/toolsets/helpers/provider.go")
	require.NotContains(t, provider, "meta := runtime.ToolCallMeta{",
		"no method-backed tool injects, so provider.go must not declare meta")
	require.NotContains(t, provider, `"goa.design/goa-ai/runtime/agent/runtime"`,
		"the runtime import must be gated together with the meta declaration")

	// The unbound tool's compiled injection still exists for local executors.
	inject := fileContent(t, files, "gen/catalog/toolsets/helpers/inject.go")
	require.Contains(t, inject, "func InjectLookupHousehold(p *LookupHouseholdPayload, meta runtime.ToolCallMeta, labels map[string]string) error {")
}
