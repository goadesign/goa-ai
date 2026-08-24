package api

// This file checks JSON and clone behavior for public run input and policy
// values passed between callers and runtime workers.

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	legacyPlanActivityInput struct {
		AgentID       agent.Ident
		RunID         string
		Messages      []*model.Message
		RunContext    run.Context
		Policy        *PolicyOverrides
		ToolOutputs   []*ToolOutputRef
		SynthesisOnly bool
		Finalize      *planner.Termination
	}

	legacyPlanActivityOutput struct {
		Result       *planner.PlanResult
		Transcript   []*model.Message
		Usage        model.TokenUsage
		SessionEnded bool
	}

	legacyPolicyOverrides struct {
		RestrictToTool                tools.Ident
		TagClauses                    []TagPolicyClause
		MaxToolCalls                  int
		MaxConsecutiveFailedToolCalls int
		TimeBudget                    time.Duration
		PlanTimeout                   time.Duration
		ToolTimeout                   time.Duration
		PerToolTimeout                map[tools.Ident]time.Duration
		FinalizerGrace                time.Duration
	}
)

func TestPolicyOverridesRequireHardWorkerCutover(t *testing.T) {
	for _, field := range []string{"CompletionTool", "LimitTerminalPlans"} {
		t.Run(field, func(t *testing.T) {
			payload, err := json.Marshal(map[string]any{field: nil})
			require.NoError(t, err)

			decoder := json.NewDecoder(bytes.NewReader(payload))
			decoder.DisallowUnknownFields()
			var legacy legacyPolicyOverrides
			err = decoder.Decode(&legacy)
			require.EqualError(t, err, `json: unknown field "`+field+`"`)
		})
	}
}

func TestPlanActivityOutputUnmarshalJSON(t *testing.T) {
	t.Run("canonical transcript", func(t *testing.T) {
		const payload = `{
			"Result": null,
			"Transcript": [{
				"role": "assistant",
				"meta": {"trace": "abc"},
				"parts": [
					{"kind": "text", "text": "hi there"},
					{"kind": "tool_use", "id": "tool-call-1", "name": "search", "input": {"z":9007199254740993,"a":1}},
					{"kind": "tool_result", "tool_use_id": "tool-call-1", "content": {"items": 1}, "is_error": false}
				]
			}]
		}`

		var out PlanActivityOutput
		require.NoError(t, json.Unmarshal([]byte(payload), &out))
		require.Len(t, out.Transcript, 1)

		msg := out.Transcript[0]
		require.Equal(t, model.ConversationRoleAssistant, msg.Role)
		require.Equal(t, map[string]any{"trace": "abc"}, msg.Meta)
		require.Len(t, msg.Parts, 3)

		if tp, ok := msg.Parts[0].(model.TextPart); ok {
			require.Equal(t, "hi there", tp.Text)
		} else {
			t.Fatalf("unexpected part[0]: %#v", msg.Parts[0])
		}

		if tu, ok := msg.Parts[1].(model.ToolUsePart); ok {
			require.Equal(t, "search", tu.Name)
			require.Equal(t, `{"z":9007199254740993,"a":1}`, string(tu.Input))
		} else {
			t.Fatalf("unexpected part[1]: %#v", msg.Parts[1])
		}

		if tr, ok := msg.Parts[2].(model.ToolResultPart); ok {
			require.Equal(t, "tool-call-1", tr.ToolUseID)
			require.False(t, tr.IsError)
			require.Equal(t, map[string]any{"items": json.Number("1")}, tr.Content)
		} else {
			t.Fatalf("unexpected part[2]: %#v", msg.Parts[2])
		}
	})

	t.Run("missing kind", func(t *testing.T) {
		const invalid = `{
			"Result": null,
			"Transcript": [{
				"role": "assistant",
				"parts": [
					{"id": "tool-call-1", "name": "search", "input": {"q": "status"}}
				]
			}]
		}`

		var out PlanActivityOutput
		require.ErrorContains(t, json.Unmarshal([]byte(invalid), &out), "message part requires kind")
	})
}

func TestPlanActivityInputOmitsEmptyRecoveryIdentity(t *testing.T) {
	payload, err := json.Marshal(PlanActivityInput{RunID: "run-1"})
	require.NoError(t, err)
	require.NotContains(t, string(payload), "RecoveryToolCallIDs")

	payload, err = json.Marshal(PlanActivityInput{
		RunID:               "run-1",
		RecoveryToolCallIDs: []string{"call-1"},
	})
	require.NoError(t, err)
	require.Contains(t, string(payload), `"RecoveryToolCallIDs":["call-1"]`)
}

func TestRecoveryActivityFieldsRequireHardWorkerCutover(t *testing.T) {
	t.Parallel()

	ordinaryInput, err := json.Marshal(PlanActivityInput{RunID: "run-1"})
	require.NoError(t, err)
	require.NoError(t, strictJSONDecode(ordinaryInput, &legacyPlanActivityInput{}))

	recoveryInput, err := json.Marshal(PlanActivityInput{
		RunID:               "run-1",
		RecoveryToolCallIDs: []string{"call-1"},
	})
	require.NoError(t, err)
	require.ErrorContains(t, strictJSONDecode(recoveryInput, &legacyPlanActivityInput{}), "unknown field")

	ordinaryOutput, err := json.Marshal(PlanActivityOutput{})
	require.NoError(t, err)
	require.NoError(t, strictJSONDecode(ordinaryOutput, &legacyPlanActivityOutput{}))

	recoveryOutput, err := json.Marshal(PlanActivityOutput{
		RecoveryCatalog: &RecoveryCatalog{},
	})
	require.NoError(t, err)
	require.ErrorContains(t, strictJSONDecode(recoveryOutput, &legacyPlanActivityOutput{}), "unknown field")
}

// strictJSONDecode mirrors the Temporal payload decoder's unknown-field
// rejection so this API test records the mixed-worker deployment boundary.
func strictJSONDecode(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
