package runtime

// Saved result events enter through checkpoint validation. Exercise malformed
// storage and disagreement between the event and the batch that references it.

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func testMaterializedCheckpointIntegrity(t *testing.T, suspension *api.RunSuspension, definition AgentDefinition) {
	t.Helper()
	const mismatchedRun = "mismatched-event-run"
	for _, test := range []struct {
		name   string
		mutate func(*checkpointToolRecord)
	}{
		{"missing published event", func(r *checkpointToolRecord) { r.ResultRecord = nil }},
		{"malformed event", func(r *checkpointToolRecord) { r.ResultRecord.Payload = rawjson.Message(`{`) }},
		{"wrong kind", func(r *checkpointToolRecord) {
			r.ResultRecord.Type = hooks.ToolCallUpdated
			r.ResultRecord.Payload = rawjson.Message(`{}`)
		}},
		{"agent", func(r *checkpointToolRecord) { r.ResultRecord.AgentID = "corrupted.agent" }},
		{"session", func(r *checkpointToolRecord) { r.ResultRecord.SessionID = "other-session" }},
		{"result run", func(r *checkpointToolRecord) { r.ResultRecord.RunID = mismatchedRun }},
		{"call run", func(r *checkpointToolRecord) { r.CallRunID = mismatchedRun }},
		{"batch result", func(r *checkpointToolRecord) { r.Result.Result = rawjson.Message(`{"id":"different"}`) }},
		{"materialized bytes", func(r *checkpointToolRecord) { r.ResultJSON = rawjson.Message(`{"id":"different"}`) }},
		{"current typed result", func(r *checkpointToolRecord) { r.Result.Result = rawjson.Message(`{"id":5}`) }},
		{"current server data", func(r *checkpointToolRecord) { r.Result.ServerData = rawjson.Message(`{`) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			checkpoint, err := decodeWorkflowCheckpointState(suspension)
			require.NoError(t, err)
			test.mutate(&checkpoint.Batch.Records[0])
			require.Error(t, validateCheckpointToolValues(checkpoint, definition))
		})
	}
	for field, value := range map[string]any{
		"tool_call_id": "other-call", "tool_name": "records.other", "parent_tool_call_id": "other-parent",
		"call_run_id": mismatchedRun, "result_bytes": 1,
		"result_json": json.RawMessage(`{"id":"different"}`),
		"server_data": json.RawMessage(`{"unexpected":true}`),
	} {
		t.Run("event "+field, func(t *testing.T) {
			checkpoint, err := decodeWorkflowCheckpointState(suspension)
			require.NoError(t, err)
			record := checkpoint.Batch.Records[0].ResultRecord
			var payload map[string]any
			require.NoError(t, json.Unmarshal(record.Payload, &payload))
			_, exists := payload[field]
			// Optional parent and server data fields may be absent.
			require.True(t, exists || field == "parent_tool_call_id" || field == "server_data")
			payload[field] = value
			record.Payload, err = json.Marshal(payload)
			require.NoError(t, err)
			require.Error(t, validateCheckpointToolValues(checkpoint, definition))
		})
	}
	// Failed publication retries reuse the same prepared event. They do not
	// turn its accepted input back into an argument for today's renderer.
	checkpoint, err := decodeWorkflowCheckpointState(suspension)
	require.NoError(t, err)
	checkpoint.Batch.Records[0].ResultPublished = false
	require.NoError(t, validateCheckpointToolValues(checkpoint, definition))

	for _, paging := range []bool{false, true} {
		t.Run(map[bool]string{false: "materialization still required", true: "paging still required"}[paging], func(t *testing.T) {
			copy, err := decodeWorkflowCheckpointState(suspension)
			require.NoError(t, err)
			specs := slices.Clone(definition.specs)
			for i := range specs {
				if specs[i].Name != copy.Batch.Records[0].Call.Name {
					continue
				}
				specs[i].ExecutionPayloadCodec.FromJSON = func([]byte) (any, error) {
					return nil, errors.New("current consumer needs original input")
				}
				if paging {
					specs[i].Bounds = &tools.BoundsSpec{Paging: &tools.PagingSpec{CursorField: "cursor"}}
				}
			}
			if !paging {
				copy.Batch.Records[0].ResultPublished = false
				copy.Batch.Records[0].ResultRecord = nil
			}
			strict := NewAgentDefinition(definition.route, specs, nil, nil, nil, nil, nil)
			require.ErrorContains(t, validateCheckpointInputs(copy, strict), "current consumer needs original input")
		})
	}
}
