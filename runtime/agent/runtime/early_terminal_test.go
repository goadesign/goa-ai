// These tests drive actual accepted workflows through failures before first
// start. They assert durable terminal identity and the absence of model work or
// invented Run records, rather than treating an engine error as completion.
package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunterminal "goa.design/goa-ai/registry/gen/runterminal"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/tools"
)

type earlyTerminalCaptureStore struct {
	*storageinmem.Store
	terminal   *genrunterminal.UnstartedExecutionTerminal
	stopReason string
	failures   int
	attempts   []genrunterminal.UnstartedExecutionTerminal
}

func (s *earlyTerminalCaptureStore) BeginRunPreparationOperation(ctx context.Context, command storage.RunPreparationBegin) (genrunterminal.PreparationDecision, error) {
	decision, err := s.Store.BeginRunPreparationOperation(ctx, command)
	if err != nil {
		return decision, err
	}
	if s.stopReason != "" {
		return genrunterminal.PreparationDecision{State: genrunterminal.NewPreparationStateStop(genrunterminal.PreparationStateBranchStop(s.stopReason))}, nil
	}
	return decision, nil
}

func (s *earlyTerminalCaptureStore) RecordUnstartedExecutionTerminal(ctx context.Context, command genrunterminal.UnstartedExecutionTerminal) (genrunterminal.UnstartedExecutionTerminalResult, error) {
	s.attempts = append(s.attempts, command)
	if s.failures > 0 {
		s.failures--
		return genrunterminal.UnstartedExecutionTerminalResult{}, errors.New("storage temporarily unavailable")
	}
	result, err := s.Store.RecordUnstartedExecutionTerminal(ctx, command)
	if err == nil {
		s.terminal = &command
	}
	return result, err
}

func TestWorkflowStoresEarlyTerminalBeforeRunStart(t *testing.T) {
	for _, failure := range []string{"missing provider", "preparation rejected", "canceled preparation", "terminal storage outage", "stopped before preparation"} {
		t.Run(failure, func(t *testing.T) {
			resolution := testNativeRegistryResolution("revision/1")
			operation := resolution.Toolset.Tools[0]
			resolved, err := registrycontract.Resolve(resolution)
			require.NoError(t, err)
			spec := resolved.Specs[tools.Ident(operation.Name)]
			store := &earlyTerminalCaptureStore{Store: storageinmem.New()}
			if failure == "terminal storage outage" {
				store.failures = 1
			}
			if failure == "stopped before preparation" {
				store.stopReason = "owner-requested-stop"
			}
			adapter := engineinmem.New()
			rt := New(store, WithEngine(adapter))
			definition := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", []tools.ToolSpec{spec}, nil)
			planned := 0
			prepared := 0
			require.NoError(t, rt.RegisterAgent(t.Context(), AgentRegistration{
				Definition: definition, WorkflowHandler: rt.ExecuteWorkflow,
				PlanActivityName: "early.plan", ResumeActivityName: "early.resume", ExecuteToolActivity: "early.execute",
				Planner: &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
					planned++
					return nil, errors.New("planner must never run")
				}},
			}))
			preparing := make(chan struct{})
			require.NoError(t, rt.RegisterAgentToolResolver(definition.route.ID, func(ctx context.Context, _ genregistry.ExecutionInvocation, _ string, _ *ToolCall) (*AgentToolConfiguration, error) {
				prepared++
				if failure == "canceled preparation" {
					close(preparing)
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return nil, engine.MarkActivityErrorNonRetryable(errors.New("preparation rejected"))
			}))
			if failure == "missing provider" {
				delete(rt.agents, definition.route.ID)
			}
			handle, err := rt.StartOneShotOperation(t.Context(), "early-run", operation, rawjson.Message(`{"value":1}`), nil, nil)
			require.NoError(t, err)
			if failure == "canceled preparation" {
				<-preparing
				requester, ok := adapter.(engine.CancellationRequester)
				require.True(t, ok)
				require.NoError(t, requester.RequestCancellation(t.Context(), "early-run", engine.CancellationRequest{RunID: "early-run", Reason: run.CancellationReasonUserRequested}))
			}
			_, err = handle.Wait(t.Context())
			require.Error(t, err)
			require.NotNil(t, store.terminal)
			assert.Equal(t, "early-run", store.terminal.RunID)
			root, ok := store.terminal.Origin.Invocation.AsRoot()
			require.True(t, ok)
			assert.Equal(t, "early-run", root.RunID)
			completion, err := adapter.QueryRunCompletion(t.Context(), "early-run")
			require.NoError(t, err)
			assert.Equal(t, completion.StartRequestDigest[:], store.terminal.RequestDigest)
			_, err = store.LoadRun(t.Context(), "early-run")
			assert.ErrorIs(t, err, session.ErrRunNotFound) //nolint:testifylint // Later checks do not depend on whether the Run lookup failed.
			assert.Zero(t, planned)
			if failure == "terminal storage outage" {
				require.Len(t, store.attempts, 2)
				assert.Equal(t, store.attempts[0], store.attempts[1], "storage retries retain the same decision and time")
			}
			switch failure {
			case "stopped before preparation":
				assert.Zero(t, prepared)
				reason, ok := store.terminal.Outcome.AsCanceled()
				require.True(t, ok)
				assert.Equal(t, store.stopReason, string(reason))
			case "canceled preparation":
				reason, ok := store.terminal.Outcome.AsCanceled()
				assert.True(t, ok)
				assert.Equal(t, run.CancellationReasonUserRequested, string(reason))
			default:
				_, ok := store.terminal.Outcome.AsFailed()
				assert.True(t, ok)
			}
		})
	}
}
