// This file verifies the Temporal payload shape used to carry an exact rejected
// tool name between planner activities while retaining old correction payloads.

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
	assert.Empty(t, decoded.ModelInvocationRecovery.Correction)
}

func TestOldModelInvocationCorrectionPayloadWithoutNameStillDecodes(t *testing.T) {
	converter := NewAgentDataConverter()
	payloads, err := converter.ToPayloads(&api.PlanActivityOutput{
		ModelInvocationRecovery: &api.ModelInvocationRecovery{
			Correction: "Use the required field.",
		},
	})
	require.NoError(t, err)
	require.Len(t, payloads.Payloads, 1)
	require.Contains(t, string(payloads.Payloads[0].Data), `"UnadvertisedToolName":""`)
	payloads.Payloads[0].Data = bytes.Replace(
		payloads.Payloads[0].Data,
		[]byte(`,"UnadvertisedToolName":""`),
		nil,
		1,
	)
	assert.False(
		t,
		bytes.Contains(payloads.Payloads[0].Data, []byte("UnadvertisedToolName")),
	)

	var decoded api.PlanActivityOutput
	require.NoError(t, converter.FromPayloads(payloads, &decoded))
	require.NotNil(t, decoded.ModelInvocationRecovery)
	assert.Equal(t, "Use the required field.", decoded.ModelInvocationRecovery.Correction)
	assert.Empty(t, decoded.ModelInvocationRecovery.UnadvertisedToolName)
}
