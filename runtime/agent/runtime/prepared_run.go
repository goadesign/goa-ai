// Package runtime exposes durable prepared runs for applications that must
// store the exact workflow request before asking the engine to start it.
package runtime

import (
	"context"
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
)

type (
	// PreparedRun is one validated, versioned workflow start request. Callers
	// can store, parse, and start it but cannot change the saved request.
	// Its bytes can contain the complete transcript and continuation checkpoint,
	// so applications must keep them in trusted, access-controlled storage.
	// RunID returns the workflow ID that applications associate with the value.
	PreparedRun struct {
		request startrecipe.PreparedRequest
	}
)

// ParsePreparedRun parses bytes previously returned by MarshalBinary. The
// returned run owns its bytes and can be started by a matching agent client.
func ParsePreparedRun(data []byte) (*PreparedRun, error) {
	request, err := startrecipe.ParsePreparedRequest(data)
	if err != nil {
		return nil, preparedRunContractError(err)
	}
	return &PreparedRun{request: request}, nil
}

// RunID returns the workflow ID assigned to this prepared run. It panics when
// called on a nil or zero-value PreparedRun because only preparation or parsing
// can create a valid value.
func (p *PreparedRun) RunID() string {
	if p == nil || p.request.Request.ID == "" {
		panic("runtime: prepared run is required")
	}
	return p.request.Request.ID
}

// MarshalBinary returns an independent copy of the versioned prepared run.
// Serialization is optional: a failure does not change the prepared run, which
// can still be passed to StartPrepared. A nil or zero-value PreparedRun returns
// ErrPreparedRunRejected.
func (p *PreparedRun) MarshalBinary() ([]byte, error) {
	if p == nil || p.request.Request.ID == "" {
		return nil, preparedRunContractError(errors.New("prepared run is required"))
	}
	data, err := p.request.MarshalBinary()
	if err != nil {
		return nil, preparedRunContractError(err)
	}
	return data, nil
}

// StartPrepared submits the exact engine request held by prepared. It first
// checks the request against this client's generated agent definition.
func (c *agentClient) StartPrepared(ctx context.Context, prepared *PreparedRun) (engine.WorkflowHandle, error) {
	if prepared == nil || prepared.request.AgentID == "" {
		return nil, preparedRunContractError(errors.New("prepared run is required"))
	}
	if prepared.request.AgentID != string(c.definition.route.ID) {
		return nil, preparedRunContractError(fmt.Errorf(
			"prepared run belongs to agent %q, not %q",
			prepared.request.AgentID,
			c.definition.route.ID,
		))
	}

	// Revalidate the stored workflow input and launch settings against the
	// current generated definition before submitting either value.
	input := *prepared.request.Request.Input
	launch := workflowLaunchSettings{
		taskQueue:        prepared.request.TaskQueueOverride,
		memo:             prepared.request.Request.Memo,
		searchAttributes: prepared.request.Request.SearchAttributes,
	}
	expected, err := prepareRunWithDefinition(&input, launch, c.definition, input.SessionID != "")
	if err != nil {
		return nil, preparedRunContractError(err)
	}
	expectedSnapshot, err := startrecipe.SnapshotRequest(expected)
	if err != nil {
		return nil, preparedRunContractError(err)
	}
	if prepared.request.Digest != expectedSnapshot.Digest {
		return nil, preparedRunContractError(errors.New("prepared engine request does not match its run input and agent definition"))
	}
	return c.r.startWorkflow(ctx, expectedSnapshot.Request)
}

// newPreparedRun copies one request after runtime and generated-definition
// validation have completed. It does not create the optional storage bytes.
func newPreparedRun(agentID agent.Ident, request engine.WorkflowStartRequest, taskQueueOverride string) (*PreparedRun, error) {
	prepared, err := startrecipe.NewPreparedRequest(string(agentID), request, taskQueueOverride)
	if err != nil {
		return nil, err
	}
	return &PreparedRun{request: prepared}, nil
}

// preparedRunContractError marks failures while storing, parsing, or validating
// a prepared run before workflow submission.
func preparedRunContractError(err error) error {
	return fmt.Errorf("%w: %w", ErrPreparedRunRejected, err)
}
