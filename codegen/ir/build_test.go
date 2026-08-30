// This file checks that the agent intermediate data keeps Goa's final service
// directories and assigns each toolset to one stable owner.
package ir_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	ir "goa.design/goa-ai/codegen/ir"
	"goa.design/goa-ai/codegen/testhelpers"
	. "goa.design/goa-ai/dsl"
	agentsExpr "goa.design/goa-ai/expr/agent"
	goacodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	. "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

func TestBuild_Deterministic(t *testing.T) {
	design := func() {
		API("svc", func() {})

		var QPayload = Type("QPayload", func() {
			Attribute("q", String, "Q")
			Required("q")
		})
		var OkResult = Type("OkResult", func() {
			Attribute("ok", Boolean, "OK")
		})
		Service("svc", func() {
			Method("Do", func() {
				Payload(func() {
					Attribute("q", String, "Q")
					Required("q")
				})
				Result(func() {
					Attribute("ok", Boolean, "OK")
				})
			})
			Agent("scribe", "Doc helper", func() {
				Use("lookup", func() {
					Tool("by_id", "Lookup by ID", func() {
						Args(QPayload)
						Return(OkResult)
						BindTo("Do")
					})
				})
			})
		})
	}

	genpkg, roots := testhelpers.RunDesign(t, design)
	a, err := buildDesign(t, genpkg, roots)
	require.NoError(t, err)
	b, err := buildDesign(t, genpkg, roots)
	require.NoError(t, err)

	aj, err := json.Marshal(a)
	require.NoError(t, err)
	bj, err := json.Marshal(b)
	require.NoError(t, err)
	require.Equal(t, string(aj), string(bj))
}

func TestBuild_ToolsetOwnership_ServiceLexicographic(t *testing.T) {
	design := func() {
		API("multi", func() {})

		var Shared = Toolset("shared", func() {
			Tool("ping", "Ping", func() {
				Args(func() {
					Attribute("msg", String, "Message")
					Required("msg")
				})
				Return(func() {
					Attribute("ok", Boolean, "OK")
					Required("ok")
				})
			})
		})

		Service("bravo", func() {
			Agent("b", "B", func() {
				Use(Shared, func() {
					Tool("ping")
				})
			})
		})
		Service("alpha", func() {
			Agent("a", "A", func() {
				Use(Shared, func() {
					Tool("ping")
				})
			})
		})
	}

	genpkg, roots := testhelpers.RunDesign(t, design)
	got, err := buildDesign(t, genpkg, roots)
	require.NoError(t, err)

	require.Len(t, got.Toolsets, 1)
	ts := got.Toolsets[0]
	require.Equal(t, "shared", ts.Name)
	require.Equal(t, ir.OwnerKindService, ts.Owner.Kind)
	require.Equal(t, "alpha", ts.Owner.ServiceName)
	require.Equal(t, "alpha", ts.Owner.ServicePathName)
}

func TestBuild_ToolsetOwnership_ExportWins(t *testing.T) {
	design := func() {
		API("multi", func() {})

		var Shared = Toolset("shared", func() {
			Tool("ping", "Ping", func() {
				Args(func() {
					Attribute("msg", String, "Message")
					Required("msg")
				})
				Return(func() {
					Attribute("ok", Boolean, "OK")
					Required("ok")
				})
			})
		})

		Service("bravo", func() {
			Agent("provider", "Provider", func() {
				Export(Shared, func() {
					Tool("ping")
				})
			})
		})
		Service("alpha", func() {
			Agent("consumer", "Consumer", func() {
				Use(Shared, func() {
					Tool("ping")
				})
			})
		})
	}

	genpkg, roots := testhelpers.RunDesign(t, design)
	got, err := buildDesign(t, genpkg, roots)
	require.NoError(t, err)

	require.Len(t, got.Toolsets, 1)
	ts := got.Toolsets[0]
	var provider *ir.Agent
	for _, agent := range got.Agents {
		if agent.Name == "provider" {
			provider = agent
			break
		}
	}
	require.NotNil(t, provider)
	require.Equal(t, "shared", ts.Name)
	require.Equal(t, ir.OwnerKindAgentExport, ts.Owner.Kind)
	require.Same(t, provider.ExportedToolsets[0], ts.Owner.Ref)
	require.Equal(t, "bravo", ts.Owner.ServiceName)
	require.Equal(t, "provider", ts.Owner.AgentName)
	require.NotEmpty(t, ts.Owner.AgentSlug)
	var consumer *ir.Agent
	for _, agent := range got.Agents {
		if agent.Name == "consumer" {
			consumer = agent
			break
		}
	}
	require.NotNil(t, consumer)
	require.Len(t, consumer.UsedToolsets, 1)
	used := consumer.UsedToolsets[0]
	require.Same(t, provider.ExportedToolsets[0], used.SourceExport)
	require.Equal(t, provider.ExportedToolsets[0].AgentToolsPackage, used.AgentToolsPackage)
	require.Equal(t, provider.ExportedToolsets[0].AgentToolsImportPath, used.AgentToolsImportPath)
	require.Equal(t, provider.ExportedToolsets[0].AgentToolsDir, used.AgentToolsDir)
}

func TestBuild_ToolsetOwnership_ServiceExportWins(t *testing.T) {
	design := func() {
		API("multi", func() {})

		var Shared = Toolset("shared", func() {
			Tool("ping", "Ping", func() {
				Args(func() {
					Attribute("msg", String, "Message")
					Required("msg")
				})
				Return(func() {
					Attribute("ok", Boolean, "OK")
					Required("ok")
				})
			})
		})

		Service("bravo", func() {
			Export(Shared, func() {
				Tool("ping")
			})
		})
		Service("alpha", func() {
			Agent("consumer", "Consumer", func() {
				Use(Shared, func() {
					Tool("ping")
				})
			})
		})
	}

	genpkg, roots := testhelpers.RunDesign(t, design)
	got, err := buildDesign(t, genpkg, roots)
	require.NoError(t, err)

	require.Len(t, got.Toolsets, 1)
	ts := got.Toolsets[0]
	require.Equal(t, "shared", ts.Name)
	require.Equal(t, ir.OwnerKindService, ts.Owner.Kind)
	require.Equal(t, "bravo", ts.Owner.ServiceName)
	require.Equal(t, "bravo", ts.Owner.ServicePathName)
}

func TestBuild_PreservesEveryServiceExport(t *testing.T) {
	design := func() {
		API("multi", func() {})

		var Shared = Toolset("atlas.read", func() {
			Tool("ping", "Ping", func() {})
		})

		Service("atlas_data", func() {
			Export(Shared, func() {
				Tool("ping")
			})
		})
		Service("beta", func() {
			Export(Shared, func() {
				Tool("ping")
			})
		})
	}

	genpkg, roots := testhelpers.RunDesign(t, design)
	got, err := buildDesign(t, genpkg, roots)
	require.NoError(t, err)

	require.Len(t, got.ServiceExports, 2)
	alpha := got.ServiceExports[0]
	beta := got.ServiceExports[1]
	require.Equal(t, "atlas_data", alpha.Service.Name)
	require.Equal(t, "atlas.read", alpha.Name)
	require.Equal(t, "atlas_data.atlas.read", alpha.QualifiedName)
	require.Equal(t, "beta.atlas.read", beta.QualifiedName)
	require.Same(t, alpha.Definition, beta.Definition)
	require.Equal(t, got.Toolsets[0], alpha.Definition)
	require.Equal(t, genpkg+"/atlas_data", alpha.Service.ImportPath)
	require.Equal(t, "gen/atlas_data", alpha.Service.Dir)
	require.Equal(t, []*ir.ToolsetRef{alpha}, alpha.Service.Exports)
	require.Equal(t, []*ir.ToolsetRef{beta}, beta.Service.Exports)

	// The selected owner only chooses where the reusable specs are written.
	require.Equal(t, "atlas_data", got.Toolsets[0].Owner.ServiceName)
	require.Same(t, alpha, got.Toolsets[0].Owner.Ref)
}

func TestBuild_RejectsOwnerScopedSanitizedCollisions(t *testing.T) {
	genpkg, roots := testhelpers.RunDesign(t, func() {
		API("multi", func() {})
		Service("consumer", func() {})
	})
	goaRoot, agentsRoot := buildRoots(t, roots)
	consumer := goaRoot.Service("consumer")
	require.NotNil(t, consumer)
	planner := &agentsExpr.AgentExpr{Name: "planner", Service: consumer}
	runner := &agentsExpr.AgentExpr{Name: "runner", Service: consumer}
	planner.Used = &agentsExpr.ToolsetGroupExpr{
		Toolsets: []*agentsExpr.ToolsetExpr{
			{Name: "remote-tools", Agent: planner},
		},
	}
	runner.Used = &agentsExpr.ToolsetGroupExpr{
		Toolsets: []*agentsExpr.ToolsetExpr{
			{Name: "remote_tools", Agent: runner},
		},
	}
	agentsRoot.Agents = []*agentsExpr.AgentExpr{planner, runner}
	_, err := buildDesign(t, genpkg, roots)
	require.Error(t, err)
	require.ErrorContains(t, err, `collides`)
	require.ErrorContains(t, err, `remote_tools`)
}

func TestBuild_RejectsUnsanitizableAgentNames(t *testing.T) {
	genpkg, roots := testhelpers.RunDesign(t, func() {
		API("multi", func() {})
		Service("consumer", func() {})
	})
	goaRoot, agentsRoot := buildRoots(t, roots)
	consumer := goaRoot.Service("consumer")
	require.NotNil(t, consumer)
	agentsRoot.Agents = []*agentsExpr.AgentExpr{
		{Name: "!!!", Service: consumer},
	}

	_, err := buildDesign(t, genpkg, roots)

	require.Error(t, err)
	require.ErrorContains(t, err, `agent "!!!" has no sanitized identifier`)
}

func TestBuild_ServiceAgentAndCompletionLayout(t *testing.T) {
	design := func() {
		API("svc", func() {})

		var Shared = Toolset("shared_tools", func() {
			Tool("ping", "Ping", func() {})
		})

		Service("svc", func() {
			Completion("draft", "Draft completion", func() {
				Return(func() {
					Attribute("text", String, "Draft text")
				})
			})
			Agent("scribe", "Doc helper", func() {
				Use(Shared, func() {
					Tool("ping")
				})
			})
		})
	}

	genpkg, roots := testhelpers.RunDesign(t, design)
	got, err := buildDesign(t, genpkg, roots)
	require.NoError(t, err)

	require.Len(t, got.Services, 1)
	svc := got.Services[0]
	require.Equal(t, "svc", svc.Name)
	require.Len(t, svc.Agents, 1)
	require.Len(t, svc.Completions, 1)

	agent := svc.Agents[0]
	require.Equal(t, "scribe", agent.Name)
	require.Equal(t, "svc.scribe", agent.ID)
	require.Equal(t, "scribe", agent.Slug)
	require.Equal(t, "scribe", agent.PackageName)
	require.Equal(t, "svc_scribe_workflow", agent.WorkflowQueue)
	require.Len(t, agent.UsedToolsets, 1)

	ref := agent.UsedToolsets[0]
	require.Equal(t, "shared_tools", ref.Name)
	require.Equal(t, "svc.shared_tools", ref.QualifiedName)
	require.Equal(t, "svc", ref.ServiceName)
	require.Equal(t, "svc", ref.SourceServiceName)
	require.Equal(t, "shared_tools", ref.SpecsPackageName)
	require.Equal(t, "gen/svc/toolsets/shared_tools", ref.SpecsDir)

	completion := svc.Completions[0]
	require.Equal(t, "draft", completion.Name)
	require.Equal(t, "Draft", completion.GoName)
}

// buildRoots returns the Goa service definitions and goa-ai agent definitions
// created by a test design.
func buildRoots(t *testing.T, roots []eval.Root) (*goaexpr.RootExpr, *agentsExpr.RootExpr) {
	t.Helper()

	var goaRoot *goaexpr.RootExpr
	var agentsRoot *agentsExpr.RootExpr
	for _, root := range roots {
		switch root := root.(type) {
		case *goaexpr.RootExpr:
			goaRoot = root
		case *agentsExpr.RootExpr:
			agentsRoot = root
		}
	}
	require.NotNil(t, goaRoot)
	require.NotNil(t, agentsRoot)
	return goaRoot, agentsRoot
}

// buildDesign creates the Goa service plan that owns generated package paths,
// then builds the agent IR from that same plan.
func buildDesign(t *testing.T, genpkg string, roots []eval.Root) (*ir.Design, error) {
	t.Helper()
	generation, err := goacodegen.NewGeneration(genpkg, roots)
	if err != nil {
		return nil, err
	}
	goaRoot, _ := buildRoots(t, roots)
	plan, err := service.NewPlan(
		goaRoot,
		generation,
		goaexpr.NewExampleGenerator(goaRoot.API.RandomizerFactory),
	)
	if err != nil {
		return nil, err
	}
	return ir.Build(generation, plan)
}
