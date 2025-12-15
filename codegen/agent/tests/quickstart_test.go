package tests

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestQuickstartGeneratesAndRuns verifies that the quickstart example:
// 1. Successfully generates code with `goa gen`
// 2. Successfully generates example with `goa example`
// 3. Compiles without errors
// 4. Runs and produces expected output
//
// This test ensures the quickstart doesn't break as the codebase evolves.
func TestQuickstartGeneratesAndRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping quickstart integration test in short mode")
	}

	// Get the quickstart directory path (relative to repo root)
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	quickstartDir := filepath.Join(repoRoot, "quickstart")

	// Check required preconditions
	designPath := filepath.Join(quickstartDir, "design", "design.go")
	if _, err := os.Stat(designPath); os.IsNotExist(err) {
		t.Skipf("quickstart design not found at %s, skipping integration test", designPath)
	}
	goModPath := filepath.Join(quickstartDir, "go.mod")
	if _, err := os.Stat(goModPath); os.IsNotExist(err) {
		t.Skipf("quickstart go.mod not found at %s, skipping integration test", goModPath)
	}

	// Ensure we have a clean state (remove generated files that aren't committed)
	// Note: We don't remove the design/ directory which should be committed
	for _, dir := range []string{"gen", "cmd", "internal"} {
		path := filepath.Join(quickstartDir, dir)
		if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
			t.Logf("warning: could not clean %s: %v", dir, err)
		}
	}

	// Remove any user-created files that depend on generated code to allow clean bootstrap
	for _, file := range []string{"orchestrator.go"} {
		path := filepath.Join(quickstartDir, file)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Logf("warning: could not remove %s: %v", file, err)
		}
	}

	ctx := context.Background()

	// Step 1: Run goa gen
	t.Run("goa_gen", func(t *testing.T) {
		cmd := exec.CommandContext(ctx, "goa", "gen", "example.com/quickstart/design")
		cmd.Dir = quickstartDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("goa gen failed: %v\nOutput:\n%s", err, out)
		}
		t.Logf("goa gen output:\n%s", out)
	})

	// Step 2: Run goa example
	t.Run("goa_example", func(t *testing.T) {
		cmd := exec.CommandContext(ctx, "goa", "example", "example.com/quickstart/design")
		cmd.Dir = quickstartDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("goa example failed: %v\nOutput:\n%s", err, out)
		}
		t.Logf("goa example output:\n%s", out)
	})

	// Step 3: Verify compilation
	t.Run("go_build", func(t *testing.T) {
		cmd := exec.CommandContext(ctx, "go", "build", "./cmd/...")
		cmd.Dir = quickstartDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go build failed: %v\nOutput:\n%s", err, out)
		}
	})

	// Step 4: Run the example and verify output
	t.Run("run_example", func(t *testing.T) {
		cmd := exec.CommandContext(ctx, "go", "run", "./cmd/orchestrator")
		cmd.Dir = quickstartDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go run failed: %v\nOutput:\n%s", err, out)
		}

		// Verify expected output
		output := string(out)
		if !strings.Contains(output, "RunID:") {
			t.Errorf("expected output to contain 'RunID:', got:\n%s", output)
		}
		if !strings.Contains(output, "Assistant:") {
			t.Errorf("expected output to contain 'Assistant:', got:\n%s", output)
		}
		t.Logf("Example output:\n%s", output)
	})
}

// TestQuickstartDesignExists verifies the design file is present and parseable.
func TestQuickstartDesignExists(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	designPath := filepath.Join(repoRoot, "quickstart", "design", "design.go")
	if _, err := os.Stat(designPath); os.IsNotExist(err) {
		t.Fatalf("design file not found at %s", designPath)
	}
}
