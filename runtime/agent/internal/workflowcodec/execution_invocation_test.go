// These tests preserve the original execution identity before provider work and
// reject damaged saved identities instead of assigning a new owner on replay.
package workflowcodec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/storage"
)

func TestPreparationInvocationRoundTrip(t *testing.T) {
	for _, origin := range []genregistry.ExecutionInvocation{
		{Invocation: genregistry.NewExecutionInvocationKindRoot(&genregistry.RootInvocation{RunID: "original-root"})},
		{Invocation: genregistry.NewExecutionInvocationKindTool(&genregistry.ToolInvocation{RunID: "original-parent", ToolCallID: "original-call"})},
	} {
		t.Run(string(origin.Invocation.Kind()), func(t *testing.T) {
			command := api.StorageActivityCommand{PreparationBegin: &storage.RunPreparationBegin{
				Operation: storage.PreparationOperation{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("executor")}, RunID: "current-run", SessionID: "session", CommandID: "accepted-command"},
				Origin:    origin,
			}}
			codec := NewDataConverter()
			encoded, err := codec.ToPayload(command)
			require.NoError(t, err)
			var decoded api.StorageActivityCommand
			require.NoError(t, codec.FromPayload(encoded, &decoded))
			assert.Equal(t, command, decoded)
			var bound Budget
			require.NoError(t, bound.AddEncodedSource(command))
			assert.GreaterOrEqual(t, bound.used, len(encoded.Data))
		})
	}
}

func TestPreparationInvocationRejectsMissingAndMalformedSavedOwner(t *testing.T) {
	for _, origin := range []string{
		`null`, `{}`, `{"invocation":null}`,
		`{"invocation":{"type":"root","value":{"run_id":""}}}`,
		`{"invocation":{"type":"tool","value":{"run_id":"parent","tool_call_id":""}}}`,
		`{"invocation":{"type":"root","value":{"run_id":"root","unknown":1}}}`,
		`{"invocation":{"type":"root","value":{"run_id":"root"}},"unknown":1}`,
		`{"invocation":{"type":"root","value":{"run_id":"root"}},"invocation":{"type":"root","value":{"run_id":"other"}}}`,
	} {
		var saved struct {
			Origin genregistry.ExecutionInvocation
		}
		require.Error(t, DecodeJSON([]byte(`{"Origin":`+origin+`}`), &saved), origin)
	}
	var missing struct {
		Origin genregistry.ExecutionInvocation
	}
	require.Error(t, DecodeJSON([]byte(`{}`), &missing))
	_, err := NewDataConverter().ToPayload(genregistry.ExecutionInvocation{})
	require.Error(t, err)
}
