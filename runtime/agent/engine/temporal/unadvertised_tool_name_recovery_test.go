// This file verifies the Temporal payload shape used to carry an exact rejected
// tool name between planner activities while rejecting the removed correction-only shape.

package temporal

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
)

func TestUnadvertisedToolNameRecoveryPayloadRoundTrip(t *testing.T) {
	converter := NewAgentDataConverter()
	payloads, err := converter.ToPayloads(&api.PlanActivityOutput{
		ModelInvocationRecovery: &api.ModelInvocationRecovery{
			UnadvertisedToolName: "$FUNCTIONS.catalog_list_nearby",
		},
	})
	require.NoError(t, err)
	require.Contains(t, string(payloads.Payloads[0].Data), `"UnadvertisedToolName":"$FUNCTIONS.catalog_list_nearby"`)

	var decoded api.PlanActivityOutput
	require.NoError(t, converter.FromPayloads(payloads, &decoded))
	require.NotNil(t, decoded.ModelInvocationRecovery)
	assert.Equal(
		t,
		"$FUNCTIONS.catalog_list_nearby",
		decoded.ModelInvocationRecovery.UnadvertisedToolName,
	)
	assert.Empty(t, decoded.ModelInvocationRecovery.NoCallBodyCorrection)
}

// Removed correction-only records must be handled by their original worker.
func TestLegacyModelInvocationCorrectionIsRejected(t *testing.T) {
	converter := NewAgentDataConverter()
	payloads, err := converter.ToPayloads(&api.PlanActivityOutput{
		ModelInvocationRecovery: &api.ModelInvocationRecovery{NoCallBodyCorrection: "Use the required field."},
	})
	require.NoError(t, err)
	payloads.Payloads[0].Data = bytes.ReplaceAll(payloads.Payloads[0].Data, []byte(`"ToolInput":null,`), nil)
	payloads.Payloads[0].Data = bytes.ReplaceAll(payloads.Payloads[0].Data, []byte(`"NoCallBodyCorrection"`), []byte(`"Correction"`))
	var decoded api.PlanActivityOutput
	require.ErrorContains(t, converter.FromPayloads(payloads, &decoded), `unknown field "Correction"`)
}
