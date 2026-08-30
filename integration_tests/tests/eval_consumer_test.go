// Package tests exercises goa-ai through generated downstream applications.
package tests

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	goaRoot := goModuleDirectory(t, repoRoot, "goa.design/goa/v3")
	runGoCommand(t, consumerRoot, "mod", "edit", "-replace", "goa.design/goa/v3="+goaRoot)
	runGoCommand(t, consumerRoot, "mod", "tidy")
	runGoCommand(
		t,
		consumerRoot,
		"run",
		"goa.design/goa/v3/cmd/goa",
		"gen",
		"example.com/evalconsumer/design",
	)
	runGoCommand(
		t,
		consumerRoot,
		"run",
		"goa.design/goa/v3/cmd/goa",
		"example",
		"example.com/evalconsumer/design",
	)
	scaffoldPath := filepath.Join(consumerRoot, "cmd", "assistant_quality-evals", "main.go")
	// #nosec G304 -- scaffoldPath is rooted in the test's temporary fixture.
	scaffold, err := os.ReadFile(scaffoldPath)
	require.NoError(t, err)
	require.Equal(t, 2, strings.Count(string(scaffold), "TODO: implement "))
	require.Contains(t, string(scaffold), "RecordSummary")
	require.Contains(t, string(scaffold), "SavedQueryReplay")
	scaffold = append(scaffold, []byte("\n// application-owned marker\n")...)
	// #nosec G703 -- scaffoldPath is rooted in the test's temporary fixture.
	require.NoError(t, os.WriteFile(scaffoldPath, scaffold, 0600))
	runGoCommand(
		t,
		consumerRoot,
		"run",
		"goa.design/goa/v3/cmd/goa",
		"example",
		"example.com/evalconsumer/design",
	)
	// #nosec G304 -- scaffoldPath is rooted in the test's temporary fixture.
	preserved, err := os.ReadFile(scaffoldPath)
	require.NoError(t, err)
	require.Equal(t, scaffold, preserved)
	testPackage := filepath.Join(consumerRoot, "evalconsumer")
	require.NoError(t, os.MkdirAll(testPackage, 0750))
	require.NoError(t, os.Rename(
		filepath.Join(consumerRoot, "eval_test.go.txt"),
		filepath.Join(testPackage, "eval_test.go"),
	))
	runGoCommand(t, consumerRoot, "mod", "tidy")
	runGoCommand(
		t,
		consumerRoot,
		"test",
		"./evalconsumer",
		"./gen/...",
		"./cmd/assistant_quality-evals",
	)
}

// goModuleDirectory returns the module source used to compile this checkout so
// the copied consumer verifies the same Goa and goa-ai code together.
func goModuleDirectory(t *testing.T, directory, modulePath string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), "go", "list", "-m", "-f={{.Dir}}", modulePath) // #nosec G204
	command.Dir = directory
	output, err := command.CombinedOutput()
	require.NoError(t, err, "find module %s: %s", modulePath, output)
	return strings.TrimSpace(string(output))
}

// runGoCommand fails with the complete command output so generation and consumer
// compilation errors remain actionable in CI.
func runGoCommand(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "go", arguments...) // #nosec G204
	command.Dir = directory
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s: %s", fmt.Sprintf("go %v", arguments), output)
}
