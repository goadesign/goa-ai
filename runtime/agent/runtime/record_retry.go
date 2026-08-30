// Package runtime retries exact durable storage commands outside workflow
// code. The caller prepares every record once, so retries cannot change data.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

type runRepairCall func(context.Context) (storage.RunRepairResult, error)

// storageCommandUntilApplied retries temporary failures without rebuilding the
// command. Permanent Store contract failures return immediately.
func (r *Runtime) storageCommandUntilApplied(ctx context.Context, command *api.StorageActivityCommand) error {
	delay := 100 * time.Millisecond
	for {
		if _, err := r.executeStorageCommand(ctx, command); err == nil {
			return nil
		} else if engine.IsActivityErrorNonRetryable(err) {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, 5*time.Second)
	}
}

// repairRunSuspensionUntilApplied retries one repair and publishes its event
// only when the supplied repair record owns the stored suspension.
func (r *Runtime) repairRunSuspensionUntilApplied(ctx context.Context, command storage.RunSuspension, event hooks.Event) error {
	return r.runRepairUntilApplied(ctx, event, session.RunStatusSuspended, func(callCtx context.Context) (storage.RunRepairResult, error) {
		return r.Store.RepairRunSuspension(callCtx, command)
	})
}

// repairRunTerminalUntilApplied retries one repair and publishes its event only
// when the supplied repair record owns the stored terminal result.
func (r *Runtime) repairRunTerminalUntilApplied(ctx context.Context, command storage.RunTerminal, event hooks.Event) error {
	return r.runRepairUntilApplied(ctx, event, command.Status, func(callCtx context.Context) (storage.RunRepairResult, error) {
		return r.Store.RepairRunTerminal(callCtx, command)
	})
}

// runRepairUntilApplied retries temporary store failures. Once the repair is
// stored, local observers run once and keyed stream delivery retries without
// asking the store to repair a terminal run again.
func (r *Runtime) runRepairUntilApplied(ctx context.Context, event hooks.Event, expected session.RunStatus, call runRepairCall) error {
	delay := 100 * time.Millisecond
	for {
		result, err := call(ctx)
		if err == nil {
			if resultErr := validateRunRepairResult(result, expected); resultErr != nil {
				return resultErr
			}
			if result.Outcome == storage.RunRepairDifferentTerminal {
				return nil
			}
			if result.Outcome == storage.RunRepairStored {
				r.publishInsertedHook(ctx, event, result.Record)
			}
			return r.publishRepairStreamUntilApplied(ctx, event, result.Record)
		} else {
			err = classifyStorageActivityError(err)
		}
		if engine.IsActivityErrorNonRetryable(err) {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, 5*time.Second)
	}
}

// publishRepairStreamUntilApplied retries only live stream delivery after a
// repair write succeeds. It never repeats the repair store operation.
func (r *Runtime) publishRepairStreamUntilApplied(ctx context.Context, event hooks.Event, result storage.AppendResult) error {
	delay := 100 * time.Millisecond
	for {
		if err := r.publishStoredHookStream(ctx, event, result); err == nil {
			return nil
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, 5*time.Second)
	}
}

// validateRunRepairResult checks the Store result before the runtime decides
// whether to publish the reconstructed event.
func validateRunRepairResult(result storage.RunRepairResult, expected session.RunStatus) error {
	switch result.Outcome {
	case storage.RunRepairStored:
		if result.Status != expected {
			return fmt.Errorf("runtime: stored repair has status %q, want %q", result.Status, expected)
		}
		if result.Record.ID == "" {
			return errors.New("runtime: stored repair has no record id")
		}
		if !result.Record.Inserted {
			return errors.New("runtime: stored repair did not insert its record")
		}
	case storage.RunRepairAlreadyStored:
		if result.Status != expected {
			return fmt.Errorf("runtime: existing repair has status %q, want %q", result.Status, expected)
		}
		if result.Record.ID == "" {
			return errors.New("runtime: existing repair has no record id")
		}
		if result.Record.Inserted {
			return errors.New("runtime: existing repair unexpectedly inserted its record")
		}
	case storage.RunRepairDifferentTerminal:
		if !session.IsTerminalRunStatus(result.Status) {
			return fmt.Errorf("runtime: repair reports non-terminal existing status %q", result.Status)
		}
		if result.Record != (storage.AppendResult{}) {
			return errors.New("runtime: skipped repair unexpectedly returned a record")
		}
	default:
		return fmt.Errorf("runtime: store returned unknown repair outcome %q", result.Outcome)
	}
	return nil
}
