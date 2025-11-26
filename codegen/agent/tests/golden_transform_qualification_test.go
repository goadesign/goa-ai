package tests

import (
	"path/filepath"
	"strings"
	"testing"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// Ensures internal adapter transforms qualify the top-level tool result initializer
// with the specs package alias while preserving external package qualifiers for nested types.
func TestGolden_InternalTransforms_Qualification(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.MethodExternalAlias())
	// gen/<service>/agents/<agent>/<toolset>/transforms.go
	p := filepath.ToSlash("gen/alpha/agents/scribe/svcset/transforms.go")
	content := fileContent(t, files, p)

	// Top-level transform function should exist for the tool result.
	if !strings.Contains(content, "func InitFetchToolResult(") {
		t.Fatalf("expected InitFetchToolResult helper in %s", p)
	}
	// The initializer must use the specs package alias (svcset).
	if !strings.Contains(content, "&svcset.") {
		t.Fatalf("expected specs-qualified initializer in %s", p)
	}

	// Must not incorrectly qualify the alias with the external package.
	if strings.Contains(content, "types.FetchResult{") {
		t.Fatalf("did not expect external-qualified alias initializer in %s", p)
	}
	// Presence of external package qualifiers for nested fields/helpers.
	if !strings.Contains(content, "types.") {
		t.Fatalf("expected external package references for nested types in %s", p)
	}
}
