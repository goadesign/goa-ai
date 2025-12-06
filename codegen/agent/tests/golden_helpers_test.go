// Package tests provides golden file tests for agent codegen.
// It uses the shared testhelpers package for common functionality.
package tests

import (
	"path/filepath"
	"testing"

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

// assertGoldenGo compares content as Go source with the golden file path
// relative to tests/testdata/golden/<scenario>/...
func assertGoldenGo(t *testing.T, scenario string, name string, content string) {
	t.Helper()
	p := filepath.Join("testdata", "golden", scenario, name)
	testutil.AssertGo(t, p, content)
}
