package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

func TestGeneratedAgentUsesPlannedNames(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.PlanOwnedNames())

	provider := renderedFileContent(t, files, "gen/runtime/toolsets/lookup/provider.go")
	require.Contains(t, provider, "runtime2.Service")
	require.Contains(t, provider, "mr.ReturnedCount")
	require.Contains(t, provider, "mr.WasTruncated")
	require.Contains(t, provider, "mr.FollowingCursor")
	require.Contains(t, provider, "methodOut.AttachedEvidence")

	inject := renderedFileContent(t, files, "gen/runtime/toolsets/lookup/inject.go")
	require.Contains(t, inject, "p.BoundSession = v")

	executor := renderedFileContent(t, files, "gen/runtime/agents/scribe/lookup/service_executor.go")
	require.Contains(t, executor, "runtime2.Client")
	require.Contains(t, executor, "lookupspecs.InitLookup12MethodPayload")
	require.Contains(t, executor, "lookupspecs.InitLookup12ToolResult")
	require.Contains(t, executor, "mr.ReturnedCount")
	require.Contains(t, executor, "mr.WasTruncated")
	require.Contains(t, executor, "mr.FollowingCursor")
	require.Contains(t, executor, "mr.AttachedEvidence")
}
