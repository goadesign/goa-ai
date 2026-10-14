// These checks reject missing and competing preparation outcomes at the
// workflow transport, while preserving the owner's exact stop reason.
package workflowcodec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/api"
)

func TestPreparationResultRequiresOneOutcome(t *testing.T) {
	converter := NewDataConverter()
	for _, test := range []struct {
		name  string
		value any
	}{
		{"missing", &api.AgentOperationActivityResult{}},
		{"nil", (*api.AgentOperationActivityResult)(nil)},
		{"competing", &api.AgentOperationActivityResult{Prepared: &api.RunInput{}, Stopped: &api.CancellationRequest{RunID: "root", Reason: "operator shutdown"}}},
		{"missing reason", &api.AgentOperationActivityResult{Stopped: &api.CancellationRequest{RunID: "root"}}},
		{"child competing", &api.AgentChildActivityOutput{Success: &api.AgentChildActivitySuccess{}, Stopped: &api.CancellationRequest{RunID: "child", Reason: "operator shutdown"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := converter.ToPayload(test.value)
			assert.Error(t, err)
		})
	}
	original := &api.AgentOperationActivityResult{Stopped: &api.CancellationRequest{RunID: "root", Reason: "operator shutdown"}}
	payload, err := converter.ToPayload(original)
	require.NoError(t, err)
	var restored api.AgentOperationActivityResult
	require.NoError(t, converter.FromPayload(payload, &restored))
	assert.Equal(t, original, &restored)
	assert.Error(t, DecodeJSON([]byte(`{"Prepared":null,"Stopped":null}`), &restored))
}
