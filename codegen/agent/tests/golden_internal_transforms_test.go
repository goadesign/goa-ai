package tests

import (
	"path/filepath"
	"strings"
	"testing"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// Verifies internal adapter transforms are emitted for method-backed tools and
// include the expected helper function names for payload/result mapping.
func TestInternalTransforms_EmitHelpers(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.MethodSimpleCompatible())
	// gen/<service>/toolsets/<toolset>/transforms.go
	p := filepath.ToSlash("gen/svc/toolsets/lookup/transforms.go")
	content := fileContent(t, files, p)
	// Function names follow Init<GoName><Suffix> convention where GoName is Goified tool name.
	if !strings.Contains(content, "adapter transforms") {
		t.Fatalf("expected transforms header in %s", p)
	}
	if !strings.Contains(content, "func Init") {
		t.Fatalf("expected at least one Init* helper in %s", p)
	}
}
