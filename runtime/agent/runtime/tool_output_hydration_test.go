// These tests verify that tool-output hydration rejects malformed canonical
// run-log pages before decoding planner-visible tool state.
package runtime

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/runlog"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	"goa.design/goa-ai/runtime/agent/tools"
)

type nilEventRunlog struct{}

// Append panics because hydration must only read the test store.
func (nilEventRunlog) Append(context.Context, *runlog.Event) (runlog.AppendResult, error) {
	panic("Append must not be called")
}

// List returns the impossible nil event that hydration must reject.
func (nilEventRunlog) List(context.Context, string, string, int) (runlog.Page, error) {
	return runlog.Page{Events: []*runlog.Event{nil}}, nil
}

func TestLoadCanonicalToolEventsRejectsNilStoredEvent(t *testing.T) {
	t.Parallel()

	runtime := &Runtime{RunEventStore: nilEventRunlog{}}
	_, err := runtime.loadCanonicalToolEvents(
		context.Background(),
		"run-1",
		map[string]struct{}{"another-call": {}},
	)

	require.EqualError(
		t,
		err,
		`runtime: nil event from run log during tool hydration (run_id=run-1 page_cursor="" index=0)`,
	)
}

func TestPlannerToolOutputFromCanonicalEventsRequiresMatchingIdentity(t *testing.T) {
	t.Parallel()

	const (
		callRunID   = "call-run"
		resultRunID = "result-run"
		toolCallID  = "call-1"
	)
	toolName := tools.Ident("svc.tools.lookup")
	tests := []struct {
		name            string
		scheduledParent string
		resultParent    string
		scheduledCallID string
		resultCallID    string
		eventCallRunID  string
		wantErr         string
	}{
		{
			name:           "matching call run",
			eventCallRunID: callRunID,
		},
		{
			name:            "matching nested call",
			scheduledParent: "parent-1",
			resultParent:    "parent-1",
			eventCallRunID:  callRunID,
		},
		{
			name:            "different scheduled call",
			scheduledCallID: "other-call",
			eventCallRunID:  callRunID,
			wantErr: "runtime: canonical tool schedule call mismatch " +
				"(run_id=call-run tool_call_id=call-1 event_tool_call_id=other-call)",
		},
		{
			name:           "different result call",
			resultCallID:   "other-call",
			eventCallRunID: callRunID,
			wantErr: "runtime: canonical tool result identity mismatch " +
				"(call_run_id=call-run result_run_id=result-run tool_call_id=call-1): " +
				`tool result call "other-call" does not match scheduled call "call-1"`,
		},
		{
			name:           "same tool identity from another call run",
			eventCallRunID: "other-call-run",
			wantErr: "runtime: canonical tool result identity mismatch " +
				"(call_run_id=call-run result_run_id=result-run tool_call_id=call-1): " +
				`tool result call run "other-call-run" does not match scheduled run "call-run"`,
		},
		{
			name:            "different parent call",
			scheduledParent: "parent-1",
			resultParent:    "parent-2",
			eventCallRunID:  callRunID,
			wantErr: "runtime: canonical tool result identity mismatch " +
				"(call_run_id=call-run result_run_id=result-run tool_call_id=call-1): " +
				`tool result parent "parent-2" does not match scheduled parent "parent-1" for call "call-1"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			scheduledCallID := test.scheduledCallID
			if scheduledCallID == "" {
				scheduledCallID = toolCallID
			}
			resultCallID := test.resultCallID
			if resultCallID == "" {
				resultCallID = toolCallID
			}
			callEvents := &canonicalToolEvents{scheduled: hooks.NewToolCallScheduledEvent(
				callRunID,
				"svc.agent",
				"session",
				toolName,
				scheduledCallID,
				nil,
				"",
				test.scheduledParent,
				0,
			)}
			resultEvents := &canonicalToolEvents{result: hooks.NewToolResultReceivedEvent(
				resultRunID,
				"svc.agent",
				"session",
				test.eventCallRunID,
				toolName,
				resultCallID,
				test.resultParent,
				rawjson.Message(`{}`),
				nil,
				"",
				nil,
				0,
				nil,
				nil,
			)}

			output, err := plannerToolOutputFromCanonicalEvents(
				tools.ToolSpec{Result: tools.TypeSpec{Codec: tools.AnyJSONCodec}},
				callRunID,
				resultRunID,
				toolCallID,
				callEvents,
				resultEvents,
			)
			if test.wantErr != "" {
				require.Nil(t, output)
				require.EqualError(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, callRunID, output.CallRunID)
			require.Equal(t, resultRunID, output.ResultRunID)
			require.Equal(t, toolCallID, output.ToolCallID)
			require.Equal(t, toolName, output.Name)
			require.JSONEq(t, `{}`, string(output.Result))
		})
	}
}

func TestLoadCanonicalToolEventsValidatesToolResultPlacement(t *testing.T) {
	t.Parallel()

	toolName := tools.Ident("svc.tools.lookup")
	schedule := func(runID string) hooks.Event {
		return hooks.NewToolCallScheduledEvent(
			runID,
			"svc.agent",
			"session-1",
			toolName,
			"call-1",
			rawjson.Message(`{"query":"x"}`),
			"",
			"",
			0,
		)
	}
	result := func(resultRunID, callRunID string) hooks.Event {
		return hooks.NewToolResultReceivedEvent(
			resultRunID,
			"svc.agent",
			"session-1",
			callRunID,
			toolName,
			"call-1",
			"",
			rawjson.Message(`{}`),
			nil,
			"",
			nil,
			time.Second,
			nil,
			nil,
		)
	}
	tests := []struct {
		name    string
		runID   string
		events  []hooks.Event
		wantErr string
	}{
		{
			name:   "same run with matching local schedule",
			runID:  "call-run",
			events: []hooks.Event{schedule("call-run"), result("call-run", "call-run")},
		},
		{
			name:    "same run without local schedule",
			runID:   "call-run",
			events:  []hooks.Event{result("call-run", "call-run")},
			wantErr: "tool schedule is required",
		},
		{
			name:   "cross run without local schedule",
			runID:  "result-run",
			events: []hooks.Event{result("result-run", "call-run")},
		},
		{
			name:    "cross run with local schedule",
			runID:   "result-run",
			events:  []hooks.Event{schedule("result-run"), result("result-run", "call-run")},
			wantErr: "reuses a schedule from result run",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store := runloginmem.New()
			for index, event := range test.events {
				appendHistoricalHookEvent(
					t,
					store,
					event,
					fmt.Sprintf("%s-%d", test.name, index),
					int64(index+1),
				)
			}
			runtime := &Runtime{RunEventStore: store}
			events, err := runtime.loadCanonicalToolEvents(
				context.Background(),
				test.runID,
				map[string]struct{}{"call-1": {}},
			)
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, events["call-1"].result)
		})
	}
}

func TestLoadCanonicalToolEventsRetainsOnlyWantedCalls(t *testing.T) {
	t.Parallel()

	store := runloginmem.New()
	toolName := tools.Ident("svc.tools.lookup")
	for index, callID := range []string{"unrelated-1", "wanted", "unrelated-2"} {
		appendHistoricalHookEvent(t, store, hooks.NewToolCallScheduledEvent(
			"run-1",
			"svc.agent",
			"session-1",
			toolName,
			callID,
			rawjson.Message(`{}`),
			"",
			"",
			0,
		), fmt.Sprintf("schedule-%d", index), int64(index*2+1))
		appendHistoricalHookEvent(t, store, hooks.NewToolResultReceivedEvent(
			"run-1",
			"svc.agent",
			"session-1",
			"run-1",
			toolName,
			callID,
			"",
			rawjson.Message(`{}`),
			nil,
			"",
			nil,
			time.Second,
			nil,
			nil,
		), fmt.Sprintf("result-%d", index), int64(index*2+2))
	}

	runtime := &Runtime{RunEventStore: store}
	events, err := runtime.loadCanonicalToolEvents(
		context.Background(),
		"run-1",
		map[string]struct{}{"wanted": {}},
	)

	require.NoError(t, err)
	require.Len(t, events, 1)
	require.NotNil(t, events["wanted"].scheduled)
	require.NotNil(t, events["wanted"].result)
}

func TestLoadCanonicalToolEventsDecodesUnrelatedCalls(t *testing.T) {
	t.Parallel()

	store := runloginmem.New()
	_, err := store.Append(context.Background(), &runlog.Event{
		EventKey:  "malformed-unrelated",
		RunID:     "run-1",
		AgentID:   "svc.agent",
		SessionID: "session-1",
		Type:      hooks.ToolCallScheduled,
		Payload:   []byte(`{`),
		Timestamp: time.UnixMilli(1).UTC(),
	})
	require.NoError(t, err)

	runtime := &Runtime{RunEventStore: store}
	_, err = runtime.loadCanonicalToolEvents(
		context.Background(),
		"run-1",
		map[string]struct{}{"wanted": {}},
	)

	require.Error(t, err)
}

func TestLoadPlannerToolOutputsAcceptsCrossRunContinuationResult(t *testing.T) {
	t.Parallel()

	store := runloginmem.New()
	toolName := tools.Ident("svc.tools.lookup")
	appendHistoricalHookEvent(t, store, hooks.NewToolCallScheduledEvent(
		"call-run",
		"svc.agent",
		"session-1",
		toolName,
		"call-1",
		rawjson.Message(`{"query":"x"}`),
		"",
		"parent-1",
		0,
	), "schedule", 1)
	appendHistoricalHookEvent(t, store, hooks.NewToolResultReceivedEvent(
		"result-run",
		"svc.agent",
		"session-1",
		"call-run",
		toolName,
		"call-1",
		"parent-1",
		rawjson.Message(`{"value":"found"}`),
		nil,
		"",
		nil,
		time.Second,
		nil,
		nil,
	), "result", 2)

	runtime := &Runtime{RunEventStore: store}
	seedTestToolSpecs(runtime, newAnyJSONSpec(toolName))
	outputs, err := runtime.loadPlannerToolOutputs(context.Background(), []*api.ToolOutputRef{{
		CallRunID:   "call-run",
		ResultRunID: "result-run",
		ToolCallID:  "call-1",
	}})

	require.NoError(t, err)
	require.Len(t, outputs, 1)
	require.Equal(t, "call-run", outputs[0].CallRunID)
	require.Equal(t, "result-run", outputs[0].ResultRunID)
	require.Equal(t, "call-1", outputs[0].ToolCallID)
	require.Equal(t, toolName, outputs[0].Name)
	require.JSONEq(t, `{"value":"found"}`, string(outputs[0].Result))
}

func TestPlannerToolOutputFromCanonicalEventsValidatesResultContract(t *testing.T) {
	t.Parallel()

	toolName := tools.Ident("svc.tools.lookup")
	callEvents := &canonicalToolEvents{scheduled: hooks.NewToolCallScheduledEvent(
		"run-1",
		"svc.agent",
		"session",
		toolName,
		"call-1",
		nil,
		"",
		"",
		0,
	)}
	tests := []struct {
		name    string
		spec    tools.ToolSpec
		result  *hooks.ToolResultReceivedEvent
		wantErr string
	}{
		{
			name: "result-bearing tool with empty result",
			spec: tools.ToolSpec{Result: tools.TypeSpec{Codec: tools.AnyJSONCodec}},
			result: &hooks.ToolResultReceivedEvent{
				CallRunID:  "run-1",
				ToolName:   toolName,
				ToolCallID: "call-1",
			},
			wantErr: "tool result is missing",
		},
		{
			name: "no-result tool with empty result",
			result: &hooks.ToolResultReceivedEvent{
				CallRunID:  "run-1",
				ToolName:   toolName,
				ToolCallID: "call-1",
			},
		},
		{
			name: "no-result tool with unexpected result",
			result: &hooks.ToolResultReceivedEvent{
				CallRunID:   "run-1",
				ToolName:    toolName,
				ToolCallID:  "call-1",
				ResultJSON:  rawjson.Message(`{}`),
				ResultBytes: 2,
			},
			wantErr: "does not define a result but contains one",
		},
		{
			name: "wrong byte count",
			spec: tools.ToolSpec{Result: tools.TypeSpec{Codec: tools.AnyJSONCodec}},
			result: &hooks.ToolResultReceivedEvent{
				CallRunID:   "run-1",
				ToolName:    toolName,
				ToolCallID:  "call-1",
				ResultJSON:  rawjson.Message(`{}`),
				ResultBytes: 3,
			},
			wantErr: "canonical tool result size mismatch",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			resultEvent := hooks.NewToolResultReceivedEvent(
				"run-1",
				"svc.agent",
				"session",
				"run-1",
				test.result.ToolName,
				test.result.ToolCallID,
				"",
				test.result.ResultJSON,
				nil,
				"",
				nil,
				0,
				nil,
				nil,
			)
			resultEvent.ResultBytes = test.result.ResultBytes
			output, err := plannerToolOutputFromCanonicalEvents(
				test.spec,
				"run-1",
				"run-1",
				"call-1",
				callEvents,
				&canonicalToolEvents{result: resultEvent},
			)

			if test.wantErr != "" {
				require.Nil(t, output)
				require.ErrorContains(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, output)
			require.Empty(t, output.Result)
		})
	}
}
