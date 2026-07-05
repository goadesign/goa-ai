package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	goa "goa.design/goa/v3/pkg"

	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

// lookupHouseholdPayload mirrors the shape codegen generates for a tool
// payload with a label-backed Inject() field: HouseholdID is a pointer
// tagged `json:"-"` (hidden from the wire codec and optional in the
// model-facing contract, matching prepare.go's flattenAndHide) so it can
// only ever be populated by injection, never by the model.
type lookupHouseholdPayload struct {
	HouseholdID *string `json:"-"`
	Query       string  `json:"query"`
}

// lookupHouseholdPayloadCodecFromJSON is a hand-written stand-in for the
// FromJSON side of a generated <Tool>PayloadCodec (see
// codegen/agent/templates/tool_codecs.go.tpl), used only by
// decodeLookupHousehold below to mirror the generated Decode<Tool>
// composition exactly.
func lookupHouseholdPayloadCodecFromJSON(data []byte) (*lookupHouseholdPayload, error) {
	var p lookupHouseholdPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// injectLookupHousehold is a hand-written stand-in for a generated
// Inject<Tool> function (see codegen/agent/templates/tool_inject.go.tpl and
// codegen/agent/tests/inject_test.go's TestInjectLabelBackedWithValidation
// for the literal generated shape this mirrors). It exists to prove the
// contract custom (non-method-backed) local executors rely on: the runtime
// already hands ToolCallMeta and call.Labels to any ToolCallExecutor, so a
// custom executor can call a function shaped exactly like this one to get
// the same compiled meta/label population and validation semantics as a
// generated executor, without any additional runtime interception.
func injectLookupHousehold(p *lookupHouseholdPayload, meta ToolCallMeta, labels map[string]string) error {
	v, ok := labels["household_id"]
	if !ok {
		return fmt.Errorf("tool %q: required label %q is missing; call WithLabels(%q, ...) at run start", "helpers.lookup_household", "household_id", "household_id")
	}
	if err := goa.ValidatePattern("household_id", v, "^[a-z0-9-]+$"); err != nil {
		return fmt.Errorf("tool %q: label %q failed validation: %w", "helpers.lookup_household", "household_id", err)
	}
	p.HouseholdID = &v
	_ = meta // unused in this fixture; a real meta-backed field would read meta.SessionID etc.
	return nil
}

// decodeLookupHousehold is a hand-written stand-in for a generated
// Decode<Tool> function (see codegen/agent/templates/tool_inject.go.tpl's
// Decode<ConstName>): it composes the payload codec with the Inject<Tool>
// function in one call so a custom executor cannot decode this tool's
// payload without also running injection. This is the function a
// hand-written ToolCallExecutor for a generated toolset would call instead
// of decoding via the raw payload codec.
func decodeLookupHousehold(payload []byte, meta ToolCallMeta, labels map[string]string) (*lookupHouseholdPayload, error) {
	p, err := lookupHouseholdPayloadCodecFromJSON(payload)
	if err != nil {
		return nil, err
	}
	if err := injectLookupHousehold(p, meta, labels); err != nil {
		return nil, err
	}
	return p, nil
}

// newCustomLookupHouseholdToolset registers a hand-written (non-generated)
// ToolCallExecutor for an unbound "custom executor" tool, exactly the case
// codegen never covers (service_executor.go.tpl only emits dispatch cases
// for method-backed tools). The executor decodes and injects in one call via
// decodeLookupHousehold, proving the local topology needs no additional
// runtime seam for this case beyond what already existed (ToolCallMeta +
// call.Labels reaching the executor) and that the composed Decode<Tool>
// shape is what a custom executor author should reach for.
func newCustomLookupHouseholdToolset(t *testing.T, resultHouseholdID *string) ToolsetRegistration {
	t.Helper()
	return ToolsetRegistration{
		Name: "helpers",
		Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			meta := ToolCallMeta{
				RunID:     call.RunID,
				SessionID: call.SessionID,
				TurnID:    call.TurnID,
			}
			p, err := decodeLookupHousehold(call.Payload, meta, call.Labels)
			if err != nil {
				return &planner.ToolResult{Name: call.Name, Error: planner.ToolErrorFromError(err)}, nil
			}
			*resultHouseholdID = *p.HouseholdID
			return &planner.ToolResult{Name: call.Name, Result: map[string]any{"ok": true}}, nil
		}),
		Specs: []tools.ToolSpec{newAnyJSONSpec(tools.Ident("helpers.lookup_household"), "helpers")},
	}
}

// TestCustomExecutorLabelInjection_TypedFieldPopulated is the inmem
// end-to-end proof: a run label set at the ExecuteToolActivity boundary
// (mirroring what Runtime.Start/WithLabels thread down to
// planner.ToolRequest.Labels, see activities.go's ExecuteToolActivity) ends
// up as a validated, typed field on a custom executor's decoded payload.
func TestCustomExecutorLabelInjection_TypedFieldPopulated(t *testing.T) {
	rt := New()
	var gotHouseholdID string
	ts := newCustomLookupHouseholdToolset(t, &gotHouseholdID)
	rt.mu.Lock()
	rt.addToolsetLocked(ts)
	rt.mu.Unlock()

	input := ToolInput{
		ToolsetName: "helpers",
		ToolName:    tools.Ident("helpers.lookup_household"),
		Payload:     rawjson.Message(`{"query":"who lives here"}`),
		Labels:      map[string]string{"household_id": "house-42"},
	}
	out, err := rt.ExecuteToolActivity(context.Background(), &input)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Empty(t, out.Error, "tool call must succeed once the required label is present and valid")
	require.Equal(t, "house-42", gotHouseholdID, "the injected field must carry the run label value")
}

// TestCustomExecutorLabelInjection_MissingLabelProducesPreciseToolError
// proves a missing label surfaces as a precise, actionable tool-call error
// naming the tool and the label key -- distinct from (and in addition to)
// the run-start enforcement gate, which this test bypasses by calling
// ExecuteToolActivity directly.
func TestCustomExecutorLabelInjection_MissingLabelProducesPreciseToolError(t *testing.T) {
	rt := New()
	var gotHouseholdID string
	ts := newCustomLookupHouseholdToolset(t, &gotHouseholdID)
	rt.mu.Lock()
	rt.addToolsetLocked(ts)
	rt.mu.Unlock()

	input := ToolInput{
		ToolsetName: "helpers",
		ToolName:    tools.Ident("helpers.lookup_household"),
		Payload:     rawjson.Message(`{"query":"who lives here"}`),
		Labels:      nil,
	}
	out, err := rt.ExecuteToolActivity(context.Background(), &input)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotEmpty(t, out.Error)
	require.Contains(t, out.Error, `required label "household_id" is missing`)
	require.Contains(t, out.Error, "helpers.lookup_household")
	require.Empty(t, gotHouseholdID)
}

// TestCustomExecutorLabelInjection_MalformedLabelProducesPreciseToolError
// proves a present-but-invalid label value fails the field's own declared
// validation (reused, not hand-duplicated -- see
// codegen/agent/inject.go:fieldValidationCode) with a precise error instead
// of silently accepting a malformed value.
func TestCustomExecutorLabelInjection_MalformedLabelProducesPreciseToolError(t *testing.T) {
	rt := New()
	var gotHouseholdID string
	ts := newCustomLookupHouseholdToolset(t, &gotHouseholdID)
	rt.mu.Lock()
	rt.addToolsetLocked(ts)
	rt.mu.Unlock()

	input := ToolInput{
		ToolsetName: "helpers",
		ToolName:    tools.Ident("helpers.lookup_household"),
		Payload:     rawjson.Message(`{"query":"who lives here"}`),
		Labels:      map[string]string{"household_id": "Not Valid!"},
	}
	out, err := rt.ExecuteToolActivity(context.Background(), &input)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotEmpty(t, out.Error)
	require.Contains(t, out.Error, `label "household_id" failed validation`)
	require.Empty(t, gotHouseholdID)
}
