// These tests prove that confirmation reads the saved JSON arguments, including
// exact numbers and union values, independently of generated Go payload types.
package runtime

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestConfirmationUsesCanonicalJSONProperties(t *testing.T) {
	t.Parallel()

	spec := newAnyJSONSpec("commands.change")
	spec.Confirmation = &tools.ConfirmationSpec{
		Title:                "Confirm change",
		PromptTemplate:       `Set {{ .setting_alias }} to {{ json .desired.value }}{{ with index . "note" }} ({{ . }}){{ end }}`,
		DeniedResultTemplate: `{"cancelled":true,"desired":{{ json .desired }},"setting_alias":{{ json .setting_alias }}}`,
	}
	// The compiled payload representation deliberately differs from JSON. A
	// confirmation must not depend on this process-local representation.
	spec.ExecutionPayloadCodec.FromJSON = func([]byte) (any, error) {
		t.Fatal("confirmation must read the saved JSON arguments")
		return nil, nil
	}
	rt := New(newTestStore())
	rt.toolSpecs[spec.Name] = spec
	call := &ToolCall{
		Name:    spec.Name,
		Payload: rawjson.Message(`{"setting_alias":"large \"value\"","desired":{"type":"number","value":9007199254740993}}`),
	}
	plan, needs, err := rt.confirmationPlan(t.Context(), call)
	require.NoError(t, err)
	require.True(t, needs)
	assert.Equal(t, `Set large "value" to 9007199254740993`, plan.Prompt)
	assert.Equal(t, "Confirm change", plan.Title)
	// The tool's existing result codec still owns the denial value.
	encoded, err := json.Marshal(plan.DeniedResult)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"cancelled":true`)
	assert.Contains(t, string(encoded), `"setting_alias":"large \"value\""`)
}

func TestConfirmationRejectsGoPropertyNames(t *testing.T) {
	t.Parallel()

	spec := newAnyJSONSpec("commands.change")
	spec.Confirmation = &tools.ConfirmationSpec{
		PromptTemplate:       `Set {{ .SettingAlias }}`,
		DeniedResultTemplate: `{}`,
	}
	rt := New(newTestStore())
	rt.toolSpecs[spec.Name] = spec
	_, _, err := rt.confirmationPlan(t.Context(), &ToolCall{
		Name: spec.Name, Payload: rawjson.Message(`{"setting_alias":"limit"}`),
	})
	require.ErrorContains(t, err, `map has no entry for key "SettingAlias"`)
}
