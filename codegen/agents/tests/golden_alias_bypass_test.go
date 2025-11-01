package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agents/tests/testscenarios"
)

// Executors are used; no adapters/clients generated.
func TestGolden_AliasBypass_Both(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.AliasBoth())
	svc := fileContent(t, files, "gen/alpha/agents/scribe/lookup/service_toolset.go")
	require.Contains(t, svc, "func NewScribeLookupToolsetRegistration(exec runtime.ToolCallExecutor)")
	require.Contains(t, svc, "return exec.Execute(ctx, meta, call)")
}

// Payload aliases only: still executor pattern.
func TestGolden_AliasBypass_PayloadOnly(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.AliasPayloadOnly())
	svc := fileContent(t, files, "gen/alpha/agents/scribe/echo/service_toolset.go")
	require.Contains(t, svc, "func NewScribeEchoToolsetRegistration(exec runtime.ToolCallExecutor)")
	require.Contains(t, svc, "return exec.Execute(ctx, meta, call)")
}

// Result aliases only: still executor pattern.
func TestGolden_AliasBypass_ResultOnly(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.AliasResultOnly())
	svc := fileContent(t, files, "gen/alpha/agents/scribe/reply/service_toolset.go")
	require.Contains(t, svc, "func NewScribeReplyToolsetRegistration(exec runtime.ToolCallExecutor)")
	require.Contains(t, svc, "return exec.Execute(ctx, meta, call)")
}
