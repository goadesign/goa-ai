// Package inmem retains the first root or tool invocation when starting runs.
// Successors copy that value after predecessor validation, so later changes to
// the parent cannot alter the invocation retained by the successor.
package inmem

import (
	"fmt"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/runlog"
)

func rootExecutionOrigin(runID string) genregistry.ExecutionInvocation {
	return genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindRoot(&genregistry.RootInvocation{RunID: runID})}
}

// childExecutionOrigin compares the current parent link with the published
// preparation and retains the original issuing Run. Parent continuations may
// change the current parent without changing the call that created the child.
func childExecutionOrigin(parent *runlog.Event, prepared genregistry.ExecutionInvocation) (genregistry.ExecutionInvocation, error) {
	event, err := hooks.DecodeRunlogEvent(parent)
	if err != nil {
		return genregistry.ExecutionInvocation{}, err
	}
	link, ok := event.(*hooks.ChildRunLinkedEvent)
	if !ok {
		return genregistry.ExecutionInvocation{}, fmt.Errorf("child origin requires a child-link record")
	}
	original, child := prepared.Invocation.AsTool()
	if !child || original.ToolCallID != link.ToolCallID {
		return genregistry.ExecutionInvocation{}, fmt.Errorf("child preparation and parent link name different invocations")
	}
	return cloneExecutionOrigin(prepared), nil
}

// cloneExecutionOrigin keeps returned and successor values independent of the
// stored identity, so changing a caller's value cannot change another run.
func cloneExecutionOrigin(origin genregistry.ExecutionInvocation) genregistry.ExecutionInvocation {
	if root, ok := origin.Invocation.AsRoot(); ok {
		copied := *root
		return genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindRoot(&copied)}
	}
	if tool, ok := origin.Invocation.AsTool(); ok {
		copied := *tool
		return genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindTool(&copied)}
	}
	return origin
}
