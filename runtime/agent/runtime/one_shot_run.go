// Package runtime provides explicit one-shot run execution and canonical
// run-log persistence for sessionless request/response flows.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
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
// The execute callback receives a context that records prompt versions. The
// runtime stores those records before it completes the run.
// An exact start replay of a closed run returns engine.ErrWorkflowCompleted
// before invoking the callback or writing any later records.
//
// Contract:
// - RunOneShot stores run metadata but never creates, loads, or updates a session.
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
	storageCtx := context.WithoutCancel(ctx)
	startResult, err := r.recordOneShotEvent(storageCtx, hooks.NewRunStartedEvent(
		input.RunID,
		input.AgentID,
		"",
		"",
		"",
		runCtx.Labels,
	), input.TurnID)
	if err != nil {
		return err
	}
	if session.IsTerminalRunStatus(startResult.SynchronousStart.RunStatus) {
		return engine.ErrWorkflowCompleted
	}
	promptRenders := prompt.NewRenderRecorder()
	execCtx := prompt.WithRenderRecorder(ctx, promptRenders)
	execErr := execute(execCtx)
	for _, rendered := range promptRenders.Events() {
		if _, err := r.recordOneShotEvent(storageCtx, hooks.NewPromptRenderedEvent(
			input.RunID,
			input.AgentID,
			"",
			rendered.PromptID,
			rendered.Version,
			rendered.Scope,
		), input.TurnID); err != nil {
			execErr = errors.Join(execErr, fmt.Errorf("record one-shot prompt: %w", err))
			break
		}
	}
	status := terminalRunStatusForError(execErr)
	phase := terminalRunPhaseForStatus(status)
	completed, err := r.buildRunCompletedEvent(storageCtx, input.RunID, input.AgentID, "", status, phase, runCtx.Labels, execErr)
	if err != nil {
		return errors.Join(execErr, fmt.Errorf("build one-shot terminal record: %w", err))
	}
	if _, err := r.recordOneShotEvent(storageCtx, completed, input.TurnID); err != nil {
		return errors.Join(execErr, fmt.Errorf("record one-shot terminal result: %w", err))
	}
	return execErr
}

// recordOneShotEvent prepares one immutable record and retries temporary store
// failures without running the caller's work again. Permanent state conflicts
// return immediately. The caller receives the accepted activity result.
func (r *Runtime) recordOneShotEvent(ctx context.Context, event hooks.Event, turnID string) (*api.StorageActivityResult, error) {
	record, err := prepareHookRecordInput(ctx, event, turnID)
	if err != nil {
		return nil, err
	}
	var command *api.StorageActivityCommand
	switch event.Type() {
	case hooks.RunStarted:
		// Callback runs have no initial messages. Publish that explicit empty
		// history before accepting the run, just as prepared workflows do.
		writer, err := stageLiteralHistory(ctx, synchronousSeedWriter{r: r}, storage.SeedDeclaration{
			AgentID: event.AgentID(), RunID: event.RunID(), CommandID: event.RunID(), AttemptID: event.RunID(), Kind: storage.SeedLiteral,
		}, nil)
		if err != nil {
			return nil, err
		}
		command = &api.StorageActivityCommand{SynchronousStart: &api.SynchronousRunStartCommand{SeedEndID: writer.endID, Started: record}}
		compiled, err := json.Marshal(command)
		if err != nil {
			return nil, err
		}
		if err := writer.publish(ctx, compiled); err != nil {
			return nil, err
		}

	case hooks.RunCompleted:
		command = &api.StorageActivityCommand{
			Terminal: &api.RunTerminalCommand{Record: record},
		}
	default:
		command = appendStorageCommand(record)
	}
	return r.storageCommandUntilApplied(ctx, command)
}
