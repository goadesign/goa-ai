package temporal

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/converter"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/planner"
	aitools "goa.design/goa-ai/runtime/agent/tools"
)

func TestNewAgentDataConverter_RehydratesRunOutputToolEvents(t *testing.T) {
	type result struct {
		Value string `json:"value"`
	}

	toolName := aitools.Ident("test.tool")
	specFn := func(id aitools.Ident) (*aitools.ToolSpec, bool) {
		if id != toolName {
			return nil, false
		}
		return &aitools.ToolSpec{
			Name: toolName,
			Result: aitools.TypeSpec{
				Codec: aitools.JSONCodec[any]{
					FromJSON: func(data []byte) (any, error) {
						var r result
						if err := json.Unmarshal(data, &r); err != nil {
							return nil, err
						}
						return r, nil
					},
				},
			},
		}, true
	}

	base := converter.NewJSONPayloadConverter()
	payload, err := base.ToPayload(api.RunOutput{
		AgentID: agent.Ident("agent.test"),
		RunID:   "run-1",
		ToolEvents: []*planner.ToolResult{
			{
				Name:   toolName,
				Result: result{Value: "ok"},
			},
		},
	})
	require.NoError(t, err)

	dc := NewAgentDataConverter(specFn)
	var decoded *api.RunOutput
	require.NoError(t, dc.FromPayload(payload, &decoded))
	require.NotNil(t, decoded)
	require.Len(t, decoded.ToolEvents, 1)

	got := decoded.ToolEvents[0].Result
	r, ok := got.(result)
	require.True(t, ok, "expected decoded tool result to be concrete type, got %T", got)
	assert.Equal(t, "ok", r.Value)
}
