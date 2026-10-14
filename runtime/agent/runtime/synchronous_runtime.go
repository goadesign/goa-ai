// Package runtime runs synchronous callbacks and writes their lifecycle
// without submitting engine work. The caller supplies storage, prompts, and
// observers; callbacks create no Session and claim no engine-owned request.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/trace"
	genrunterminal "goa.design/goa-ai/registry/gen/runterminal"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

type (
	// SynchronousRuntime runs sessionless callbacks and records their lifecycle.
	// It exposes no engine submission, suspension, or workflow repair methods.
	SynchronousRuntime struct {
		// PromptRegistry registers and renders the callback's prompt versions.
		PromptRegistry *prompt.Registry
		store          storage.SynchronousStore
		bus            hooks.Bus
	}
)

// NewSynchronous receives the callback store, prompt registry, and hook bus.
// Missing dependencies fail construction before any callback can start.
func NewSynchronous(store storage.SynchronousStore, registry *prompt.Registry, bus hooks.Bus) (*SynchronousRuntime, error) {
	if store == nil || registry == nil || bus == nil {
		return nil, errors.New("synchronous runtime requires store, prompt registry, and hook bus")
	}
	return &SynchronousRuntime{PromptRegistry: registry, store: store, bus: bus}, nil
}

// RunOneShot remains available during the caller migration. It delegates to the
// explicit callback capability; the engine never handles synchronous commands.
func (r *Runtime) RunOneShot(ctx context.Context, input OneShotRunInput, execute func(context.Context) error) error {
	callback, err := NewSynchronous(r.Store, r.PromptRegistry, r.Bus)
	if err != nil {
		return err
	}
	return callback.RunOneShot(ctx, input, execute)
}

func (r *SynchronousRuntime) storageCommandUntilApplied(ctx context.Context, command *api.StorageActivityCommand) (*api.StorageActivityResult, error) {
	return retryStorageCommand(ctx, func(ctx context.Context) (*api.StorageActivityResult, error) {
		return r.executeStorageCommand(ctx, command)
	})
}

// executeStorageCommand applies only commands produced by the callback runner.
// Engine commands are contract errors; the callback store never receives them.
func (r *SynchronousRuntime) executeStorageCommand(ctx context.Context, command *api.StorageActivityCommand) (*api.StorageActivityResult, error) {
	kind, err := selectedStorageCommandKind(command)
	if err != nil {
		return nil, malformedStorageCommand(err)
	}
	var result *api.StorageActivityResult
	switch kind {
	case storageCommandPreparationBegin:
		var decision genrunterminal.PreparationDecision
		decision, err = r.store.BeginRunPreparationOperation(ctx, *command.PreparationBegin)
		result = &api.StorageActivityResult{PreparationBegin: &decision}
	case storageCommandSeedBegin:
		var seed storage.RunSeed
		seed, err = r.store.BeginRunSeed(ctx, *command.SeedBegin)
		result = &api.StorageActivityResult{SeedBegin: &seed}
	case storageCommandSeedAppend:
		var end string
		end, err = r.store.AppendRunSeed(ctx, *command.SeedAppend)
		result = &api.StorageActivityResult{SeedAppend: &api.SeedPositionResult{EndID: end}}
	case storageCommandSeedPublish:
		err = r.store.PublishRunSeed(ctx, *command.SeedPublish)
		result = &api.StorageActivityResult{SeedPublish: &api.SeedPositionResult{EndID: command.SeedPublish.EndID}}
	case storageCommandSynchronousStart:
		result, err = r.start(ctx, command.SynchronousStart)
	case storageCommandAppend:
		result, err = r.appendPromptRecords(ctx, command.Append.Records)
	case storageCommandTerminal:
		result, err = r.complete(ctx, command.Terminal)
	case storageCommandRootStart, storageCommandChildStart, storageCommandOneShotStart, storageCommandOneShotChildStart,
		storageCommandCancellation, storageCommandSuspension, storageCommandUnstartedTerminal:
		return nil, malformedStorageCommand(errors.New("callback runner received an engine-only storage command"))
	}
	if err != nil {
		return nil, classifyStorageActivityError(err)
	}
	if err := validateStorageResult(kind, result); err != nil {
		return nil, malformedStorageCommand(err)
	}
	return result, nil
}

// start stores the exact published synchronous command before callback effects.
// An existing closed start returns its saved result without notifying observers.
func (r *SynchronousRuntime) start(ctx context.Context, input *api.SynchronousRunStartCommand) (*api.StorageActivityResult, error) {
	if input.Started == nil || input.Started.Type != hooks.RunStarted {
		return nil, malformedStorageCommand(errors.New("callback start requires run-started record"))
	}
	event, err := hooks.DecodeFromRecordInput(input.Started)
	if err != nil {
		return nil, malformedStorageCommand(err)
	}
	started, ok := event.(*hooks.RunStartedEvent)
	if !ok || started.SessionID() != "" || started.ParentRunID != "" || started.PredecessorRunID != "" {
		return nil, malformedStorageCommand(errors.New("callback start must be an independent sessionless run"))
	}
	seed, err := r.store.LoadRunSeed(ctx, started.RunID(), input.SeedEndID)
	if err != nil {
		return nil, err
	}
	outcome, err := r.store.StartSynchronousRun(ctx, storage.SynchronousRunStart{
		Run:     session.RunStart{Subject: started.Subject(), RunID: started.RunID(), SeedEndID: input.SeedEndID, StartedAt: time.UnixMilli(started.Timestamp()).UTC(), Labels: started.Labels},
		Started: runLogEvent(input.Started, input.Started.Payload, started.Timestamp()),
	})
	if err != nil {
		return nil, err
	}
	records := []storage.AppendResult{outcome.Record}
	if outcome.Outcome == session.RunStartStop {
		records = append(records, outcome.Cancellation)
	}
	result := &api.StorageActivityResult{SynchronousStart: &api.StartRunResult{Outcome: outcome.Outcome, RunStatus: outcome.RunStatus, CancellationReason: outcome.CancellationReason, Records: records, RenderedPrompts: clonePromptRenderEvents(seed.Declaration.RenderedPrompts)}}
	if err := validateStorageResult(storageCommandSynchronousStart, result); err != nil {
		return nil, malformedStorageCommand(err)
	}
	if !session.IsTerminalRunStatus(outcome.RunStatus) {
		r.publish(ctx, event, outcome.Record)
	}
	return result, nil
}

// appendPromptRecords stores the versions rendered by the callback in order.
// Each retry keeps the original keys and notifies observers only on first insert.
func (r *SynchronousRuntime) appendPromptRecords(ctx context.Context, inputs []*RecordActivityInput) (*api.StorageActivityResult, error) {
	if len(inputs) == 0 {
		return nil, malformedStorageCommand(errors.New("callback prompt command is empty"))
	}
	records := make([]storage.AppendResult, 0, len(inputs))
	for _, input := range inputs {
		if input == nil || input.Type != hooks.PromptRendered {
			return nil, malformedStorageCommand(errors.New("callback append requires a prompt-rendered record"))
		}
		event, err := hooks.DecodeFromRecordInput(input)
		if err != nil {
			return nil, malformedStorageCommand(err)
		}
		record, err := r.store.AppendRunRecord(ctx, runLogEvent(input, input.Payload, event.Timestamp()))
		if err != nil {
			return nil, err
		}
		r.publish(ctx, event, record)
		records = append(records, record)
	}
	return &api.StorageActivityResult{Append: &api.AppendRecordsResult{Records: records}}, nil
}

// complete reads the saved run identity and stores its terminal outcome before
// returning to the callback caller. A lost reply retries the same result.
func (r *SynchronousRuntime) complete(ctx context.Context, input *api.RunTerminalCommand) (*api.StorageActivityResult, error) {
	if input == nil || input.Record == nil {
		return nil, malformedStorageCommand(errors.New("callback completion requires a record"))
	}
	meta, err := r.store.LoadRun(ctx, input.Record.RunID)
	if err != nil {
		return nil, fmt.Errorf("load callback terminal metadata: %w", err)
	}
	command, event, err := runTerminalStorageCommand(input, meta)
	if err != nil {
		return nil, err
	}
	record, err := r.store.RecordRunTerminal(ctx, command)
	if err != nil {
		return nil, err
	}
	r.publish(ctx, event, record)
	return &api.StorageActivityResult{Terminal: &api.RecordWriteResult{Record: record}}, nil
}

func (r *SynchronousRuntime) publish(ctx context.Context, event hooks.Event, result storage.AppendResult) {
	if result.Inserted {
		if err := r.bus.Publish(ctx, event); err != nil {
			trace.SpanFromContext(ctx).RecordError(fmt.Errorf("publish callback observer event: %w", err))
		}
	}
}
