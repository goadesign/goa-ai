// Package contract provides the request, input, and result rules that every
// workflow engine must apply. Custom engines use these functions so accepted
// work does not share mutable memory with callers or workflow handlers and the
// same workflow ID always identifies the same root request.
package contract

import (
	"crypto/sha256"
	"fmt"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
)

type (
	// RootRequest contains an owned root workflow request and the exact identity
	// an engine uses to recognize an identical retry.
	RootRequest struct {
		// Request is a validated copy that does not share mutable memory with the
		// caller.
		Request engine.WorkflowStartRequest
		// Digest identifies every value that can change root workflow execution.
		Digest [sha256.Size]byte
	}
)

// NormalizeRootRequest validates and copies a root workflow request. Engines
// retain Request for execution and bind its ID to Digest while the execution
// remains queryable. An exact retry has the same digest; changed input,
// routing, timeouts, retry settings, memo, or search values has a different
// digest.
func NormalizeRootRequest(request engine.WorkflowStartRequest) (RootRequest, error) {
	if _, reserved := request.Memo[startrecipe.MemoKey]; reserved {
		return RootRequest{}, fmt.Errorf("workflow memo key %q is reserved", startrecipe.MemoKey)
	}
	snapshot, err := startrecipe.SnapshotRequest(request)
	if err != nil {
		return RootRequest{}, err
	}
	return RootRequest{Request: snapshot.Request, Digest: snapshot.Digest}, nil
}

// NormalizeChildRequest validates and copies a child workflow request. The
// returned request does not share mutable input memory with the caller.
func NormalizeChildRequest(request engine.ChildWorkflowRequest) (engine.ChildWorkflowRequest, error) {
	snapshot, err := startrecipe.SnapshotChildRequest(request)
	if err != nil {
		return engine.ChildWorkflowRequest{}, err
	}
	return snapshot.Request, nil
}

// CopyRunInput validates and copies a workflow input. Engines retain the
// normalized request privately and call CopyRunInput before every initial or
// retry attempt so one handler attempt cannot change another attempt's input.
func CopyRunInput(input *api.RunInput) (*api.RunInput, error) {
	copied, err := workflowcodec.Copy(workflowcodec.NewDataConverter(), input)
	if err != nil {
		return nil, fmt.Errorf("copy workflow input: %w", err)
	}
	return copied, nil
}

// CopyRunOutput validates and copies a workflow result. Engines retain one
// private copy and call CopyRunOutput again for every wait, query, or other
// caller-facing read so neither handlers nor callers can change saved output.
func CopyRunOutput(output *api.RunOutput) (*api.RunOutput, error) {
	copied, err := workflowcodec.Copy(workflowcodec.NewDataConverter(), output)
	if err != nil {
		return nil, fmt.Errorf("copy workflow output: %w", err)
	}
	return copied, nil
}
