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

// TestRegistryToolsetSpecsStructure checks that the agent emits its exact source
// read without a startup discovery package or a runtime source loop.
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

	files, err := codegen.BuildFilesForTest("example.com/registry", []eval.Root{goaexpr.Root, agentsExpr.Root}, false)
	require.NoError(t, err)

	var specsContent string
	expectedPath := filepath.ToSlash("gen/registry_test/agents/data_agent/agent.go")
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
	require.NotEmpty(t, specsContent, "expected generated agent.go at %s", expectedPath)

	require.Contains(t, specsContent, `catalog.IncludeToolset(ctx, "corp-registry", "data-tools", "", false)`)
	require.Contains(t, specsContent, `registry == "corp-registry" && toolset == "data-tools"`)
	require.Contains(t, specsContent, "WithRegistryTools(registryTools{})")
	require.NotContains(t, specsContent, "RegistryToolsets")
	require.NotContains(t, specsContent, "Discover(")
	require.NotContains(t, specsContent, "for _,")
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

	files, err := codegen.BuildFilesForTest("example.com/meta", []eval.Root{goaexpr.Root, agentsExpr.Root}, false)
	require.NoError(t, err)

	var specsContent string
	expectedPath := filepath.ToSlash("gen/meta_test/agents/meta_agent/agent.go")
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
	require.NotEmpty(t, specsContent, "expected generated agent.go at %s", expectedPath)

	require.Contains(t, specsContent, "\"test-registry\"")
	require.Contains(t, specsContent, "\"enterprise-tools\"")
	require.Contains(t, specsContent, "\"1.2.3\"")
	require.Contains(t, specsContent, `catalog.IncludeToolset(ctx, "test-registry", "enterprise-tools", "1.2.3", false)`)
	require.Contains(t, specsContent, `version == "1.2.3"`)
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
	require.Empty(t, regToolset.Tools)
}
