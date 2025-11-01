package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agents/tests/testscenarios"
)

// Ensures service toolset generation handles method-backed tools with no
// service result (error only) using the executor-first API.
func TestGolden_NoResultMethod_ServiceToolset(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.NoResultMethod())
	svc := fileContent(t, files, "gen/alpha/agents/scribe/ops/service_toolset.go")
	// Executor constructor and delegation
	require.Contains(t, svc, "func NewScribeOpsToolsetRegistration(exec runtime.ToolCallExecutor)")
	require.Contains(t, svc, "return exec.Execute(ctx, meta, call)")
}
