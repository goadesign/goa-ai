// Package tests provides golden file tests for agent codegen.
// It uses the shared testhelpers package for common functionality.
package tests

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/testhelpers"
	"goa.design/goa-ai/testutil"
	gcodegen "goa.design/goa/v3/codegen"
)

// buildAndGenerate executes the DSL, runs codegen and returns generated files.
// Delegates to testhelpers.BuildAndGenerate.
func buildAndGenerate(t *testing.T, design func()) []*gcodegen.File {
	t.Helper()
	return testhelpers.BuildAndGenerate(t, design)
}

// buildAndGenerateExample executes the DSL, runs example-phase codegen and returns files.
// Delegates to testhelpers.BuildAndGenerateExample.
func buildAndGenerateExample(t *testing.T, design func()) []*gcodegen.File {
	t.Helper()
	return testhelpers.BuildAndGenerateExample(t, design)
}

// fileContent locates a generated file by path (slash-normalized) and returns the concatenated sections.
// Delegates to testhelpers.FileContent.
func fileContent(t *testing.T, files []*gcodegen.File, wantPath string) string {
	t.Helper()
	return testhelpers.FileContent(t, files, wantPath)
}

// renderedFileContent renders one generated file through Goa's formatting and
// unused-import cleanup before returning the final source.
func renderedFileContent(t *testing.T, files []*gcodegen.File, wantPath string) string {
	t.Helper()
	root := t.TempDir()
	for _, file := range files {
		if filepath.ToSlash(file.Path) != wantPath {
			continue
		}
		path, err := file.Render(root)
		require.NoError(t, err)
		rootDir, err := os.OpenRoot(root)
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, rootDir.Close())
		})
		rel, err := filepath.Rel(root, path)
		require.NoError(t, err)
		rendered, err := rootDir.Open(rel)
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, rendered.Close())
		})
		content, err := io.ReadAll(rendered)
		require.NoError(t, err)
		return string(content)
	}
	t.Fatalf("generated file %q not found", wantPath)
	return ""
}

// fileExists reports whether the generated file list contains wantPath.
func fileExists(files []*gcodegen.File, wantPath string) bool {
	return testhelpers.FileExists(files, wantPath)
}

// assertGoldenGo compares content as Go source with the golden file path
// relative to tests/testdata/golden/<scenario>/...
func assertGoldenGo(t *testing.T, scenario string, name string, content string) {
	t.Helper()
	p := filepath.Join("testdata", "golden", scenario, name)
	testutil.AssertGo(t, p, content)
}
