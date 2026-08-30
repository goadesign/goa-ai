package runtime

// workflow_suspension_contract_test.go verifies the immutable public envelope
// and concrete tool-codec checks applied before continuation state is restored.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestValidateContinuationRejectsSavedServerDataOutsideCurrentContract(t *testing.T) {
	runtime := New(newTestStore())
	spec := newAnyJSONSpec("svc.lookup")
	canonicalizerCalled := false
	spec.CanonicalizeServerData = func(rawjson.Message) (rawjson.Message, error) {
		canonicalizerCalled = true
		return nil, errors.New("server data does not match the current contract")
	}
	seedTestToolSpecs(runtime, spec)
	suspension := suspensionContractFixture(t, spec.Name)
	rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
		checkpoint.State.ToolEvents = []*api.ToolEvent{{
			Name:       spec.Name,
			Result:     rawjson.Message(`{"value":"complete"}`),
			ServerData: rawjson.Message(`[{"kind":"svc.chart","data":{"removed_field":true}}]`),
			ToolCallID: "call-1",
		}}
	})

	err := runtime.ValidateContinuation(suspension)

	require.ErrorContains(t, err, "decode suspended tool result for svc.lookup")
	require.ErrorContains(t, err, "invalid server data")
	require.ErrorContains(t, err, "does not match the current contract")
	require.True(t, canonicalizerCalled)
}

func TestValidateCheckpointToolOutputEnforcesPersistedResultContract(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		bounded bool
		output  *planner.ToolOutput
		wantErr string
	}{
		{
			name: "failed result with result JSON",
			output: &planner.ToolOutput{
				Result:  rawjson.Message(`{}`),
				Failure: testToolFailure(planner.FailureUnavailable, planner.RecoveryReplan, "unavailable"),
			},
			wantErr: "failure and result JSON are both set",
		},
		{
			name: "failed result with server data",
			output: &planner.ToolOutput{
				ServerData: rawjson.Message(`[]`),
				Failure:    testToolFailure(planner.FailureUnavailable, planner.RecoveryReplan, "unavailable"),
			},
			wantErr: "failure and server data are both set",
		},
		{
			name: "failed result with bounds",
			output: &planner.ToolOutput{
				Bounds:  &agent.Bounds{Returned: 1},
				Failure: testToolFailure(planner.FailureUnavailable, planner.RecoveryReplan, "unavailable"),
			},
			wantErr: "failure and bounds are both set",
		},
		{
			name:    "invalid failure",
			output:  &planner.ToolOutput{Failure: &planner.ToolFailure{}},
			wantErr: "invalid failure",
		},
		{
			name:    "bounded success without bounds",
			bounded: true,
			output:  &planner.ToolOutput{Result: rawjson.Message(`{}`)},
			wantErr: "returned result without bounds",
		},
		{
			name: "unbounded success with bounds",
			output: &planner.ToolOutput{
				Result: rawjson.Message(`{}`),
				Bounds: &agent.Bounds{Returned: 1},
			},
			wantErr: "returned unexpected bounds metadata",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			runtime := New(newTestStore())
			spec := newAnyJSONSpec("svc.lookup")
			if test.bounded {
				spec.Bounds = &tools.BoundsSpec{}
			}
			seedTestToolSpecs(runtime, spec)
			test.output.Name = spec.Name
			test.output.ToolCallID = "call-1"
			test.output.Payload = rawjson.Message(`{}`)

			err := runtime.validateCheckpointToolOutput(t.Context(), test.output)

			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestValidateContinuationRejectsRemovedTool(t *testing.T) {
	runtime := New(newTestStore())
	spec := newAnyJSONSpec("svc.lookup")
	seedTestToolSpecs(runtime, spec)
	suspension := suspensionContractFixture(t, spec.Name)
	delete(runtime.toolSpecs, spec.Name)

	require.ErrorContains(t, runtime.ValidateContinuation(suspension), `requires unregistered tool "svc.lookup"`)
}

func TestValidateContinuationChecksSavedLimitTerminalPlans(t *testing.T) {
	runtime := New(newTestStore())
	lookup := newAnyJSONSpec("svc.lookup")
	terminal := strictLimitTerminalSpec()
	seedTestToolSpecs(runtime, lookup, terminal)
	runtime.agents["svc.agent"] = AgentRegistration{
		ID:    "svc.agent",
		Specs: []tools.ToolSpec{lookup, terminal},
	}
	suspension := suspensionContractFixture(t, lookup.Name)
	rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
		checkpoint.Policy = &PolicyOverrides{
			LimitTerminalPlans: testLimitTerminalPlans(terminal.Name),
		}
		checkpoint.RequiredTools = requiredCheckpointToolNames(checkpoint)
		suspension.RequiredTools = append([]tools.Ident(nil), checkpoint.RequiredTools...)
	})

	require.Contains(t, suspension.RequiredTools, terminal.Name)
	require.NoError(t, runtime.ValidateContinuation(suspension))

	t.Run("removed tool", func(t *testing.T) {
		delete(runtime.toolSpecs, terminal.Name)
		require.ErrorContains(t, runtime.ValidateContinuation(suspension), `requires unregistered tool "service.tools.complete"`)
		runtime.toolSpecs[terminal.Name] = terminal
	})
	t.Run("changed payload", func(t *testing.T) {
		changed := terminal
		changed.Payload.Codec = tools.JSONCodec[any]{
			FromJSON: func([]byte) (any, error) {
				return nil, errors.New("result contract changed")
			},
		}
		runtime.agents["svc.agent"] = AgentRegistration{
			ID:    "svc.agent",
			Specs: []tools.ToolSpec{lookup, changed},
		}
		require.ErrorContains(t, runtime.ValidateContinuation(suspension), "result contract changed")
	})
}

func TestValidateContinuationChecksSavedCompletionToolPolicy(t *testing.T) {
	tests := []struct {
		name          string
		mutateSpec    func(*tools.ToolSpec)
		mutate        func(*PolicyOverrides)
		omitFromAgent bool
		want          string
	}{
		{
			name:          "removed from agent",
			mutateSpec:    func(*tools.ToolSpec) {},
			mutate:        func(*PolicyOverrides) {},
			omitFromAgent: true,
			want:          `completion tool "svc.persist" is not registered for agent "svc.agent"`,
		},
		{
			name: "bookkeeping tool",
			mutateSpec: func(spec *tools.ToolSpec) {
				spec.Bookkeeping = true
			},
			mutate: func(*PolicyOverrides) {},
			want:   `completion tool "svc.persist" must be budgeted`,
		},
		{
			name:       "conflicting limit policy",
			mutateSpec: func(*tools.ToolSpec) {},
			mutate: func(runPolicy *PolicyOverrides) {
				runPolicy.LimitTerminalPlans = testLimitTerminalPlans("svc.persist")
			},
			want: "completion tool and limit terminal plans cannot be combined",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtime := New(newTestStore())
			spec := newAnyJSONSpec("svc.persist")
			tt.mutateSpec(&spec)
			seedTestToolSpecs(runtime, spec)
			var agentSpecs []tools.ToolSpec
			if !tt.omitFromAgent {
				agentSpecs = []tools.ToolSpec{spec}
			}
			runtime.agents["svc.agent"] = AgentRegistration{
				ID:    "svc.agent",
				Specs: agentSpecs,
			}
			suspension := suspensionContractFixture(t, spec.Name)
			rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
				checkpoint.Policy = &PolicyOverrides{CompletionTool: spec.Name}
				tt.mutate(checkpoint.Policy)
				checkpoint.RequiredTools = requiredCheckpointToolNames(checkpoint)
				suspension.RequiredTools = append([]tools.Ident(nil), checkpoint.RequiredTools...)
			})

			require.ErrorContains(t, runtime.ValidateContinuation(suspension), tt.want)
		})
	}
}

func TestValidateContinuationRejectsNoncurrentSuspensionVersion(t *testing.T) {
	runtime := New(newTestStore())
	spec := newAnyJSONSpec("svc.lookup")
	seedTestToolSpecs(runtime, spec)
	suspension := suspensionContractFixture(t, spec.Name)
	rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
		checkpoint.Version = "goa-ai.run-suspension.v6"
	})
	suspension.Version = "goa-ai.run-suspension.v6"

	require.EqualError(t, runtime.ValidateContinuation(suspension),
		`unsupported run suspension version "goa-ai.run-suspension.v6"`)
}

func TestValidateContinuationRejectsMissingRecoveryTurnMaximum(t *testing.T) {
	runtime := New(newTestStore())
	spec := newAnyJSONSpec("svc.lookup")
	seedTestToolSpecs(runtime, spec)
	suspension := suspensionContractFixture(t, spec.Name)
	rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
		checkpoint.State.Caps.MaxRecoveryTurns = 0
		checkpoint.State.Caps.RemainingRecoveryTurns = 0
	})

	require.ErrorContains(
		t,
		runtime.ValidateContinuation(suspension),
		"requires a positive recovery turn maximum",
	)
}

func TestValidateContinuationChecksSavedCompletionPlan(t *testing.T) {
	runtime := New(newTestStore())
	completion := newAnyJSONSpec("svc.persist")
	lookup := newAnyJSONSpec("svc.lookup")
	seedTestToolSpecs(runtime, completion, lookup)
	runtime.agents["svc.agent"] = AgentRegistration{
		ID:    "svc.agent",
		Specs: []tools.ToolSpec{completion, lookup},
	}
	suspension := suspensionContractFixture(t, completion.Name)
	rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
		await := planner.AwaitClarificationItem(&planner.AwaitClarification{
			ID: "extra-action", Question: "Continue?",
		})
		checkpoint.Policy = &PolicyOverrides{CompletionTool: completion.Name}
		checkpoint.Batch.Result.Await = planner.NewAwait(await)
		checkpoint.Batch.AwaitItems = []planner.AwaitItem{await}
		checkpoint.RequiredTools = requiredCheckpointToolNames(checkpoint)
		suspension.RequiredTools = append([]tools.Ident(nil), checkpoint.RequiredTools...)
	})

	require.ErrorContains(
		t,
		runtime.ValidateContinuation(suspension),
		`completion tool "svc.persist" must be the only action`,
	)
}

func TestValidateContinuationRejectsIncompatibleSavedResult(t *testing.T) {
	runtime := New(newTestStore())
	spec := newAnyJSONSpec("svc.lookup")
	spec.Result.Codec = tools.JSONCodec[any]{
		FromJSON: func([]byte) (any, error) {
			return nil, errors.New("value must be a string")
		},
	}
	seedTestToolSpecs(runtime, spec)
	suspension := suspensionContractFixture(t, spec.Name)
	rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
		checkpoint.State.ToolEvents = []*api.ToolEvent{{
			Name: spec.Name, Result: rawjson.Message(`{"value":42}`),
		}}
	})

	err := runtime.ValidateContinuation(suspension)
	require.ErrorContains(t, err, "decode suspended tool result")
	require.ErrorContains(t, err, "value must be a string")
}

func TestValidateContinuationRejectsIncompatibleSavedPayload(t *testing.T) {
	runtime := New(newTestStore())
	spec := newAnyJSONSpec("svc.lookup")
	spec.Payload.Codec = tools.JSONCodec[any]{
		FromJSON: func([]byte) (any, error) {
			return nil, errors.New("query must use a numeric identifier")
		},
	}
	seedTestToolSpecs(runtime, spec)
	suspension := suspensionContractFixture(t, spec.Name)

	err := runtime.ValidateContinuation(suspension)
	require.ErrorContains(t, err, "decode suspended tool payload")
	require.ErrorContains(t, err, "query must use a numeric identifier")
}

func TestValidateContinuationRejectsPublicPendingMutation(t *testing.T) {
	runtime := New(newTestStore())
	spec := newAnyJSONSpec("svc.lookup")
	seedTestToolSpecs(runtime, spec)
	suspension := suspensionContractFixture(t, spec.Name)
	suspension.Pending[0].Await.Clarification.Question = "different question"

	require.ErrorContains(t, runtime.ValidateContinuation(suspension), "pending inputs do not match checkpoint")
}

func TestValidateContinuationRejectsUnknownSavedAwaitKind(t *testing.T) {
	runtime := New(newTestStore())
	spec := newAnyJSONSpec("svc.lookup")
	seedTestToolSpecs(runtime, spec)
	suspension := suspensionContractFixture(t, spec.Name)

	rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
		checkpoint.Pending[0].Await.Kind = planner.AwaitItemKind("unknown")
	})

	require.ErrorContains(t, runtime.ValidateContinuation(suspension), "unknown saved await item kind")
}

func TestValidateContinuationRejectsUnknownSavedStepKind(t *testing.T) {
	runtime := New(newTestStore())
	spec := newAnyJSONSpec("svc.lookup")
	seedTestToolSpecs(runtime, spec)
	suspension := suspensionContractFixture(t, spec.Name)
	rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
		checkpoint.Batch.Kind = stepKind(99)
	})

	require.ErrorContains(t, runtime.ValidateContinuation(suspension), "unknown step kind")
}

func TestDecodeWorkflowCheckpointRejectsUnknownAndTrailingJSON(t *testing.T) {
	tests := []struct {
		name    string
		rewrite func(rawjson.Message) rawjson.Message
		want    string
	}{
		{
			name: "unknown field",
			rewrite: func(checkpoint rawjson.Message) rawjson.Message {
				return append(append(rawjson.Message(nil), checkpoint[:len(checkpoint)-1]...), []byte(`,"unknown":true}`)...)
			},
			want: "unknown field",
		},
		{
			name: "trailing data",
			rewrite: func(checkpoint rawjson.Message) rawjson.Message {
				return append(append(rawjson.Message(nil), checkpoint...), []byte(` true`)...)
			},
			want: "trailing data",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			suspension := suspensionContractFixture(t, "svc.lookup")
			suspension.Checkpoint = test.rewrite(suspension.Checkpoint)
			digest := sha256.Sum256(suspension.Checkpoint)
			suspension.ID = hex.EncodeToString(digest[:16])

			_, err := decodeWorkflowCheckpointState(suspension)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestDecodeWorkflowCheckpointPreservesMetadataIntegers(t *testing.T) {
	suspension := suspensionContractFixtureWithContext(
		t,
		"svc.lookup",
		"svc.agent",
		"run-1",
		nil,
		map[string]any{"sequence": json.Number("9007199254740993")},
	)

	checkpoint, err := decodeWorkflowCheckpointState(suspension)

	require.NoError(t, err)
	require.Equal(t, json.Number("9007199254740993"), checkpoint.Context.Metadata["sequence"])
}

func TestValidateContinuationRejectsNilSavedToolValue(t *testing.T) {
	runtime := New(newTestStore())
	spec := newAnyJSONSpec("svc.lookup")
	seedTestToolSpecs(runtime, spec)
	suspension := suspensionContractFixture(t, spec.Name)
	rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
		checkpoint.State.ToolOutputs = []*planner.ToolOutput{nil}
	})

	require.ErrorContains(t, runtime.ValidateContinuation(suspension), "tool output 0 is nil")
}

func TestValidateContinuationRequiresRecoveryCatalog(t *testing.T) {
	runtime := New(newTestStore())
	spec := newAnyJSONSpec("svc.lookup")
	seedTestToolSpecs(runtime, spec)
	suspension := suspensionContractFixture(t, spec.Name)
	rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
		checkpoint.State.PendingRecovery = []*planner.ToolOutput{
			recoveryOutput(spec.Name, "call-1", planner.RecoveryReplan),
		}
	})

	require.ErrorContains(
		t,
		runtime.ValidateContinuation(suspension),
		"pending recovery failures requires a recovery catalog",
	)
}

func TestValidateContinuationRecoveryCatalogVersions(t *testing.T) {
	runtime := New(newTestStore())
	spec := newAnyJSONSpec("svc.lookup")
	seedTestToolSpecs(runtime, spec)

	newCorrectCallSuspension := func(t *testing.T, version string, catalog *RecoveryCatalog) *api.RunSuspension {
		t.Helper()
		suspension := suspensionContractFixture(t, spec.Name)
		rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
			checkpoint.Version = version
			checkpoint.State.PendingRecovery = []*planner.ToolOutput{
				recoveryOutput(spec.Name, "call-1", planner.RecoveryCorrectCall),
			}
			checkpoint.State.PendingRecoveryCatalog = catalog
		})
		suspension.Version = version
		return suspension
	}

	t.Run("valid current version absent catalog", func(t *testing.T) {
		suspension := newCorrectCallSuspension(t, api.RunSuspensionVersion, nil)
		require.NoError(t, runtime.ValidateContinuation(suspension))
		checkpoint, err := runtime.decodeWorkflowCheckpoint(suspension)
		require.NoError(t, err)
		state, err := runtime.restoreCheckpointState(checkpoint.State)
		require.NoError(t, err)
		_, catalog := toolRecovery(state.PendingRecovery)
		require.Equal(t, &RecoveryCatalog{Tools: []tools.Ident{spec.Name}}, catalog)
	})

	t.Run("invalid current version contradictory catalog", func(t *testing.T) {
		suspension := newCorrectCallSuspension(
			t,
			api.RunSuspensionVersion,
			&RecoveryCatalog{Tools: []tools.Ident{spec.Name}},
		)
		require.ErrorContains(
			t,
			runtime.ValidateContinuation(suspension),
			"correct-call recovery cannot carry a recovery catalog",
		)
	})

	t.Run("valid current version replan serialized catalog", func(t *testing.T) {
		suspension := suspensionContractFixture(t, spec.Name)
		rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
			checkpoint.State.PendingRecovery = []*planner.ToolOutput{
				recoveryOutput(spec.Name, "call-1", planner.RecoveryReplan),
			}
			checkpoint.State.PendingRecoveryCatalog = &RecoveryCatalog{
				Tools: []tools.Ident{spec.Name},
			}
		})
		require.NoError(t, runtime.ValidateContinuation(suspension))
	})
}

func TestLoadPlannerToolOutputsCombinesDifferentRunLogs(t *testing.T) {
	runtime := New(newTestStore())
	spec := newAnyJSONSpec("svc.lookup")
	seedTestToolSpecs(runtime, spec)
	require.NoError(t, runtime.publishHookErr(t.Context(), hooks.NewToolCallScheduledEvent(
		"run-call", "svc.agent", "session-1", spec.Name, "call-1",
		rawjson.Message(`{"query":"temperature"}`), "queue", "", 0,
	), "turn-1"))
	result := rawjson.Message(`{"value":"42"}`)
	require.NoError(t, runtime.publishHookErr(t.Context(), hooks.NewToolResultReceivedEvent(
		"run-result", "svc.agent", "session-1", "run-call", spec.Name, "call-1", "",
		result, nil, "", nil, 0, nil, nil,
	), "turn-1"))

	outputs, err := runtime.loadPlannerToolOutputs(t.Context(), []*api.ToolOutputRef{{
		CallRunID: "run-call", ResultRunID: "run-result", ToolCallID: "call-1",
	}})
	require.NoError(t, err)
	require.Len(t, outputs, 1)
	require.Equal(t, "run-call", outputs[0].CallRunID)
	require.Equal(t, "run-result", outputs[0].ResultRunID)
	require.JSONEq(t, `{"query":"temperature"}`, string(outputs[0].Payload))
	require.JSONEq(t, `{"value":"42"}`, string(outputs[0].Result))
}

func suspensionContractFixture(t *testing.T, tool tools.Ident) *api.RunSuspension {
	return suspensionContractFixtureWithContext(t, tool, "svc.agent", "run-1", nil, nil)
}

// suspensionContractFixtureWithContext creates a valid suspension whose saved
// session and labels can exercise continuation start checks.
func suspensionContractFixtureWithContext(t *testing.T, tool tools.Ident, agentID, runID string, labels map[string]string, metadata map[string]any) *api.RunSuspension {
	t.Helper()
	const sessionID = "session-1"
	await := planner.AwaitClarificationItem(&planner.AwaitClarification{
		ID: "clarification-1", Question: "Which facility?",
	})
	calls := []ToolCall{{
		Name: tool, ToolCallID: "call-1", Payload: rawjson.Message(`{"query":"status"}`),
	}}
	result := &PlanResult{ToolCalls: calls}
	checkpoint := &workflowCheckpoint{
		Version:        api.RunSuspensionVersion,
		AgentID:        agentID,
		SessionID:      sessionID,
		PreviousRunID:  runID,
		PreviousTurnID: "turn-1",
		Context: checkpointRunContext{
			Labels:   cloneLabels(labels),
			Metadata: cloneMetadata(metadata),
		},
		State: checkpointRunState{
			NextAttempt: 2,
			Caps: policy.CapsState{
				MaxRecoveryTurns:       policy.DefaultMaxRecoveryTurns,
				RemainingRecoveryTurns: policy.DefaultMaxRecoveryTurns,
			},
		},
		Batch:   checkpointStepBatch{Result: result, Calls: calls, Kind: stepKindTools},
		Pending: []checkpointPendingInput{{Await: &await, CallRunID: runID}},
	}
	required := requiredCheckpointToolNames(checkpoint)
	checkpoint.RequiredTools = required
	payload, err := json.Marshal(checkpoint)
	require.NoError(t, err)
	pending, err := publicPendingInputs(checkpoint.Pending)
	require.NoError(t, err)
	digest := sha256.Sum256(payload)
	return &api.RunSuspension{
		ID:            hex.EncodeToString(digest[:16]),
		Version:       api.RunSuspensionVersion,
		Checkpoint:    payload,
		Pending:       pending,
		RequiredTools: required,
	}
}

// rewriteSuspensionCheckpoint applies one corruption case and recomputes the
// public content identifier so tests reach private checkpoint validation.
func rewriteSuspensionCheckpoint(t *testing.T, suspension *api.RunSuspension, mutate func(*workflowCheckpoint)) {
	t.Helper()
	var checkpoint workflowCheckpoint
	require.NoError(t, json.Unmarshal(suspension.Checkpoint, &checkpoint))
	mutate(&checkpoint)
	payload, err := json.Marshal(&checkpoint)
	require.NoError(t, err)
	digest := sha256.Sum256(payload)
	suspension.Checkpoint = payload
	suspension.ID = hex.EncodeToString(digest[:16])
}
