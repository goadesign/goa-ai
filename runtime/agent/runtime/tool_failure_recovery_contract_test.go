package runtime

// This file verifies that failures returned by tool executors cannot choose the
// model-visible input or example recorded for a correct-call recovery.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

// TestRuntimeOwnsCorrectCallRecoveryContext exercises both in-workflow
// executors and failures returned across the tool activity boundary.
func TestRuntimeOwnsCorrectCallRecoveryContext(t *testing.T) {
	tests := []struct {
		name     string
		activity bool
	}{
		{name: "inline executor"},
		{name: "activity output", activity: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const (
				toolName    = tools.Ident("svc.tools.example")
				toolsetName = "svc.tools"
			)
			modelPayload := rawjson.Message(`{"query":"compressor temperature"}`)
			executionPayload := rawjson.Message(
				`{"query":"compressor temperature","execution_token":"server-owned"}`,
			)
			registeredExample := rawjson.Message(`{"query":"example"}`)
			maliciousPrior := rawjson.Message(`{"query":`)
			maliciousExample := rawjson.Message(`not-json`)
			issue := &tools.FieldIssue{
				Field:      "query",
				Constraint: "missing_field",
			}
			toolError := planner.NewToolError("query is not accepted")
			executorFailure := &planner.ToolFailure{
				Kind:  planner.FailureInvalidCall,
				Error: toolError,
				Recovery: planner.RecoveryDirective{
					Action:      planner.RecoveryCorrectCall,
					Issues:      []*tools.FieldIssue{issue},
					PriorInput:  maliciousPrior,
					ExampleJSON: maliciousExample,
				},
			}
			spec := newAnyJSONSpec(toolName)
			spec.Payload.ExampleJSON = registeredExample
			recorder := &recordingHooks{}
			rt := &Runtime{
				Bus:           recorder,
				logger:        telemetry.NoopLogger{},
				metrics:       telemetry.NoopMetrics{},
				tracer:        telemetry.NoopTracer{},
				RunEventStore: runloginmem.New(),
			}
			if test.activity {
				rt.toolsets = map[string]ToolsetRegistration{toolsetName: {}}
			} else {
				rt.toolsets = map[string]ToolsetRegistration{
					toolsetName: {
						Inline: true,
						Execute: wrapExecute(func(context.Context, *ToolCall) (*planner.ToolResult, error) {
							return &planner.ToolResult{Failure: executorFailure}, nil
						}),
					},
				}
			}
			seedTestToolset(rt, toolsetName, spec)

			wfCtx := &testWorkflowContext{
				ctx:         context.Background(),
				hookRuntime: rt,
			}
			if test.activity {
				wfCtx.asyncResult = ToolOutput{Failure: executorFailure}
			}
			call := ToolCall{
				Name:            toolName,
				ToolCallID:      "tool-call-1",
				ModelToolCallID: "model-tool-call-1",
				Payload:         executionPayload,
				ModelPayload:    modelPayload,
			}
			results, _, err := rt.executeToolCalls(
				wfCtx,
				"execute",
				engine.ActivityOptions{},
				"svc.agent",
				&run.Context{RunID: "run-1", SessionID: "session-1"},
				nil,
				[]ToolCall{call},
				0,
				nil,
				time.Time{},
			)

			require.NoError(t, err)
			require.Len(t, results, 1)
			require.NotNil(t, results[0].ToolResult)
			require.NotNil(t, results[0].ToolResult.Failure)
			failure := results[0].ToolResult.Failure
			require.NotSame(t, executorFailure, failure)
			require.Equal(t, planner.FailureInvalidCall, failure.Kind)
			require.NotSame(t, toolError, failure.Error)
			require.Equal(t, planner.RecoveryCorrectCall, failure.Recovery.Action)
			require.Equal(t, []*tools.FieldIssue{issue}, failure.Recovery.Issues)
			require.JSONEq(t, string(modelPayload), string(failure.Recovery.PriorInput))
			require.JSONEq(t, string(registeredExample), string(failure.Recovery.ExampleJSON))
			require.Equal(t, rawjson.Message(`{"query":`), executorFailure.Recovery.PriorInput)
			require.Equal(t, rawjson.Message(`not-json`), executorFailure.Recovery.ExampleJSON)

			if test.activity {
				require.NotNil(t, wfCtx.lastToolCall.Input)
				require.Equal(t, executionPayload, wfCtx.lastToolCall.Input.Payload)
			}

			var recorded *hooks.ToolResultReceivedEvent
			for _, event := range recorder.events {
				if resultEvent, ok := event.(*hooks.ToolResultReceivedEvent); ok {
					recorded = resultEvent
				}
			}
			require.NotNil(t, recorded)
			require.NotNil(t, recorded.Failure)
			require.Equal(t, planner.FailureInvalidCall, recorded.Failure.Kind)
			require.Equal(t, toolError.Message, recorded.Failure.Error.Message)
			require.Equal(t, planner.RecoveryCorrectCall, recorded.Failure.Recovery.Action)
			require.Equal(t, []*tools.FieldIssue{issue}, recorded.Failure.Recovery.Issues)
			require.JSONEq(t, string(modelPayload), string(recorded.Failure.Recovery.PriorInput))
			require.JSONEq(t, string(registeredExample), string(recorded.Failure.Recovery.ExampleJSON))

			call.ModelPayload[0] = '!'
			spec.Payload.ExampleJSON[0] = '!'
			maliciousPrior[0] = '!'
			maliciousExample[0] = '!'
			require.JSONEq(t, `{"query":"compressor temperature"}`, string(failure.Recovery.PriorInput))
			require.JSONEq(t, `{"query":"example"}`, string(failure.Recovery.ExampleJSON))
			require.JSONEq(t, `{"query":"compressor temperature"}`, string(recorded.Failure.Recovery.PriorInput))
			require.JSONEq(t, `{"query":"example"}`, string(recorded.Failure.Recovery.ExampleJSON))
		})
	}
}

// TestToolActivityCarriesUncanonicalizedCorrectionFailure verifies that an
// activity can return executor classification and field issues without model
// provenance. The workflow will replace the optional evidence after return.
func TestToolActivityCarriesUncanonicalizedCorrectionFailure(t *testing.T) {
	const (
		toolName    = tools.Ident("svc.tools.example")
		toolsetName = "svc.tools"
	)
	issue := &tools.FieldIssue{
		Field:      "query",
		Constraint: "invalid_length",
	}
	prior := rawjson.Message(`{"executor":`)
	example := rawjson.Message(`not-json`)
	executorFailure := &planner.ToolFailure{
		Kind:  planner.FailureDomainRejection,
		Error: planner.NewToolError("query was rejected"),
		Recovery: planner.RecoveryDirective{
			Action:      planner.RecoveryCorrectCall,
			Issues:      []*tools.FieldIssue{issue},
			PriorInput:  prior,
			ExampleJSON: example,
		},
	}
	spec := newAnyJSONSpec(toolName)
	rt := &Runtime{
		logger: telemetry.NoopLogger{},
		toolsets: map[string]ToolsetRegistration{
			toolsetName: {
				DecodeInExecutor: true,
				Execute: wrapExecute(func(context.Context, *ToolCall) (*planner.ToolResult, error) {
					return &planner.ToolResult{
						Failure: executorFailure,
					}, nil
				}),
			},
		},
	}
	seedTestToolset(rt, toolsetName, spec)

	out, err := rt.ExecuteToolActivity(context.Background(), &ToolInput{
		RunID:       "run-1",
		AgentID:     "svc.agent",
		ToolsetName: toolsetName,
		ToolName:    toolName,
		ToolCallID:  "tool-call-1",
		Payload:     rawjson.Message(`{"query":"execution"}`),
	})

	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.Failure)
	require.Equal(t, planner.FailureDomainRejection, out.Failure.Kind)
	require.Equal(t, planner.RecoveryCorrectCall, out.Failure.Recovery.Action)
	require.Equal(t, []*tools.FieldIssue{issue}, out.Failure.Recovery.Issues)
	require.Empty(t, out.Failure.Recovery.PriorInput)
	require.Empty(t, out.Failure.Recovery.ExampleJSON)
	require.NotSame(t, executorFailure, out.Failure)
	require.Equal(t, prior, executorFailure.Recovery.PriorInput)
	require.Equal(t, example, executorFailure.Recovery.ExampleJSON)
}

// TestExecutorFailureIngressRejectsOwnedContractViolations verifies that
// discarded evidence cannot hide invalid executor-owned classification,
// recovery action, or field issues.
func TestExecutorFailureIngressRejectsOwnedContractViolations(t *testing.T) {
	const toolName = tools.Ident("svc.tools.example")
	spec := newAnyJSONSpec(toolName)
	rt := &Runtime{
		toolSpecs: map[tools.Ident]tools.ToolSpec{toolName: spec},
	}
	call := ToolCall{
		Name:       toolName,
		ToolCallID: "tool-call-1",
	}
	tests := []struct {
		name    string
		mutate  func(*planner.ToolFailure)
		wantErr string
	}{
		{
			name: "invalid kind",
			mutate: func(failure *planner.ToolFailure) {
				failure.Kind = "not-a-kind"
			},
			wantErr: `unknown failure kind "not-a-kind"`,
		},
		{
			name: "invalid action",
			mutate: func(failure *planner.ToolFailure) {
				failure.Recovery.Action = "not-an-action"
			},
			wantErr: `unknown recovery action "not-an-action"`,
		},
		{
			name: "invalid issues",
			mutate: func(failure *planner.ToolFailure) {
				failure.Recovery.Issues[0].Field = ""
			},
			wantErr: "correct-call recovery issues are invalid",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failure := &planner.ToolFailure{
				Kind:  planner.FailureInvalidCall,
				Error: planner.NewToolError("query is invalid"),
				Recovery: planner.RecoveryDirective{
					Action: planner.RecoveryCorrectCall,
					Issues: []*tools.FieldIssue{{
						Field:      "query",
						Constraint: "missing_field",
					}},
					PriorInput:  rawjson.Message(`{"stale":`),
					ExampleJSON: rawjson.Message(`not-json`),
				},
			}
			test.mutate(failure)
			result := &planner.ToolResult{Failure: failure}

			_, err := rt.materializeActivityToolResult(context.Background(), call, result)

			require.ErrorContains(t, err, test.wantErr)
			require.NotSame(t, failure, result.Failure)
			require.Equal(t, rawjson.Message(`{"stale":`), failure.Recovery.PriorInput)
			require.Equal(t, rawjson.Message(`not-json`), failure.Recovery.ExampleJSON)
		})
	}
}

// TestWorkflowCorrectionUsesModelTranscriptPayload verifies that model
// provenance, rather than ModelPayload presence, authorizes correction.
func TestWorkflowCorrectionUsesModelTranscriptPayload(t *testing.T) {
	tests := []struct {
		name string
		call ToolCall
		want string
	}{
		{
			name: "model call without execution enrichment",
			call: ToolCall{
				Name:            "svc.tools.example",
				ToolCallID:      "tool-call-1",
				ModelToolCallID: "model-call-1",
				Payload:         rawjson.Message(`{"query":"status"}`),
			},
			want: `{"query":"status"}`,
		},
		{
			name: "model call with execution enrichment",
			call: ToolCall{
				Name:            "svc.tools.example",
				ToolCallID:      "tool-call-1",
				ModelToolCallID: "model-call-1",
				Payload:         rawjson.Message(`{"query":"status","credential":"private"}`),
				ModelPayload:    rawjson.Message(`{"query":"status"}`),
			},
			want: `{"query":"status"}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := newAnyJSONSpec(test.call.Name)
			spec.Payload.ExampleJSON = rawjson.Message(`{"query":"example"}`)
			result := &planner.ToolResult{
				Name:       test.call.Name,
				ToolCallID: test.call.ToolCallID,
				Failure: &planner.ToolFailure{
					Kind:  planner.FailureInvalidCall,
					Error: planner.NewToolError("query is invalid"),
					Recovery: planner.RecoveryDirective{
						Action:      planner.RecoveryCorrectCall,
						PriorInput:  rawjson.Message(`{"executor":"stale"}`),
						ExampleJSON: rawjson.Message(`{"executor":"stale"}`),
					},
				},
			}

			require.NoError(t, canonicalizeAndValidateWorkflowToolResult(spec, test.call, result))
			require.JSONEq(t, test.want, string(result.Failure.Recovery.PriorInput))
			require.JSONEq(t, `{"query":"example"}`, string(result.Failure.Recovery.ExampleJSON))
		})
	}
}

// TestAutomaticContinuationCannotRequestCorrectCall follows a runtime-authored
// continuation through inline execution and workflow collection. Its private
// cursor may execute, but it cannot become model correction evidence because
// the call has no model correlation ID.
func TestAutomaticContinuationCannotRequestCorrectCall(t *testing.T) {
	const (
		toolName      = tools.Ident("svc.tools.continue_search")
		toolsetName   = "svc.tools"
		privateCursor = "private-cursor-value"
	)
	spec := newAnyJSONSpec(toolName)
	recorder := &recordingHooks{}
	rt := &Runtime{
		Bus:           recorder,
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		RunEventStore: runloginmem.New(),
		toolsets: map[string]ToolsetRegistration{
			toolsetName: {
				Execute: wrapExecute(func(context.Context, *ToolCall) (*planner.ToolResult, error) {
					return &planner.ToolResult{
						Failure: &planner.ToolFailure{
							Kind:  planner.FailureInvalidCall,
							Error: planner.NewToolError("continuation arguments were rejected"),
							Recovery: planner.RecoveryDirective{
								Action: planner.RecoveryCorrectCall,
							},
						},
					}, nil
				}),
			},
		},
	}
	seedTestToolset(rt, toolsetName, spec)
	runCtx := run.Context{
		RunID:     "run-1",
		SessionID: "session-1",
		TurnID:    "turn-1",
	}
	plan, ok := rt.automaticContinuationPlan(runCtx, []continuationAction{{
		spec: spec,
		state: continuationState{
			rootToolCallID: "source-call-1",
			returned:       0,
		},
		executablePayload: rawjson.Message(`{"cursor":"` + privateCursor + `"}`),
	}})
	require.True(t, ok)
	require.Len(t, plan.ToolCalls, 1)
	require.Empty(t, plan.ToolCalls[0].ModelToolCallID)

	results, _, err := rt.executeToolCalls(
		&testWorkflowContext{
			ctx:         context.Background(),
			runtime:     rt,
			hookRuntime: rt,
		},
		"execute",
		engine.ActivityOptions{},
		"svc.agent",
		&runCtx,
		nil,
		plan.ToolCalls,
		0,
		nil,
		time.Time{},
	)

	require.ErrorContains(t, err, "correct-call recovery requires a model-authored call")
	require.NotContains(t, err.Error(), privateCursor)
	for _, result := range results {
		if result == nil || result.ToolResult == nil || result.ToolResult.Failure == nil {
			continue
		}
		require.NotContains(t, result.ToolResult.Failure.Error.Error(), privateCursor)
		require.Empty(t, result.ToolResult.Failure.Recovery.PriorInput)
		require.Empty(t, result.ToolResult.Failure.Recovery.ExampleJSON)
	}
	for _, event := range recorder.events {
		_, published := event.(*hooks.ToolResultReceivedEvent)
		require.False(t, published, "illegal correction must stop before result history is published")
	}
}
