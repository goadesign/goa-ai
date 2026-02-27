// Package runtime provides explicit one-shot run execution and canonical
// run-log persistence for sessionless request/response flows.
package runtime

import (
	"context"
	"errors"
	"fmt"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/run"
)

type (
	// OneShotRunInput configures one-shot run execution.
	//
	// One-shot runs are explicit sessionless executions used for request/response
	// workloads that still require durable run-log introspection.
	//
	// Contract:
	// - AgentID is required.
	// - Session ownership is always empty; one-shot runs never attach to a session.
	// - TurnID defaults to RunID when omitted.
	// - RunID defaults to a generated ID when omitted.
	OneShotRunInput struct {
		// AgentID identifies the logical agent identity for hook and run-log events.
		AgentID agent.Ident

		// RunID is the durable run identifier used by run-log storage.
		RunID string

		// TurnID identifies the logical turn for event grouping.
		TurnID string

		// Labels attaches optional metadata to the run context.
		Labels map[string]string

		// Metadata carries caller-provided structured metadata.
		Metadata map[string]any
	}
)

// RunOneShot executes one sessionless run and appends canonical lifecycle/prompt
// events to the run log.
//
// The execute callback receives a context stamped with PromptRenderHookContext so
// prompt renders inside the callback emit canonical prompt_rendered run-log events.
//
// Contract:
// - RunOneShot never creates/loads/updates session state.
// - RunOneShot never emits session stream events.
// - Hook append failures are terminal.
func (r *Runtime) RunOneShot(ctx context.Context, input OneShotRunInput, execute func(context.Context) error) error {
	if execute == nil {
		return errors.New("one-shot executor is required")
	}
	if input.AgentID == "" {
		return fmt.Errorf("%w: missing agent id", ErrAgentNotFound)
	}
	if input.RunID == "" {
		input.RunID = generateRunID(string(input.AgentID))
	}
	if input.TurnID == "" {
		input.TurnID = input.RunID
	}
	runCtx := run.Context{
		RunID:     input.RunID,
		SessionID: "",
		TurnID:    input.TurnID,
		Attempt:   1,
		Labels:    cloneLabels(input.Labels),
	}
	if err := r.publishHookErr(ctx, hooks.NewRunStartedEvent(input.RunID, input.AgentID, runCtx, input), input.TurnID); err != nil {
		return err
	}
	execCtx := withPromptRenderHookContext(ctx, PromptRenderHookContext{
		RunID:     input.RunID,
		AgentID:   input.AgentID,
		SessionID: "",
		TurnID:    input.TurnID,
	})
	execErr := execute(execCtx)
	status, phase := oneShotTerminalState(execErr)
	if err := r.publishHookErr(ctx, hooks.NewRunCompletedEvent(input.RunID, input.AgentID, "", status, phase, execErr), input.TurnID); err != nil {
		if execErr != nil {
			r.logWarn(ctx, "one-shot completion hook failed", err, "run_id", input.RunID, "agent_id", input.AgentID)
			return execErr
		}
		return err
	}
	return execErr
}

// oneShotTerminalState maps the callback result to runtime run completion status.
func oneShotTerminalState(err error) (string, run.Phase) {
	if err == nil {
		return runStatusSuccess, run.PhaseCompleted
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return runStatusCanceled, run.PhaseCanceled
	}
	return runStatusFailed, run.PhaseFailed
}
