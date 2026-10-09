// These tests pass required-input records through actual registry event decoding
// and runtime activities. A pending round must not call the completed result codec.
package executor

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genrecords "goa.design/goa-ai/internal/testpresentation/gen/records/toolsets/records"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	agentsruntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/mcp"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/pulse/streaming"
)

func TestExecutorRequiredInputReachesActivityWithoutCompletedDecoding(t *testing.T) {
	for _, textOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "interactive", true: "text only"}[textOnly], func(t *testing.T) {
			state := ""
			pending := &mcp.InputRequired{RequestState: &state, Requests: map[string]mcp.InputRequest{
				"choice": {Method: "elicitation/create", Params: json.RawMessage(`{"message":"Choose","requestedSchema":{"type":"object","properties":{"choice":{"type":"string"}},"required":["choice"]}}`)},
			}}
			decoded := 0
			spec := genrecords.SpecRead()
			decodeCompleted := spec.Result.Codec.FromJSON
			spec.Result.Codec.FromJSON = func(raw []byte) (any, error) {
				decoded++
				return decodeCompleted(raw)
			}
			message, err := toolregistry.NewInputRequiredResult(testRegistrationTokenA, "input-round", pending)
			require.NoError(t, err)
			stream := &fakeStream{t: t, requiredStart: "0", events: []*streaming.Event{{ID: "1-0", EventName: toolregistry.ResultEventKey, Payload: mustJSON(t, message)}}}
			var delivered genregistry.ToolCallMeta
			exec := newExecutor(t, fakeRegistryClient{toolUseID: "input-round", meta: &delivered}, fakePulseClient{streamID: toolregistry.ResultStreamID("input-round"), stream: stream}, "records", fakeSpecs{spec: &spec})
			rt := agentsruntime.New(inmem.New())
			require.NoError(t, rt.RegisterToolset(agentsruntime.ToolsetRegistration{Name: "records", Specs: []tools.ToolSpec{spec}, DecodeInExecutor: true, Execute: func(ctx context.Context, call *agentsruntime.ToolCall) (*agentsruntime.ToolExecutionResult, error) {
				meta := agentsruntime.ToolCallMetaFromCall(*call)
				return exec.Execute(ctx, &meta, call)
			}}))
			out, err := rt.ExecuteToolActivity(t.Context(), &agentsruntime.ToolInput{ToolsetName: "records", ToolName: spec.Name, ToolCallID: "model-call", RunID: "run", SessionID: "session", Payload: rawjson.Message(`{"query":"active"}`), TextOnly: textOnly})
			require.NoError(t, err)
			require.NotNil(t, out)
			if textOnly {
				assert.Nil(t, out.PendingExecution)
				require.NotNil(t, out.Failure)
				assert.Equal(t, planner.FailureMalformedResult, out.Failure.Kind)
				assert.Equal(t, planner.RecoveryFinish, out.Failure.Recovery.Action)
			} else {
				require.NotNil(t, out)
				received, ok := out.PendingExecution.AsInput()
				require.True(t, ok)
				assert.Equal(t, pending, received)
				assert.Empty(t, out.Payload)
				assert.Nil(t, out.Failure)
			}
			assert.Zero(t, decoded)
			assert.Equal(t, textOnly, delivered.TextOnly)
		})
	}
}
