// Package tests exercises goa-ai through generated downstream applications.
package tests

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEvalConsumer generates, compiles, and executes the same eval hook
// contract that a downstream Goa application consumes.
func TestEvalConsumer(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	fixtureRoot := filepath.Join(repoRoot, "integration_tests", "fixtures", "eval_consumer")
	consumerRoot := filepath.Join(t.TempDir(), "eval_consumer")
	require.NoError(t, os.CopyFS(consumerRoot, os.DirFS(fixtureRoot)))

	runGoCommand(t, consumerRoot, "mod", "edit", "-replace", "goa.design/goa-ai="+repoRoot)
	runGoCommand(t, consumerRoot, "mod", "tidy")
	runGoCommand(
		t,
		consumerRoot,
		"run",
		"goa.design/goa/v3/cmd/goa",
		"gen",
		"example.com/evalconsumer/design",
	)
	require.NoError(t, os.Rename(
		filepath.Join(consumerRoot, "eval_test.go.txt"),
		filepath.Join(consumerRoot, "eval_test.go"),
	))
	runGoCommand(t, consumerRoot, "mod", "tidy")
	runGoCommand(t, consumerRoot, "test", "./...")
}

// runGoCommand fails with the complete command output so generation and consumer
// compilation errors remain actionable in CI.
func runGoCommand(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "go", arguments...) // #nosec G204
	command.Dir = directory
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s: %s", fmt.Sprintf("go %v", arguments), output)
}
