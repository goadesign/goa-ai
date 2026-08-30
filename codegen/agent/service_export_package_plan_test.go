// This file checks that each service owns the generated names for the toolset
// routes it exports.
package codegen_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	codegen "goa.design/goa-ai/codegen/agent"
	"goa.design/goa-ai/codegen/testhelpers"
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

func TestServiceExportSpecsDoNotDependOnUnrelatedConsumers(t *testing.T) {
	withoutConsumer := serviceExportSpecs(t, false)
	withConsumer := serviceExportSpecs(t, true)

	require.NotEmpty(t, withoutConsumer)
	require.Equal(t, withoutConsumer, withConsumer)
	specs := withoutConsumer["gen/alpha/toolsets/shared/specs.go"]
	provider := withoutConsumer["gen/alpha/toolsets/shared/provider.go"]
	require.Contains(t, specs, `Ping tools.Ident = "shared.ping"`)
	require.Contains(t, specs, `Pong tools.Ident = "shared.pong"`)
	require.Contains(t, provider, ".Ping(")
	require.Contains(t, provider, ".Pong(")
}

func TestServiceExportFilesKeepEachServiceRoute(t *testing.T) {
	genpkg, roots := testhelpers.RunDesign(t, func() {
		API("exports", func() {})
		shared := Toolset("atlas.read", func() {
			Tool("ping", "Ping", func() {})
		})
		Service("alpha", func() {
			Export(shared)
		})
		Service("beta", func() {
			Export(shared)
		})
	})

	files, err := codegen.BuildFilesForTest(genpkg, roots, false)
	require.NoError(t, err)
	alpha := testhelpers.FileContent(t, files, "gen/alpha/toolset_exports.go")
	beta := testhelpers.FileContent(t, files, "gen/beta/toolset_exports.go")
	require.Equal(t, "alpha.atlas.read", generatedConstants(t, alpha)["AtlasReadToolsetName"])
	require.Equal(t, "beta.atlas.read", generatedConstants(t, beta)["AtlasReadToolsetName"])
	require.True(t, testhelpers.FileExists(files, "gen/alpha/toolsets/atlas_read/specs.go"))
	for _, file := range files {
		path := filepath.ToSlash(file.Path)
		require.NotContains(t, path, "/agents/")
		require.NotEqual(t, "gen/AGENTS_QUICKSTART.md", path)
	}
}

func TestServiceExportFilesMoveDerivedNamesAroundServiceDeclarations(t *testing.T) {
	genpkg, roots := testhelpers.RunDesign(t, func() {
		API("exports", func() {})
		collision := Type("SharedToolsetName", String)
		shared := Toolset("shared", func() {
			Tool("ping", "Ping", func() {})
		})
		Service("alpha", func() {
			Method("Read", func() {
				Result(collision)
			})
			Export(shared)
		})
	})

	files, err := codegen.BuildFilesForTest(genpkg, roots, false)
	require.NoError(t, err)
	content := testhelpers.FileContent(t, files, "gen/alpha/toolset_exports.go")
	require.Equal(t, "alpha.shared", generatedConstants(t, content)["SharedToolsetName2"])
}

func TestServiceExportRegistrySpecsWithoutAgent(t *testing.T) {
	genpkg, roots := testhelpers.RunDesign(t, func() {
		API("exports", func() {})
		registry := Registry("catalog", func() {
			URL("https://catalog.example")
		})
		shared := Toolset(FromRegistry(registry, "shared"))
		Service("alpha", func() {
			Export(shared)
		})
		Service("beta", func() {
			Export(shared)
		})
	})

	files, err := codegen.BuildFilesForTest(genpkg, roots, false)
	require.NoError(t, err)
	content := testhelpers.FileContent(t, files, "gen/alpha/toolsets/shared/specs.go")
	require.NotContains(t, content, "RegistryToolsetID")
	require.Contains(t, content, `const RegistryName = "catalog"`)
	require.Contains(t, content, `const ToolsetName = "shared"`)
	require.Equal(t, "alpha.shared", generatedConstants(t, testhelpers.FileContent(t, files, "gen/alpha/toolset_exports.go"))["SharedToolsetName"])
	require.Equal(t, "beta.shared", generatedConstants(t, testhelpers.FileContent(t, files, "gen/beta/toolset_exports.go"))["SharedToolsetName"])
}

// serviceExportSpecs returns every reusable specs file for one exported
// toolset, with an optional consumer that must not affect those files.
func serviceExportSpecs(t *testing.T, withConsumer bool) map[string]string {
	t.Helper()
	genpkg, roots := testhelpers.RunDesign(t, func() {
		API("exports", func() {})
		shared := Toolset("shared", func() {
			Tool("ping", "Ping", func() {
				BindTo("alpha", "Ping")
			})
			Tool("pong", "Pong", func() {
				BindTo("alpha", "Pong")
			})
		})
		Service("alpha", func() {
			Method("Ping", func() {
				Payload(String)
				Result(String)
			})
			Method("Pong", func() {
				Payload(String)
				Result(String)
			})
			Export(shared)
		})
		Service("beta", func() {
			if withConsumer {
				Agent("worker", "Worker", func() {
					Use(shared)
				})
			}
		})
	})
	files, err := codegen.BuildFilesForTest(genpkg, roots, false)
	require.NoError(t, err)
	contents := make(map[string]string)
	for _, file := range files {
		path := filepath.ToSlash(file.Path)
		if strings.HasPrefix(path, "gen/alpha/toolsets/shared/") {
			contents[path] = testhelpers.FileContent(t, files, path)
		}
	}
	return contents
}

// generatedConstants reads string constants from one generated Go file.
func generatedConstants(t *testing.T, source string) map[string]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "generated.go", source, 0)
	require.NoError(t, err)
	constants := make(map[string]string)
	for _, declaration := range file.Decls {
		group, ok := declaration.(*ast.GenDecl)
		if !ok || group.Tok != token.CONST {
			continue
		}
		for _, spec := range group.Specs {
			value := spec.(*ast.ValueSpec)
			for index, name := range value.Names {
				literal, ok := value.Values[index].(*ast.BasicLit)
				if !ok {
					continue
				}
				decoded, err := strconv.Unquote(literal.Value)
				require.NoError(t, err)
				constants[name.Name] = decoded
			}
		}
	}
	return constants
}
