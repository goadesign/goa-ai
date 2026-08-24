package tests

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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
	ctx := context.Background()

	// Get the quickstart directory path (relative to repo root)
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	quickstartSrcDir := filepath.Join(repoRoot, "quickstart")
	goaRoot, err := selectedGoaModuleDir(ctx, repoRoot)
	if err != nil {
		t.Fatalf("resolve selected Goa module: %v", err)
	}

	// Check required preconditions
	designPath := filepath.Join(quickstartSrcDir, "design", "design.go")
	if _, err := os.Stat(designPath); os.IsNotExist(err) {
		t.Skipf("quickstart design not found at %s, skipping integration test", designPath)
	}
	goModPath := filepath.Join(quickstartSrcDir, "go.mod")
	if _, err := os.Stat(goModPath); os.IsNotExist(err) {
		t.Skipf("quickstart go.mod not found at %s, skipping integration test", goModPath)
	}

	// Copy quickstart into a temp workspace so tests never mutate the repo tree.
	quickstartDir := filepath.Join(t.TempDir(), "quickstart")
	if err := copyDir(quickstartSrcDir, quickstartDir); err != nil {
		t.Fatalf("copy quickstart fixture: %v", err)
	}

	// Point the copied module at the same goa-ai and Goa source used to build
	// this test. The copied module can then run without the caller's workspace.
	if err := rewriteQuickstartModule(ctx, quickstartDir, repoRoot, goaRoot); err != nil {
		t.Fatalf("rewrite quickstart go.mod: %v", err)
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

	// Step 0: Ensure the module graph is tidy before running goa. The goa CLI
	// compiles the design package via `go list`, which fails when the module has
	// pending sum updates.
	t.Run("go_mod_tidy_pre", func(t *testing.T) {
		cmd := isolatedGoCommand(ctx, quickstartDir, "mod", "tidy")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go mod tidy failed: %v\nOutput:\n%s", err, out)
		}
	})

	// Step 1: Run goa gen
	t.Run("goa_gen", func(t *testing.T) {
		cmd := isolatedGoCommand(ctx, quickstartDir,
			"run", "goa.design/goa/v3/cmd/goa", "gen", "example.com/quickstart/design")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("goa gen failed: %v\nOutput:\n%s", err, out)
		}
		t.Logf("goa gen output:\n%s", out)
	})

	// Step 2: Run goa example
	t.Run("goa_example", func(t *testing.T) {
		cmd := isolatedGoCommand(ctx, quickstartDir,
			"run", "goa.design/goa/v3/cmd/goa", "example", "example.com/quickstart/design")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("goa example failed: %v\nOutput:\n%s", err, out)
		}
		t.Logf("goa example output:\n%s", out)
	})

	// Step 2b: Ensure module sums include dependencies pulled in by generated code.
	// This is required when tests run with module updates disabled (e.g. GOFLAGS=-mod=readonly).
	t.Run("go_mod_tidy", func(t *testing.T) {
		cmd := isolatedGoCommand(ctx, quickstartDir, "mod", "tidy")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go mod tidy failed: %v\nOutput:\n%s", err, out)
		}
	})

	// Step 3: Verify compilation
	t.Run("go_build", func(t *testing.T) {
		cmd := isolatedGoCommand(ctx, quickstartDir, "build", "./cmd/...")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go build failed: %v\nOutput:\n%s", err, out)
		}
	})

	// Step 4: Run the example and verify output
	t.Run("run_example", func(t *testing.T) {
		cmd := isolatedGoCommand(ctx, quickstartDir, "run", "./cmd/orchestrator")
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
		if !strings.Contains(output, `Completion draft_task: {"assistant_text":"Created a launch-readiness task draft."`) {
			t.Errorf("expected output to contain 'Completion draft_task:', got:\n%s", output)
		}
		if !strings.Contains(output, `Completion delta draft_task: {"assistant_text":"Creat`) {
			t.Errorf("expected output to contain streamed completion delta preview, got:\n%s", output)
		}
		if !strings.Contains(output, `Completion stream draft_task: {"assistant_text":"Created a launch-readiness task draft."`) {
			t.Errorf("expected output to contain 'Completion stream draft_task:', got:\n%s", output)
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

// selectedGoaModuleDir returns the Goa source directory selected for this test.
// It runs before copied-module commands disable the caller's Go workspace.
func selectedGoaModuleDir(ctx context.Context, repoRoot string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-m", "-f", "{{.Dir}}", "goa.design/goa/v3")
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("go list Goa module: %w: %s", err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// rewriteQuickstartModule points the copied module at the goa-ai and Goa
// source directories selected for this test.
func rewriteQuickstartModule(ctx context.Context, rootPath, repoRoot, goaRoot string) error {
	cmd := isolatedGoCommand(
		ctx,
		rootPath,
		"mod",
		"edit",
		"-replace=goa.design/goa-ai="+repoRoot,
		"-replace=goa.design/goa/v3="+goaRoot,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go mod edit: %w: %s", err, out)
	}
	return nil
}

// isolatedGoCommand runs one command inside the copied module without using
// a Go workspace that does not list the copied directory.
func isolatedGoCommand(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	return cmd
}

// copyDir copies the quickstart fixture into the temp workspace using
// root-scoped file operations so the test cannot escape either tree.
func copyDir(src, dst string) (err error) {
	if err := os.MkdirAll(dst, 0o750); err != nil {
		return err
	}
	srcRoot, err := os.OpenRoot(src)
	if err != nil {
		return fmt.Errorf("open quickstart source root: %w", err)
	}
	defer func() {
		if closeErr := srcRoot.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close quickstart source root: %w", closeErr))
		}
	}()

	dstRoot, err := os.OpenRoot(dst)
	if err != nil {
		return fmt.Errorf("open quickstart destination root: %w", err)
	}
	defer func() {
		if closeErr := dstRoot.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close quickstart destination root: %w", closeErr))
		}
	}()

	return fs.WalkDir(srcRoot.FS(), ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		if d.IsDir() {
			return dstRoot.MkdirAll(path, 0o750)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := srcRoot.ReadFile(path)
		if err != nil {
			return err
		}
		parent := filepath.Dir(path)
		if parent != "." {
			if err := dstRoot.MkdirAll(parent, 0o750); err != nil {
				return err
			}
		}
		if err := dstRoot.WriteFile(path, data, info.Mode().Perm()); err != nil {
			return err
		}
		return nil
	})
}
