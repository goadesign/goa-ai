package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// Completion-only services should traverse both the main and example generator
// paths without emitting agent-only scaffolding.
func TestCompletionOnlyServiceGeneration(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ServiceCompletion())
	require.True(t, fileExists(files, "gen/tasks/completions/specs.go"))
	require.False(t, fileExists(files, "AGENTS_QUICKSTART.md"))

	exampleFiles := buildAndGenerateExample(t, testscenarios.ServiceCompletion())
	require.False(t, fileExists(exampleFiles, "cmd/tasks/main.go"))
	require.False(t, fileExists(exampleFiles, "internal/agents/bootstrap/bootstrap.go"))
}
