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

// repairRunSuspensionUntilApplied retries one repair until storage identifies
// which terminal record owns the run.
func (r *Runtime) repairRunSuspensionUntilApplied(ctx context.Context, command storage.RunSuspension) (storage.RunRepairResult, error) {
	return r.runRepairUntilApplied(ctx, session.RunStatusSuspended, func(callCtx context.Context) (storage.RunRepairResult, error) {
		return r.Store.RepairRunSuspension(callCtx, command)
	})
}

// repairRunTerminalUntilApplied retries one repair until storage identifies
// which terminal record owns the run.
func (r *Runtime) repairRunTerminalUntilApplied(ctx context.Context, command storage.RunTerminal) (storage.RunRepairResult, error) {
	return r.runRepairUntilApplied(ctx, command.Status, func(callCtx context.Context) (storage.RunRepairResult, error) {
		return r.Store.RepairRunTerminal(callCtx, command)
	})
}

// runRepairUntilApplied retries temporary store failures without publishing a
// record before the caller knows which terminal result won.
func (r *Runtime) runRepairUntilApplied(ctx context.Context, expected session.RunStatus, call runRepairCall) (storage.RunRepairResult, error) {
	delay := 100 * time.Millisecond
	for {
		result, err := call(ctx)
		if err == nil {
			if resultErr := validateRunRepairResult(result, expected); resultErr != nil {
				return storage.RunRepairResult{}, resultErr
			}
			return result, nil
		} else {
			err = classifyStorageActivityError(err)
		}
		if engine.IsActivityErrorNonRetryable(err) {
			return storage.RunRepairResult{}, err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return storage.RunRepairResult{}, ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, 5*time.Second)
	}
}

// publishStoredHookStreamUntilApplied retries delivery of one stored event. It
// never repeats the store operation that returned the record identifier. The
// Session status returned with that write decides whether delivery is required:
// an event accepted while active remains due even if the Session later ends.
func (r *Runtime) publishStoredHookStreamUntilApplied(ctx context.Context, event hooks.Event, result storage.AppendResult) error {
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
