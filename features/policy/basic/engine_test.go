package basic_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/features/policy/basic"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestEngineFiltersByTags(t *testing.T) {
	engine, err := basic.New(basic.Options{AllowTags: []string{"trusted"}, BlockTags: []string{"deprecated"}})
	require.NoError(t, err)
	decision, err := engine.Decide(context.Background(), policy.Input{
		Tools: []policy.ToolMetadata{
			{ID: "svc.alpha.tool", Tags: []string{"trusted"}},
			{ID: "svc.beta.tool", Tags: []string{"deprecated"}},
		},
	})
	require.NoError(t, err)
	require.Equal(t, []tools.Ident{tools.Ident("svc.alpha.tool")}, decision.AllowedTools)
}

func TestEngineBlocksExplicitTools(t *testing.T) {
	engine, err := basic.New(basic.Options{BlockTools: []string{"svc.beta.tool"}})
	require.NoError(t, err)
	decision, err := engine.Decide(context.Background(), policy.Input{
		Tools: []policy.ToolMetadata{
			{ID: "svc.alpha.tool"},
			{ID: "svc.beta.tool"},
		},
		Requested: []tools.Ident{tools.Ident("svc.alpha.tool"), tools.Ident("svc.beta.tool")},
	})
	require.NoError(t, err)
	require.Equal(t, []tools.Ident{tools.Ident("svc.alpha.tool")}, decision.AllowedTools)
}

func TestEngineRestrictsViaRetryHint(t *testing.T) {
	engine, err := basic.New(basic.Options{})
	require.NoError(t, err)
	decision, err := engine.Decide(context.Background(), policy.Input{
		Tools:         []policy.ToolMetadata{{ID: "svc.alpha.tool"}, {ID: "svc.beta.tool"}},
		RetryHint:     &policy.RetryHint{Tool: "svc.beta.tool", RestrictToTool: true},
		RemainingCaps: policy.CapsState{MaxToolCalls: 5, RemainingToolCalls: 5},
	})
	require.NoError(t, err)
	require.Equal(t, []tools.Ident{tools.Ident("svc.beta.tool")}, decision.AllowedTools)
	require.Equal(t, 1, decision.Caps.RemainingToolCalls)
}

func TestEngineRemovesUnavailableTool(t *testing.T) {
	engine, err := basic.New(basic.Options{AllowTools: []string{"svc.alpha.tool", "svc.beta.tool"}})
	require.NoError(t, err)
	decision, err := engine.Decide(context.Background(), policy.Input{
		Tools:     []policy.ToolMetadata{{ID: "svc.alpha.tool"}, {ID: "svc.beta.tool"}},
		RetryHint: &policy.RetryHint{Tool: "svc.beta.tool", Reason: policy.RetryReasonToolUnavailable},
	})
	require.NoError(t, err)
	require.Equal(t, []tools.Ident{tools.Ident("svc.alpha.tool")}, decision.AllowedTools)
}

func TestEngineEmitsMetadata(t *testing.T) {
	engine, err := basic.New(basic.Options{Label: "custom"})
	require.NoError(t, err)
	decision, err := engine.Decide(context.Background(), policy.Input{
		Tools: []policy.ToolMetadata{{ID: "svc.alpha.tool"}},
	})
	require.NoError(t, err)
	require.Equal(t, "custom", decision.Metadata["engine"])
	require.Equal(t, "custom", decision.Labels["policy_engine"])
}
