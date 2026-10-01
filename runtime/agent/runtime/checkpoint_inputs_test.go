package runtime

// These tests distinguish accepted input history from the same bytes still
// needed for execution, publication or paging. All fixtures are synthetic.

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestCheckpointInputsPreserveRecordedHistory(t *testing.T) {
	for _, bookkeeping := range []bool{false, true} {
		t.Run(map[bool]string{false: "planner output", true: "bookkeeping"}[bookkeeping], func(t *testing.T) {
			spec := newAnyJSONSpec("svc.lookup")
			spec.Bookkeeping = bookkeeping
			decodes := 0
			spec.ExecutionPayloadCodec.FromJSON = func([]byte) (any, error) {
				decodes++
				return nil, errors.New("obsolete field is not accepted")
			}
			rt := New(newTestStore())
			seedTestToolSpecs(rt, spec)
			suspension := recordedInputSuspension(t, spec.Name)
			if bookkeeping {
				rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
					checkpoint.State.ToolOutputs = nil
				})
			}
			original := append(rawjson.Message(nil), suspension.Checkpoint...)
			checkpoint, err := decodeWorkflowCheckpoint(suspension, testRuntimeDefinition(rt, "svc.agent"))
			require.NoError(t, err)
			require.Zero(t, decodes)
			require.Equal(t, original, suspension.Checkpoint)
			require.Equal(t, rawjson.Message(`{"query":"status","obsolete":true}`), checkpoint.Batch.Records[0].Call.Payload)

			// An ordinary new call still uses the current input codec.
			require.ErrorContains(t, validateCheckpointToolRequest(checkpoint.Batch.Calls[0], testRuntimeDefinition(rt, "svc.agent")), "obsolete field")
			require.Equal(t, 1, decodes)
		})
	}
}

func TestCheckpointInputsRejectActiveOldArguments(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*workflowCheckpoint)
	}{
		{
			name: "successful but unrecorded result",
			mutate: func(c *workflowCheckpoint) {
				c.Batch.Recorded = 0
				c.State.ToolOutputs = nil
				c.State.ToolEvents = nil
			},
		},
		{
			name: "publication still required",
			mutate: func(c *workflowCheckpoint) {
				c.Batch.Recorded = 0
				c.Batch.Records[0].ResultPublished = false
				c.Batch.Records[0].ResultJSON = nil
				c.State.ToolOutputs = nil
				c.State.ToolEvents = nil
			},
		},
		{
			name: "lost reply has no recorded outcome",
			mutate: func(c *workflowCheckpoint) {
				c.Batch.Recorded = 0
				c.Batch.Records = nil
				c.State.ToolOutputs = nil
				c.State.ToolEvents = nil
			},
		},
		{
			name: "unresolved copy cannot borrow history",
			mutate: func(c *workflowCheckpoint) {
				c.Batch.Recorded = 0
				c.Batch.Records = nil
			},
		},
		{
			name: "pending confirmation",
			mutate: func(c *workflowCheckpoint) {
				c.Pending = []checkpointPendingInput{{Confirmation: &checkpointConfirmation{
					ID: "confirm", Call: c.Batch.Calls[0],
					Title: "Confirm", Prompt: "Proceed?", DeniedResult: rawjson.Message(`{"value":"denied"}`),
				}}}
			},
		},
		{
			name: "pending external input",
			mutate: func(c *workflowCheckpoint) {
				call := c.Batch.Calls[0]
				await := planner.AwaitToolClarificationItem(&planner.AwaitToolClarification{
					ID: "answer", ToolName: call.Name, ToolCallID: call.ToolCallID,
					Payload: call.Payload, Question: "Which group?",
				})
				c.Pending = []checkpointPendingInput{{Await: &await}}
			},
		},
		{
			name: "unfinished child",
			mutate: func(c *workflowCheckpoint) {
				c.Batch.Recorded = 0
				c.Batch.Records[0].ChildSuspension = &api.RunSuspension{}
				c.Batch.Records[0].Result = nil
				c.State.ToolOutputs = nil
				c.State.ToolEvents = nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := newAnyJSONSpec("svc.lookup")
			spec.ExecutionPayloadCodec.FromJSON = func([]byte) (any, error) {
				return nil, errors.New("obsolete field is not accepted")
			}
			rt := New(newTestStore())
			seedTestToolSpecs(rt, spec)
			checkpoint, err := decodeWorkflowCheckpointState(recordedInputSuspension(t, spec.Name))
			require.NoError(t, err)
			test.mutate(checkpoint)
			// Isolate the input consumer check: child/response/recovery union
			// validation has separate coverage at the complete boundary.
			require.ErrorContains(t, validateCheckpointInputs(checkpoint, testRuntimeDefinition(rt, "svc.agent")), "obsolete field")
		})
	}
}

func TestCheckpointInputsRejectInconsistentHistory(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*workflowCheckpoint)
		want   string
	}{
		{"record arguments", func(c *workflowCheckpoint) { c.Batch.Records[0].Call.Payload = rawjson.Message(`{"query":"other"}`) }, "inputs disagree"},
		{"record name", func(c *workflowCheckpoint) { c.Batch.Records[0].Call.Name = "svc.other" }, "inputs disagree"},
		{"provider identity", func(c *workflowCheckpoint) { c.Batch.Records[0].Call.ModelToolCallID = "another-provider-call" }, "inputs disagree"},
		{"paging identity", func(c *workflowCheckpoint) { c.Batch.Records[0].Call.ContinuationRootToolCallID = "another-query-call" }, "inputs disagree"},
		{"output arguments", func(c *workflowCheckpoint) { c.State.ToolOutputs[0].Payload = rawjson.Message(`{"query":"other"}`) }, "inputs disagree"},
		{"result provenance", func(c *workflowCheckpoint) { c.Batch.Records[0].ResultRunID = "another-run" }, "inconsistent result provenance"},
		{"unpublished prefix", func(c *workflowCheckpoint) { c.Batch.Records[0].ResultPublished = false }, "unfinished or inconsistent"},
		{"unscheduled prefix", func(c *workflowCheckpoint) { c.Batch.Records[0].ScheduleRequired = true }, "unfinished or inconsistent"},
		{"missing recorded event", func(c *workflowCheckpoint) { c.State.ToolEvents = nil }, "does not match recorded result"},
		{"output result", func(c *workflowCheckpoint) { c.State.ToolOutputs[0].Result = rawjson.Message(`{"value":"other"}`) }, "does not match recorded result"},
		{"duplicate record", func(c *workflowCheckpoint) { c.Batch.Records = append(c.Batch.Records, c.Batch.Records[0]) }, "duplicate saved tool call"},
		{"duplicate event", func(c *workflowCheckpoint) { c.State.ToolEvents = append(c.State.ToolEvents, c.State.ToolEvents[0]) }, "duplicate result"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rt := New(newTestStore())
			spec := newAnyJSONSpec("svc.lookup")
			seedTestToolSpecs(rt, spec)
			checkpoint, err := decodeWorkflowCheckpointState(recordedInputSuspension(t, spec.Name))
			require.NoError(t, err)
			test.mutate(checkpoint)
			require.ErrorContains(t, validateCheckpointInputs(checkpoint, testRuntimeDefinition(rt, "svc.agent")), test.want)
		})
	}
}

func TestCheckpointInputsKeepPagingAndResultsCurrent(t *testing.T) {
	for _, test := range []struct {
		name string
		spec func(*tools.ToolSpec)
		want string
	}{
		{
			name: "paging",
			spec: func(spec *tools.ToolSpec) {
				spec.Bounds = &tools.BoundsSpec{Paging: &tools.PagingSpec{CursorField: "cursor"}}
				spec.ExecutionPayloadCodec.FromJSON = func([]byte) (any, error) { return nil, errors.New("current paging input required") }
			},
			want: "current paging input required",
		},
		{
			name: "result",
			spec: func(spec *tools.ToolSpec) {
				spec.Result.Codec.FromJSON = func([]byte) (any, error) { return nil, errors.New("current result required") }
			},
			want: "current result required",
		},
		{
			name: "server data",
			spec: func(spec *tools.ToolSpec) {
				spec.CanonicalizeServerData = func(rawjson.Message) (rawjson.Message, error) {
					return nil, errors.New("current server data required")
				}
			},
			want: "current server data required",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			rt := New(newTestStore())
			spec := newAnyJSONSpec("svc.lookup")
			test.spec(&spec)
			seedTestToolSpecs(rt, spec)
			checkpoint, err := decodeWorkflowCheckpointState(recordedInputSuspension(t, spec.Name))
			require.NoError(t, err)
			if test.name == "paging" {
				require.ErrorContains(t, validateCheckpointInputs(checkpoint, testRuntimeDefinition(rt, "svc.agent")), test.want)
			} else {
				if test.name == "server data" {
					checkpoint.State.ToolOutputs[0].ServerData = rawjson.Message(`[]`)
				}
				require.ErrorContains(t, validateCheckpointToolValues(checkpoint, testRuntimeDefinition(rt, "svc.agent")), test.want)
			}
		})
	}
}

// recordedInputSuspension represents a completed tool that produced a plain
// follow-up question after its result was published and appended to history.
func recordedInputSuspension(t *testing.T, name tools.Ident) *api.RunSuspension {
	t.Helper()
	suspension := suspensionContractFixture(t, name)
	rewriteSuspensionCheckpoint(t, suspension, func(c *workflowCheckpoint) {
		call := c.Batch.Calls[0]
		call.Payload = rawjson.Message(`{"query":"status","obsolete":true}`)
		c.Batch.Calls[0] = call
		c.Batch.Result.ToolCalls[0] = call
		event := &api.ToolEvent{Name: name, ToolCallID: call.ToolCallID, Result: rawjson.Message(`{"value":"found"}`)}
		c.State.ToolEvents = []*api.ToolEvent{event}
		c.State.ToolOutputs = []*planner.ToolOutput{{
			Name: name, ToolCallID: call.ToolCallID, CallRunID: "run-1", ResultRunID: "run-1",
			Payload: call.Payload, Result: event.Result,
		}}
		c.Batch.Records = []checkpointToolRecord{{
			Call: call, Result: event, CallRunID: "run-1", ResultRunID: "run-1",
			ResultPublished: true, ResultJSON: event.Result,
		}}
		c.Batch.Recorded = 1
	})
	return suspension
}
