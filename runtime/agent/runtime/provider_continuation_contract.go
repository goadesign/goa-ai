// Package runtime retains requested response contracts and exact child
// relationships. Consumers validate those facts without the provider's private
// tools; providers still validate all executable state before restoring work.
package runtime

import (
	"errors"
	"fmt"
	"reflect"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry/schema"
)

// decodeProviderChildCheckpoint checks the owner-written child relationship
// and saved input queue. It does not interpret the provider's private tools.
func decodeProviderChildCheckpoint(call ToolCall, suspension *api.RunSuspension) (*workflowCheckpoint, error) {
	checkpoint, err := decodeProviderCheckpoint(suspension)
	if err != nil {
		return nil, err
	}
	if err := validateCheckpointChildIdentity(call, checkpoint); err != nil {
		return nil, err
	}
	return checkpoint, nil
}

// decodeProviderCheckpoint checks saved input and child relationships without
// loading private tools. The provider checks executable state before resuming.
func decodeProviderCheckpoint(suspension *api.RunSuspension) (*workflowCheckpoint, error) {
	checkpoint, err := decodeWorkflowCheckpointState(suspension)
	if err != nil {
		return nil, err
	}
	if checkpoint.Context.RegisteredInvocation != nil {
		if _, err := registeredCheckpointDeclaration(checkpoint); err != nil {
			return nil, err
		}
	}
	if !reflect.DeepEqual(requiredCheckpointToolNames(checkpoint), checkpoint.RequiredTools) {
		return nil, errors.New("provider suspension required tools do not match saved state")
	}
	if err := validateCheckpointChildRecords(checkpoint, validateProviderChildCheckpoint); err != nil {
		return nil, err
	}
	return checkpoint, nil
}

// validateProviderChildCheckpoint follows one saved child call and rejects a
// changed identity or pending response anywhere below that call.
func validateProviderChildCheckpoint(call ToolCall, suspension *api.RunSuspension) error {
	_, err := decodeProviderChildCheckpoint(call, suspension)
	return err
}

// providerCheckpointContainsRun searches only relationships already validated
// by decodeProviderChildCheckpoint. Unrelated runs cannot be canceled here.
func providerCheckpointContainsRun(checkpoint *workflowCheckpoint, runID string) (bool, error) {
	if checkpoint.PreviousRunID == runID {
		return true, nil
	}
	for _, record := range checkpoint.Batch.Records {
		if record.ChildSuspension == nil {
			continue
		}
		child, err := decodeWorkflowCheckpointState(record.ChildSuspension)
		if err != nil {
			return false, err
		}
		found, err := providerCheckpointContainsRun(child, runID)
		if err != nil || found {
			return found, err
		}
	}
	return false, nil
}

// validateProviderContinuationResponse follows the first saved pending input
// and checks externally supplied results against that input's retained schemas.
func validateProviderContinuationResponse(checkpoint *workflowCheckpoint, response *api.PendingInputResponse) error {
	pending := checkpoint.Pending[0]
	if pending.Child != nil {
		child, err := decodeWorkflowCheckpointState(pending.Child.Suspension)
		if err != nil {
			return err
		}
		return validateProviderContinuationResponse(child, response)
	}
	if pending.Await == nil || response.ToolResults == nil {
		return nil
	}
	calls := awaitToolRequests([]planner.AwaitItem{*pending.Await})
	contracts := pending.ResponseContracts
	if len(contracts) == 0 {
		return fmt.Errorf("%w: provider suspension is missing the requested result contracts", ErrRunSuspensionCorrupt)
	}
	specs := make(map[tools.Ident]tools.ToolSpec, len(contracts))
	for _, call := range calls {
		contract, ok := contracts[call.Name]
		if !ok {
			return fmt.Errorf("%w: provider suspension is missing result contract for %q", ErrRunSuspensionCorrupt, call.Name)
		}
		if _, compiled := specs[call.Name]; compiled {
			continue
		}
		spec := tools.ToolSpec{Name: call.Name, Bounds: contract.Bounds}
		if len(contract.ResultSchema) != 0 {
			codec, err := schema.Codec(contract.ResultSchema)
			if err != nil {
				return fmt.Errorf("%w: provider pending result %q schema: %w", ErrRunSuspensionCorrupt, call.Name, err)
			}
			spec.Result = tools.TypeSpec{Schema: contract.ResultSchema, Codec: codec}
		}
		specs[call.Name] = spec
	}
	return validateProvidedToolResults(*pending.Await, response.ToolResults, func(name tools.Ident) (tools.ToolSpec, bool) {
		spec, ok := specs[name]
		return spec, ok
	})
}
