// Saved service-operation checkpoints must match their owning run even when
// two runs select the same service tool and session.
package runtime

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/tooloperation"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/mcp"
)

func TestLoadServiceSuspensionChecksOriginalInvocation(t *testing.T) {
	for _, sessionID := range []string{"", "session"} {
		t.Run("session="+sessionID, func(t *testing.T) {
			resolution, invocation := registeredRootFixture()
			runtime := New(newTestStore(), WithToolWorkflowQueue("worker"))
			request, err := runtime.prepareRegisteredRootRequest(sessionID, "registry", resolution, invocation, nil, nil)
			require.NoError(t, err)
			input, state, err := restoreRegisteredRoot(request.Input)
			require.NoError(t, err)
			state.queue = "worker"
			state.pending, err = tooloperation.NewPendingInput(&mcp.InputRequired{Requests: map[string]mcp.InputRequest{
				"server-question": {Method: "elicitation/create", Params: json.RawMessage(`{"message":"Continue?","requestedSchema":{"type":"object","properties":{"approved":{"type":"boolean"}},"required":["approved"]}}`)},
			}})
			require.NoError(t, err)
			suspension, err := registeredRootSuspension(input, state)
			require.NoError(t, err)
			data, err := json.Marshal(suspension)
			require.NoError(t, err)
			owner := session.RunMeta{RunID: input.RunID, Subject: input.Subject, SessionID: sessionID, Origin: *invocation.Invocation, Status: session.RunStatusSuspended}
			store := suspensionReadStore{run: owner, suspension: session.RunSuspension{ID: suspension.ID, Data: data}}
			loaded, err := New(store).LoadRunSuspension(t.Context(), owner.RunID)
			require.NoError(t, err)
			assert.Equal(t, suspension, loaded)
			store.run.Origin = genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindRoot(&genregistry.RootInvocation{RunID: "other-root"})}
			_, err = New(store).LoadRunSuspension(t.Context(), owner.RunID)
			require.ErrorIs(t, err, ErrRunSuspensionCorrupt)
			assert.ErrorContains(t, err, "invocation does not match run origin")
		})
	}
}
