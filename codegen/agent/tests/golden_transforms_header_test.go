package tests

import (
    "path/filepath"
    "testing"

    "goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// TestTransformsHeader_NoDuplicate ensures transforms.go renders with a single
// header/import block and expected function signatures for a simple method-bound tool.
func TestTransformsHeader_NoDuplicate(t *testing.T) {
    files := buildAndGenerate(t, testscenarios.MethodSimpleCompatible())
    p := filepath.ToSlash("gen/svc/agents/scribe/specs/lookup/transforms.go")
    content := fileContent(t, files, p)
    assertGoldenGo(t, "transforms_header", "transforms.go.golden", content)
}
