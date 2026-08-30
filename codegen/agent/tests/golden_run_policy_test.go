package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// TestGoldenRunPolicy verifies that authored runtime policy and local tool
// execution support are emitted into worker registration.
func TestGoldenRunPolicy(t *testing.T) {
	design := testscenarios.RunPolicyBasic()
	files := buildAndGenerate(t, design)
	reg := fileContent(t, files, "gen/alpha/agents/scribe/registry.go")
	agent := fileContent(t, files, "gen/alpha/agents/scribe/agent.go")
	require.Contains(t, agent, "specs.Specs(),")
	require.Contains(t, reg, "ResultMaterializer: cfg.resultMaterializers[HelpersToolsetName]")
	require.Contains(t, reg, "func WithHelpersResultMaterializer(materializer agentsruntime.ResultMaterializer)")
	require.NotContains(t, reg, "InterruptsAllowed")
	require.Contains(t, reg, "return nil")
	assertGoldenGo(t, "run_policy", "registry.go.golden", reg)
}

// History compression emitted into registry registration and config.
func TestGolden_RunPolicyHistoryCompression(t *testing.T) {
	design := testscenarios.RunPolicyHistoryCompressTokens()
	files := buildAndGenerate(t, design)
	reg := fileContent(t, files, "gen/alpha/agents/scribe/registry.go")
	cfg := fileContent(t, files, "gen/alpha/agents/scribe/config.go")

	require.Contains(t, reg, "CompressAtMaxInputTokens: 120000")
	require.Contains(t, reg, "KeepMaxInputTokens: 40000")
	require.Contains(t, reg, "KeepMaxTurns: 12")
	require.Contains(t, reg, "if cfg.HistoryCompression != nil")
	require.Contains(t, reg, "agentsruntime.Compress(cfg.HistoryModel, historyCompression)")
	require.Contains(t, cfg, "HistoryCompression *agentsruntime.HistoryCompressionConfig")
	require.Contains(t, cfg, "c.HistoryCompression.Validate()")
}
