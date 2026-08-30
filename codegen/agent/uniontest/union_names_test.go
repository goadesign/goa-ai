// Package uniontest checks that generated public and HTTP packages use the same
// final union names in their definitions and references.
package uniontest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/testhelpers"
	. "goa.design/goa-ai/dsl"
	goacodegen "goa.design/goa/v3/codegen"
	. "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/expr"
)

func TestGeneratedTransportPackageCompilesWithReusedUnion(t *testing.T) {
	files := testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", reusedUnionTools)
	assertGeneratedUnionPackagesCompile(t, files, "gen/alpha/toolsets/analytics")
}

func TestGeneratedCompletionPackagesCompileWithReusedUnion(t *testing.T) {
	files := testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", reusedUnionCompletions)
	assertGeneratedUnionPackagesCompile(t, files, "gen/alpha/completions")
}

func TestGeneratedTransportPackageCompilesWithDistinctUnions(t *testing.T) {
	files := testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", distinctUnionTools)
	contents := renderGeneratedUnionPackages(t, files, "gen/alpha/toolsets/analytics")
	assertDistinctUnionPackage(t, contents["unions.go"])
	assertDistinctUnionPackage(t, contents["http/unions.go"])
}

// reusedUnionTools uses one target_series union from two tools in the same
// public and HTTP packages.
func reusedUnionTools() {
	first := unionInput("FirstInput", "target_series")

	Service("alpha", func() {
		Agent("scribe", "Exercises transport union generation.", func() {
			Use("analytics", func() {
				unionTool("first", first)
				unionTool("first_copy", first)
			})
		})
	})
}

// reusedUnionCompletions repeats the same case for direct completions.
func reusedUnionCompletions() {
	first := unionInput("FirstCompletion", "target_series")

	Service("alpha", func() {
		unionCompletion("first", first)
		unionCompletion("first_copy", first)
	})
}

// distinctUnionTools gives two separate OneOf fields explicit different names
// so both definitions can exist in the same generated package.
func distinctUnionTools() {
	first := unionInput("FirstInput", "target_series")
	second := unionInput("SecondInput", "comparison_series")

	Service("alpha", func() {
		Agent("scribe", "Exercises transport union generation.", func() {
			Use("analytics", func() {
				unionTool("first", first)
				unionTool("second", second)
			})
		})
	})
}

// unionInput creates one input with the supplied OneOf field name.
func unionInput(name, unionName string) expr.UserType {
	return Type(name, func() {
		OneOf(unionName, func() {
			Attribute("explicit", String, "Exact series identifier.")
			Attribute("top1_series_metrics", Int, "Ranked series selector.")
		})
		Required(unionName)
	})
}

// unionTool adds one tool whose JSON input uses input's union.
func unionTool(name string, input expr.UserType) {
	Tool(name, "Uses one target series selector.", func() {
		Args(input)
		Return(String)
	})
}

// unionCompletion adds one direct completion whose result references input's
// union declaration.
func unionCompletion(name string, input expr.UserType) {
	Completion(name, "Returns one target series selector.", func() {
		Return(input)
	})
}

// assertGeneratedUnionPackagesCompile writes one public and HTTP package,
// checks their union names, and compiles both against the local checkouts.
func assertGeneratedUnionPackagesCompile(t *testing.T, files []*goacodegen.File, packagePath string) {
	t.Helper()
	contents := renderGeneratedUnionPackages(t, files, packagePath)
	assertUnionPackage(t, contents["types.go"], contents["unions.go"], "TargetSeries")
	assertUnionPackage(t, contents["http/types.go"], contents["http/unions.go"], "TargetSeries")
}

// renderGeneratedUnionPackages writes one generated tree, compiles the public
// and HTTP packages, and returns their source by relative path.
func renderGeneratedUnionPackages(t *testing.T, files []*goacodegen.File, packagePath string) map[string]string {
	t.Helper()
	root := t.TempDir()
	prefix := filepath.ToSlash(packagePath) + "/"
	contents := make(map[string]string)
	for _, file := range files {
		path := filepath.ToSlash(file.Path)
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		contents[strings.TrimPrefix(path, prefix)] = testhelpers.FileContent(t, files, path)
		_, err := file.Render(root)
		require.NoError(t, err)
	}

	writeLocalModule(t, root)
	runGeneratedGoTest(t, root, "./"+filepath.ToSlash(packagePath), "./"+filepath.ToSlash(packagePath)+"/http")
	return contents
}

// assertUnionPackage checks that both uses of one OneOf field share one type.
func assertUnionPackage(t *testing.T, typesCode, unionsCode, name string) {
	t.Helper()
	require.NotEmpty(t, typesCode)
	require.NotEmpty(t, unionsCode)
	require.True(t, containsUnionReference(typesCode, name), typesCode)
	require.Equal(t, 1, strings.Count(unionsCode, "type "+name+" struct {"))
	require.NotContains(t, typesCode, "TargetSeries3")
	require.NotContains(t, unionsCode, "type TargetSeries3 struct {")
	require.Contains(t, unionsCode, "type "+name+"Kind string")
}

// assertDistinctUnionPackage checks that two explicitly named OneOf fields
// produce two definitions.
func assertDistinctUnionPackage(t *testing.T, unionsCode string) {
	t.Helper()
	require.Contains(t, unionsCode, "type TargetSeries struct {")
	require.Contains(t, unionsCode, "type ComparisonSeries struct {")
}

// containsUnionReference accepts both value fields and pointer fields.
func containsUnionReference(code, name string) bool {
	pattern := `\bTargetSeries\s+\*?` + regexp.QuoteMeta(name) + `\b`
	return regexp.MustCompile(pattern).MatchString(code)
}

// writeLocalModule makes the generated test use the local Goa and goa-ai
// checkouts instead of downloaded versions.
func writeLocalModule(t *testing.T, root string) {
	t.Helper()
	goaAIRoot := moduleDir(t, "goa.design/goa-ai")
	goaRoot := moduleDir(t, "goa.design/goa/v3")
	module := "module generated.local\n\ngo 1.24\n\nrequire (\n\tgoa.design/goa-ai v0.0.0\n\tgoa.design/goa/v3 v3.0.0\n)\n\n" +
		"replace goa.design/goa-ai => " + filepath.ToSlash(goaAIRoot) + "\n" +
		"replace goa.design/goa/v3 => " + filepath.ToSlash(goaRoot) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte(module), 0o600))
}

// moduleDir returns the local directory for one module used by the test.
func moduleDir(t *testing.T, module string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// #nosec G204 -- the module name comes from this test file.
	cmd := exec.CommandContext(ctx, "go", "list", "-m", "-f", "{{.Dir}}", module)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	dir := strings.TrimSpace(string(output))
	require.NotEmpty(t, dir)
	return dir
}

// runGeneratedGoTest compiles the generated packages using the temporary
// module's local replacements.
func runGeneratedGoTest(t *testing.T, root string, packages ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	args := append([]string{"test", "-mod=mod"}, packages...)
	// #nosec G204 -- package names come from this test file.
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
}
