package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// RunPolicy emitted into registry registration.
func TestGolden_RunPolicy(t *testing.T) {
	design := testscenarios.RunPolicyBasic()
	files := buildAndGenerate(t, design)
	reg := fileContent(t, files, "gen/alpha/agents/scribe/registry.go")
	require.Contains(t, reg, "Specs: specs.Specs")
	require.Contains(t, reg, "InterruptsAllowed")
	require.Contains(t, reg, "return nil")
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
