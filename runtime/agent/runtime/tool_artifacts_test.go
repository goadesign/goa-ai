package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestRuntime_normalizeToolArtifacts_UsesOptionalServerDataCodec(t *testing.T) {
	var encoded bool
	spec := tools.ToolSpec{
		Name:    tools.Ident("svc.tool"),
		Toolset: "svc.ts",
		Payload: tools.TypeSpec{
			Name: "P",
			Codec: tools.JSONCodec[any]{
				ToJSON: json.Marshal,
			},
		},
		Result: tools.TypeSpec{
			Name: "R",
			Codec: tools.JSONCodec[any]{
				ToJSON: json.Marshal,
			},
		},
		ServerData: []*tools.ServerDataSpec{
			{
				Kind:        "svc.artifact",
				Mode:        "optional",
				Description: "Optional server-data",
				Codec: tools.JSONCodec[any]{
					ToJSON: func(v any) ([]byte, error) {
						encoded = true
						return json.Marshal(map[string]any{
							"sidecar_key": v,
						})
					},
					FromJSON: func(data []byte) (any, error) {
						var out any
						if err := json.Unmarshal(data, &out); err != nil {
							return nil, err
						}
						return out, nil
					},
				},
			},
		},
	}
	rt := &Runtime{
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			spec.Name: spec,
		},
		logger: telemetry.NoopLogger{},
	}
	tr := &planner.ToolResult{
		Name:       spec.Name,
		ToolCallID: "call-1",
		Artifacts: []*planner.Artifact{
			{
				Kind:       "svc.artifact",
				SourceTool: spec.Name,
				Data: map[string]any{
					"typed": true,
				},
			},
		},
	}
	require.NoError(t, rt.normalizeToolArtifacts(context.Background(), spec.Name, tr))
	require.True(t, encoded)
	require.IsType(t, json.RawMessage{}, tr.Artifacts[0].Data)

	var got map[string]any
	require.NoError(t, json.Unmarshal(tr.Artifacts[0].Data.(json.RawMessage), &got))
	require.Contains(t, got, "sidecar_key")
}

func TestRuntime_normalizeToolArtifacts_FailsWithoutOptionalServerDataCodec(t *testing.T) {
	spec := newAnyJSONSpec("svc.tool", "svc.ts")
	rt := &Runtime{
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			spec.Name: spec,
		},
		logger: telemetry.NoopLogger{},
	}
	tr := &planner.ToolResult{
		Name:       spec.Name,
		ToolCallID: "call-1",
		Artifacts: []*planner.Artifact{
			{
				Kind:       "svc.artifact",
				SourceTool: spec.Name,
				Data:       map[string]any{"x": 1},
			},
		},
	}
	require.Error(t, rt.normalizeToolArtifacts(context.Background(), spec.Name, tr))
}
