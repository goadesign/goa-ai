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

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestValidateContinuationRejectsSavedServerDataOutsideCurrentContract(t *testing.T) {
	runtime := New()
	spec := newAnyJSONSpec("svc.lookup", "svc")
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

	require.ErrorContains(t, err, "validate suspended svc.lookup server data")
	require.ErrorContains(t, err, "does not match the current contract")
	require.True(t, canonicalizerCalled)
}

func TestValidateContinuationRejectsRemovedTool(t *testing.T) {
	runtime := New()
	spec := newAnyJSONSpec("svc.lookup", "svc")
	seedTestToolSpecs(runtime, spec)
	suspension := suspensionContractFixture(t, spec.Name)
	delete(runtime.toolSpecs, spec.Name)

	require.ErrorContains(t, runtime.ValidateContinuation(suspension), `requires unregistered tool "svc.lookup"`)
}

func TestValidateContinuationChecksSavedLimitTerminalPlans(t *testing.T) {
	runtime := New()
	lookup := newAnyJSONSpec("svc.lookup", "svc")
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
			runtime := New()
			spec := newAnyJSONSpec("svc.persist", "svc")
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
	runtime := New()
	spec := newAnyJSONSpec("svc.lookup", "svc")
	seedTestToolSpecs(runtime, spec)
	suspension := suspensionContractFixture(t, spec.Name)
	rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
		checkpoint.Version = "goa-ai.run-suspension.v2"
	})
	suspension.Version = "goa-ai.run-suspension.v2"

	require.EqualError(t, runtime.ValidateContinuation(suspension),
		`unsupported run suspension version "goa-ai.run-suspension.v2"`)
}

func TestDecodeVersionFourSuspensionPreservesLegacyFailureStreak(t *testing.T) {
	spec := newAnyJSONSpec("svc.lookup", "svc")
	suspension := suspensionContractFixture(t, spec.Name)
	rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
		checkpoint.Version = api.RunSuspensionVersionV4
		checkpoint.Policy = &PolicyOverrides{
			LimitTerminalPlans: testLimitTerminalPlans(spec.Name),
		}
		checkpoint.State.Caps = policy.CapsState{
			MaxRecoveryTurns:       2,
			RemainingRecoveryTurns: 1,
		}
	})
	suspension.Version = api.RunSuspensionVersionV4
	require.Contains(t, string(suspension.Checkpoint), `"FailedToolCallCap"`)
	require.NotContains(t, string(suspension.Checkpoint), `"RecoveryCap"`)

	checkpoint, err := decodeWorkflowCheckpointState(suspension)

	require.NoError(t, err)
	require.True(t, checkpoint.State.LegacyFailureStreak)
	require.Equal(t, 2, checkpoint.State.Caps.MaxRecoveryTurns)
	require.Equal(t, 1, checkpoint.State.Caps.RemainingRecoveryTurns)
	require.Equal(t, spec.Name, checkpoint.Policy.LimitTerminalPlans.RecoveryCap.Name)
}

func TestValidateContinuationChecksSavedCompletionPlan(t *testing.T) {
	runtime := New()
	completion := newAnyJSONSpec("svc.persist", "svc")
	lookup := newAnyJSONSpec("svc.lookup", "svc")
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
	runtime := New()
	spec := newAnyJSONSpec("svc.lookup", "svc")
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
	runtime := New()
	spec := newAnyJSONSpec("svc.lookup", "svc")
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
	runtime := New()
	spec := newAnyJSONSpec("svc.lookup", "svc")
	seedTestToolSpecs(runtime, spec)
	suspension := suspensionContractFixture(t, spec.Name)
	suspension.Pending[0].Await.Clarification.Question = "different question"

	require.ErrorContains(t, runtime.ValidateContinuation(suspension), "pending inputs do not match checkpoint")
}

func TestValidateContinuationRejectsUnknownSavedAwaitKind(t *testing.T) {
	runtime := New()
	spec := newAnyJSONSpec("svc.lookup", "svc")
	seedTestToolSpecs(runtime, spec)
	suspension := suspensionContractFixture(t, spec.Name)

	rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
		checkpoint.Pending[0].Await.Kind = planner.AwaitItemKind("unknown")
	})

	require.ErrorContains(t, runtime.ValidateContinuation(suspension), "unknown saved await item kind")
}

func TestValidateContinuationRejectsUnknownSavedStepKind(t *testing.T) {
	runtime := New()
	spec := newAnyJSONSpec("svc.lookup", "svc")
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
	runtime := New()
	spec := newAnyJSONSpec("svc.lookup", "svc")
	seedTestToolSpecs(runtime, spec)
	suspension := suspensionContractFixture(t, spec.Name)
	rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
		checkpoint.State.ToolOutputs = []*planner.ToolOutput{nil}
	})

	require.ErrorContains(t, runtime.ValidateContinuation(suspension), "tool output 0 is nil")
}

func TestValidateContinuationRequiresRecoveryCatalog(t *testing.T) {
	runtime := New()
	spec := newAnyJSONSpec("svc.lookup", "svc")
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

func TestLoadPlannerToolOutputsCombinesDifferentRunLogs(t *testing.T) {
	runtime := New()
	spec := newAnyJSONSpec("svc.lookup", "svc")
	seedTestToolSpecs(runtime, spec)
	require.NoError(t, runtime.publishHookErr(t.Context(), hooks.NewToolCallScheduledEvent(
		"run-call", "svc.agent", "session-1", spec.Name, "call-1",
		rawjson.Message(`{"query":"temperature"}`), "queue", "", 0,
	), "turn-1"))
	result := rawjson.Message(`{"value":"42"}`)
	require.NoError(t, runtime.publishHookErr(t.Context(), hooks.NewToolResultReceivedEvent(
		"run-result", "svc.agent", "session-1", "run-call", spec.Name, "call-1", "",
		result, len(result), false, "", nil, "", nil, 0, nil, nil,
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
		State:   checkpointRunState{NextAttempt: 2},
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
