// Package runtime validates direct input continuations using owner-saved
// operation and response contracts. Callers need only the selected worker route;
// the provider checks private tools, policy and labels before execution resumes.
package runtime

import (
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/api"
)

// prepareClientContinuation checks the saved identity and requested response
// before the caller publishes a successor. Private execution checks stay on the
// provider; ordinary message-based callers keep their complete definition checks.
func prepareClientContinuation(input *RunInput, definition AgentDefinition) (*workflowCheckpoint, error) {
	if input == nil || input.Continuation == nil {
		return nil, errors.New("continuation input is required")
	}
	checkpoint, err := decodeClientCheckpoint(input.Continuation.Suspension, definition)
	if err != nil {
		return nil, err
	}
	if err := validateClientContinuation(input, checkpoint, definition); err != nil {
		return nil, err
	}
	if err := validateCheckpointMCPInputs(checkpoint); err != nil {
		return nil, err
	}
	return checkpoint, nil
}

// decodeClientCheckpoint binds a saved direct operation to the client's worker
// route. The saved declaration supplies the route; a response cannot replace it.
func decodeClientCheckpoint(suspension *api.RunSuspension, definition AgentDefinition) (*workflowCheckpoint, error) {
	checkpoint, err := decodeWorkflowCheckpointState(suspension)
	if err != nil {
		return nil, err
	}
	if checkpoint.Context.RegisteredInvocation != nil {
		declaration, err := registeredCheckpointDeclaration(checkpoint)
		if err != nil {
			return nil, err
		}
		route := declaration.ConsumerContract.Agent
		if checkpoint.AgentID != string(definition.route.ID) || route.Workflow != definition.route.WorkflowName || route.Queue != definition.route.DefaultTaskQueue {
			return nil, errors.New("registered suspension differs from selected provider route")
		}
		return decodeProviderCheckpoint(suspension)
	}
	if checkpoint.Context.Operation == nil {
		return decodeWorkflowCheckpoint(suspension, definition)
	}
	operation := checkpoint.Context.Operation
	if checkpoint.Context.ToolRegistry != nil || checkpoint.Context.ParentRunID != "" ||
		checkpoint.Context.ParentAgentID != "" || checkpoint.Context.ParentToolCallID != "" {
		return nil, errors.New("direct suspension cannot include publication or parent identity")
	}
	spec, err := compileAgentOperation(operation, definition.route.ID)
	if err != nil {
		return nil, err
	}
	target := operation.ConsumerContract.Agent
	if checkpoint.AgentID != string(definition.route.ID) || spec.Name != checkpoint.Context.Tool ||
		target.Workflow != definition.route.WorkflowName || target.Queue != definition.route.DefaultTaskQueue {
		return nil, errors.New("direct suspension does not match its retained provider route")
	}
	if _, err := spec.Payload.Codec.FromJSON(checkpoint.Context.ToolArgs); err != nil {
		return nil, fmt.Errorf("direct suspension operation input: %w", err)
	}
	return decodeProviderCheckpoint(suspension)
}

// validateClientContinuation checks the exact pending input or saved descendant
// cancellation without interpreting provider tools. Other runs and malformed
// answers are rejected before the successor is submitted.
func validateClientContinuation(input *RunInput, checkpoint *workflowCheckpoint, definition AgentDefinition) error {
	if checkpoint.Context.Operation == nil && checkpoint.Context.RegisteredInvocation == nil {
		return validateContinuationAgainstCheckpoint(input, checkpoint, definition)
	}
	if err := validateContinuationIdentity(input, checkpoint); err != nil {
		return err
	}
	if err := validateRunContinuationOperation(input.Continuation); err != nil {
		return err
	}
	if request := input.Continuation.Cancellation; request != nil {
		found, err := providerCheckpointContainsRun(checkpoint, request.RunID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("cancellation run %q is not saved in the selected checkpoint", request.RunID)
		}
		return nil
	}
	pending, err := publicPendingInputs(checkpoint.Pending)
	if err != nil {
		return err
	}
	if err := validatePendingInputResponseFor(pending[0], input.Continuation.Response); err != nil {
		return err
	}
	return validateProviderContinuationResponse(checkpoint, input.Continuation.Response)
}
