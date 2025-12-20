package codegen_test

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	codegen "goa.design/goa-ai/codegen/agent"
	. "goa.design/goa-ai/dsl"
	agentsExpr "goa.design/goa-ai/expr/agent"
	goadsl "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// TestRegistryToolsetSpecsStructure verifies that registry-backed toolsets
// generate specs files with the same structure as local toolsets.
// **Feature: mcp-registry, Property 11: Provider-Agnostic Specs Generation**
// **Validates: Requirements 11.1**
func TestRegistryToolsetSpecsStructure(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("registry_test", func() {})

		corpRegistry := Registry("corp-registry", func() {
			goadsl.URL("https://registry.corp.internal")
			APIVersion("v1")
			SyncInterval("5m")
			CacheTTL("1h")
		})

		registryTools := Toolset(FromRegistry(corpRegistry, "data-tools"))

		goadsl.Service("registry_test", func() {
			Agent("data-agent", "Data processing agent", func() {
				Use(registryTools)
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := codegen.Generate("example.com/registry", []eval.Root{goaexpr.Root, agentsExpr.Root}, nil)
	require.NoError(t, err)

	var specsContent string
	expectedPath := filepath.ToSlash("gen/registry_test/toolsets/data_tools/specs.go")
	for _, f := range files {
		if filepath.ToSlash(f.Path) == expectedPath {
			var buf bytes.Buffer
			for _, s := range f.SectionTemplates {
				require.NoError(t, s.Write(&buf))
			}
			specsContent = buf.String()
			break
		}
	}
	require.NotEmpty(t, specsContent, "expected generated specs.go at %s", expectedPath)

	require.Contains(t, specsContent, "var Specs []tools.ToolSpec")
	require.Contains(t, specsContent, "func Names() []tools.Ident")
	require.Contains(t, specsContent, "func Spec(name tools.Ident) (*tools.ToolSpec, bool)")
	require.Contains(t, specsContent, "func PayloadSchema(name tools.Ident) ([]byte, bool)")
	require.Contains(t, specsContent, "func ResultSchema(name tools.Ident) ([]byte, bool)")
	require.Contains(t, specsContent, "func Metadata() []policy.ToolMetadata")
	require.Contains(t, specsContent, "RegistryToolsetID")
	require.Contains(t, specsContent, "RegistryName")
	require.Contains(t, specsContent, "ToolsetName")
	require.Contains(t, specsContent, "func DiscoverAndPopulate")
	require.Contains(t, specsContent, "type RegistryClient interface")
	require.Contains(t, specsContent, "func ValidatePayload")
	require.Contains(t, specsContent, "func ValidateResult")
}

// TestRegistryToolsetSpecsMetadata verifies registry metadata is embedded.
func TestRegistryToolsetSpecsMetadata(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("meta_test", func() {})

		testRegistry := Registry("test-registry", func() {
			goadsl.URL("https://test.registry.io")
			APIVersion("v2")
		})

		pinnedTools := Toolset("pinned-tools", FromRegistry(testRegistry, "enterprise-tools"), func() {
			goadsl.Version("1.2.3")
		})

		goadsl.Service("meta_test", func() {
			Agent("meta-agent", "Metadata test agent", func() {
				Use(pinnedTools)
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := codegen.Generate("example.com/meta", []eval.Root{goaexpr.Root, agentsExpr.Root}, nil)
	require.NoError(t, err)

	var specsContent string
	expectedPath := filepath.ToSlash("gen/meta_test/toolsets/pinned_tools/specs.go")
	for _, f := range files {
		if filepath.ToSlash(f.Path) == expectedPath {
			var buf bytes.Buffer
			for _, s := range f.SectionTemplates {
				require.NoError(t, s.Write(&buf))
			}
			specsContent = buf.String()
			break
		}
	}
	require.NotEmpty(t, specsContent, "expected generated specs.go at %s", expectedPath)

	require.Contains(t, specsContent, "\"test-registry\"")
	require.Contains(t, specsContent, "\"enterprise-tools\"")
	require.Contains(t, specsContent, "\"1.2.3\"")
}

// TestRegistryToolsetSpecsGeneratorData verifies generator data identifies registry toolsets.
func TestRegistryToolsetSpecsGeneratorData(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("data_test", func() {})

		reg := Registry("data-registry", func() {
			goadsl.URL("https://data.registry.io")
		})

		regTools := Toolset(FromRegistry(reg, "data-tools"))

		goadsl.Service("data_test", func() {
			Agent("data-agent", "Data test agent", func() {
				Use(regTools)
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	require.NotEmpty(t, agentsExpr.Root.Agents)
	dslAgent := agentsExpr.Root.Agents[0]
	require.NotNil(t, dslAgent.Used)
	require.NotEmpty(t, dslAgent.Used.Toolsets)
	dslToolset := dslAgent.Used.Toolsets[0]
	require.NotNil(t, dslToolset.Provider)
	require.Equal(t, agentsExpr.ProviderRegistry, dslToolset.Provider.Kind)

	data, err := codegen.BuildDataForTest("example.com/data", []eval.Root{goaexpr.Root, agentsExpr.Root})
	require.NoError(t, err)
	require.NotNil(t, data)

	var svc *codegen.ServiceAgentsData
	for _, s := range data.Services {
		if s.Service.Name == "data_test" {
			svc = s
			break
		}
	}
	require.NotNil(t, svc)
	require.NotEmpty(t, svc.Agents)

	agent := svc.Agents[0]
	require.Equal(t, "data-agent", agent.Name)

	var regToolset *codegen.ToolsetData
	for _, ts := range agent.AllToolsets {
		if ts.Name == "data-tools" {
			regToolset = ts
			break
		}
	}
	require.NotNil(t, regToolset)
	require.True(t, regToolset.IsRegistryBacked)
	require.NotNil(t, regToolset.Registry)
	require.Equal(t, "data-registry", regToolset.Registry.RegistryName)
	require.Equal(t, "data-tools", regToolset.Registry.ToolsetName)
}
