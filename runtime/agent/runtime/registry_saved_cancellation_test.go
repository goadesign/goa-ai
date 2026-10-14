// Saved Registry child cleanup uses retained provider ownership, publishes the
// child's cancellation continuation, and waits until that child has settled.
package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
)

func TestCancelSavedRegistryChildTargetsPrivateGrandchildAndWaits(t *testing.T) {
	for _, target := range []string{"leaf-run", "parent-run"} {
		t.Run(target, func(t *testing.T) {
			rt := New(newTestStore())
			call := testNativeRegistryCall(t, "revision/1")
			call.Subject, call.RunID, call.SessionID = genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("parent.agent"))}, "parent-run", "session-1"
			leaf := suspensionContractFixtureWithContext(t, "private.lookup", "leaf.agent", "leaf-run", nil, nil)
			operation := newAnyJSONSpec("private.delegate")
			operation.IsAgentTool, operation.AgentID = true, "leaf.agent"
			child := nestedChildSuspensionFixture(t, "generic.agent", "child-run", operation, leaf)
			rewriteSuspensionCheckpoint(t, child, func(saved *workflowCheckpoint) {
				saved.Context.ParentRunID = call.RunID
				saved.Context.ParentAgentID = testSubjectAgent(t, call.Subject)
				saved.Context.ParentToolCallID = call.ToolCallID
				saved.Context.Tool = call.Name
				saved.Context.ToolArgs = call.Payload
				saved.Context.ToolRegistry = call.Registry.Clone()
			})
			consumer := testAgentDefinition("parent.agent", "parent.workflow", "parent.queue", nil, nil).WithRegistryTools(testRegistrySources{})
			rt.agents[consumer.route.ID] = AgentRegistration{Definition: consumer}
			admitRunForTest(t, rt.Store, session.RunMeta{Subject: call.Subject, RunID: call.RunID, SessionID: call.SessionID, Status: session.RunStatusRunning})
			// The saved child belongs to this exact parent call. Publish that origin
			// before admission so cancellation restores the same invocation.
			childInput := &RunInput{
				Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("generic.agent")},
				RunID:   "child-run", SessionID: call.SessionID,
				ParentRunID: call.RunID, ParentToolCallID: call.ToolCallID,
			}
			publishTestRunInput(t, rt, childInput, nil)
			admitRunForTest(t, rt.Store, session.RunMeta{
				Subject: childInput.Subject, RunID: childInput.RunID, SessionID: childInput.SessionID,
				ParentRunID: childInput.ParentRunID, SeedEndID: childInput.SeedEndID, Status: session.RunStatusRunning,
			})
			data, err := json.Marshal(child)
			require.NoError(t, err)
			require.NoError(t, storeSuspensionForTest(t.Context(), rt.Store, "child-run", session.RunSuspension{ID: child.ID, Data: data}))
			handles := make(chan *controlledChildHandle, 1)
			wf := &testWorkflowContext{ctx: t.Context(), hookRuntime: rt, controlledChildHandles: handles}
			input := &RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
				RunExecutorBranchAgent("parent.agent"))}, RunID: "cleanup", SessionID: "session-1", TurnID: "turn-2"}
			pending := &checkpointChildContinuation{ToolCallID: call.ToolCallID, Suspension: child}
			loop := &workflowLoop{
				r: rt, wfCtx: wf, input: input, reg: AgentRegistration{Definition: consumer},
				base:              &workflowConversation{RunContext: run.Context{RunID: input.RunID, SessionID: input.SessionID, TurnID: input.TurnID}},
				unfinishedBatch:   &stepBatch{records: []stepToolRecord{{call: call, childSuspension: child}}},
				unfinishedPending: []checkpointPendingInput{{Child: pending}},
			}
			done := make(chan error, 1)
			go func() {
				done <- loop.cancelSavedWork(api.CancellationRequest{RunID: target, Reason: run.CancellationReasonUserRequested})
			}()
			var handle *controlledChildHandle
			select {
			case handle = <-handles:
			case err := <-done:
				t.Fatalf("cleanup returned without restoring the saved provider child: %v", err)
			case <-time.After(2 * time.Second):
				t.Fatal("cleanup did not restore the saved provider child")
			}
			require.Len(t, wf.childRequests, 1)
			request := wf.childRequests[0]
			assert.Equal(t, "generic.workflow", request.Workflow)
			assert.Equal(t, "generic.queue", request.TaskQueue)
			selected := request.Input.Continuation.Cancellation
			require.NotNil(t, selected)
			if target == "leaf-run" {
				assert.Equal(t, target, selected.RunID)
				assert.Equal(t, run.CancellationReasonUserRequested, selected.Reason)
			} else {
				assert.Equal(t, "child-run", selected.RunID)
				assert.Equal(t, run.CancellationReasonEngineCanceled, selected.Reason)
			}
			select {
			case err := <-done:
				t.Fatalf("cleanup returned before its child settled: %v", err)
			default:
			}
			handle.err = context.Canceled
			close(handle.ready)
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(2 * time.Second):
				t.Fatal("cleanup did not finish after its child settled")
			}
		})
	}
}
