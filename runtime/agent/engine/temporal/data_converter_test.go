package temporal

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestNewAgentDataConverter_RoundTripsToolResult(t *testing.T) {
	type testResult struct {
		Value string `json:"value"`
	}

	toolName := tools.Ident("test.tool")
	specFn := func(id tools.Ident) (*tools.ToolSpec, bool) {
		if id != toolName {
			return nil, false
		}
		return &tools.ToolSpec{
			Name: toolName,
			Result: tools.TypeSpec{
				Codec: tools.JSONCodec[any]{
					ToJSON: func(v any) ([]byte, error) {
						if typed, ok := v.(*testResult); ok {
							return json.Marshal(typed)
						}
						return nil, assert.AnError
					},
					FromJSON: func(data []byte) (any, error) {
						var out testResult
						if err := json.Unmarshal(data, &out); err != nil {
							return nil, err
						}
						return &out, nil
					},
				},
			},
		}, true
	}

	dc := NewAgentDataConverter(specFn)
	p, err := dc.ToPayload(&planner.ToolResult{
		Name:   toolName,
		Result: &testResult{Value: "ok"},
	})
	require.NoError(t, err)

	var decoded *planner.ToolResult
	require.NoError(t, dc.FromPayload(p, &decoded))
	require.NotNil(t, decoded)

	out, ok := decoded.Result.(*testResult)
	require.True(t, ok, "expected decoded tool result to be *testResult, got %T", decoded.Result)
	assert.Equal(t, "ok", out.Value)
}

func TestNewAgentDataConverter_RejectsJSONStringifiedToolResult(t *testing.T) {
	type testResult struct {
		Value string `json:"value"`
	}

	toolName := tools.Ident("test.tool")
	specFn := func(id tools.Ident) (*tools.ToolSpec, bool) {
		if id != toolName {
			return nil, false
		}
		return &tools.ToolSpec{
			Name: toolName,
			Result: tools.TypeSpec{
				Codec: tools.JSONCodec[any]{
					ToJSON: func(v any) ([]byte, error) {
						if typed, ok := v.(*testResult); ok {
							return json.Marshal(typed)
						}
						return nil, assert.AnError
					},
					FromJSON: func(data []byte) (any, error) {
						var out testResult
						if err := json.Unmarshal(data, &out); err != nil {
							return nil, err
						}
						return &out, nil
					},
				},
			},
		}, true
	}

	dc := NewAgentDataConverter(specFn)
	_, err := dc.ToPayload(&planner.ToolResult{
		Name:   toolName,
		Result: `{"value":"ok"}`,
	})
	require.Error(t, err)
}
