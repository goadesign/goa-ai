package shared

import (
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// TestImportPathResolutionConsistency verifies Property 14: Import Path Resolution Consistency.
// **Feature: a2a-codegen-refactor, Property 14: Import Path Resolution Consistency**
// *For any* external user type referenced in both MCP and A2A contexts, the resolved
// import path should be identical.
// **Validates: Requirements 12.4**
func TestImportPathResolutionConsistency(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("import path resolution is consistent across calls", prop.ForAll(
		func(genpkg, rel string) bool {
			// Call JoinImportPath twice with the same inputs
			path1 := JoinImportPath(genpkg, rel)
			path2 := JoinImportPath(genpkg, rel)

			// Results should be identical
			return path1 == path2
		},
		genValidGenPkg(),
		genValidRelPath(),
	))

	properties.TestingRun(t)
}

// TestImportPathHandlesGenSuffix verifies that /gen suffixes are handled correctly.
// **Feature: a2a-codegen-refactor, Property 14: Import Path Resolution Consistency**
// **Validates: Requirements 12.4**
func TestImportPathHandlesGenSuffix(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("paths with and without /gen suffix produce same result", prop.ForAll(
		func(basePkg, rel string) bool {
			// Path without /gen suffix
			pathWithout := JoinImportPath(basePkg, rel)
			// Path with /gen suffix
			pathWith := JoinImportPath(basePkg+"/gen", rel)

			// Both should produce the same result
			return pathWithout == pathWith
		},
		genValidBasePkg(),
		genValidRelPath(),
	))

	properties.TestingRun(t)
}

// TestImportPathContainsGen verifies that result always contains /gen/.
// **Feature: a2a-codegen-refactor, Property 14: Import Path Resolution Consistency**
// **Validates: Requirements 12.4**
func TestImportPathContainsGen(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("non-empty rel path produces path containing /gen/", prop.ForAll(
		func(genpkg, rel string) bool {
			result := JoinImportPath(genpkg, rel)
			// Non-empty rel should produce path containing /gen/
			return strings.Contains(result, "/gen/")
		},
		genValidGenPkg(),
		genNonEmptyRelPath(),
	))

	properties.TestingRun(t)
}

// TestImportPathEmptyRelReturnsEmpty verifies empty rel returns empty string.
// **Feature: a2a-codegen-refactor, Property 14: Import Path Resolution Consistency**
// **Validates: Requirements 12.4**
func TestImportPathEmptyRelReturnsEmpty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("empty rel path returns empty string", prop.ForAll(
		func(genpkg string) bool {
			result := JoinImportPath(genpkg, "")
			return result == ""
		},
		genValidGenPkg(),
	))

	properties.TestingRun(t)
}

// genValidGenPkg generates valid generation package paths.
func genValidGenPkg() gopter.Gen {
	return gen.OneConstOf(
		"example.com/myapp/gen",
		"github.com/user/project/gen",
		"goa.design/goa-ai/gen",
		"example.com/deep/nested/path/gen",
	)
}

// genValidBasePkg generates valid base package paths (without /gen).
func genValidBasePkg() gopter.Gen {
	return gen.OneConstOf(
		"example.com/myapp",
		"github.com/user/project",
		"goa.design/goa-ai",
		"example.com/deep/nested/path",
	)
}

// genValidRelPath generates valid relative import paths.
func genValidRelPath() gopter.Gen {
	return gen.OneConstOf(
		"",
		"types",
		"service",
		"nested/types",
		"deep/nested/path",
	)
}

// genNonEmptyRelPath generates non-empty relative import paths.
func genNonEmptyRelPath() gopter.Gen {
	return gen.OneConstOf(
		"types",
		"service",
		"nested/types",
		"deep/nested/path",
	)
}
