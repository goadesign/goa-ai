package ir_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	ir "goa.design/goa-ai/codegen/ir"
	"goa.design/goa-ai/codegen/testhelpers"
	aidsl "goa.design/goa-ai/dsl"
	agentsExpr "goa.design/goa-ai/expr/agent"
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
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Use("lookup", func() {
					aidsl.Tool("by_id", "Lookup by ID", func() {
						aidsl.Args(QPayload)
						aidsl.Return(OkResult)
						aidsl.BindTo("Do")
					})
				})
			})
		})
	}

	genpkg, roots := testhelpers.RunDesign(t, design)
	a, err := ir.Build(genpkg, roots)
	require.NoError(t, err)
	b, err := ir.Build(genpkg, roots)
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

		var Shared = aidsl.Toolset("shared", func() {
			aidsl.Tool("ping", "Ping", func() {
				aidsl.Args(func() {
					Attribute("msg", String, "Message")
					Required("msg")
				})
				aidsl.Return(func() {
					Attribute("ok", Boolean, "OK")
					Required("ok")
				})
			})
		})

		Service("bravo", func() {
			aidsl.Agent("b", "B", func() {
				aidsl.Use(Shared, func() {
					aidsl.Tool("ping")
				})
			})
		})
		Service("alpha", func() {
			aidsl.Agent("a", "A", func() {
				aidsl.Use(Shared, func() {
					aidsl.Tool("ping")
				})
			})
		})
	}

	genpkg, roots := testhelpers.RunDesign(t, design)
	got, err := ir.Build(genpkg, roots)
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

		var Shared = aidsl.Toolset("shared", func() {
			aidsl.Tool("ping", "Ping", func() {
				aidsl.Args(func() {
					Attribute("msg", String, "Message")
					Required("msg")
				})
				aidsl.Return(func() {
					Attribute("ok", Boolean, "OK")
					Required("ok")
				})
			})
		})

		Service("bravo", func() {
			aidsl.Agent("provider", "Provider", func() {
				aidsl.Export(Shared, func() {
					aidsl.Tool("ping")
				})
			})
		})
		Service("alpha", func() {
			aidsl.Agent("consumer", "Consumer", func() {
				aidsl.Use(Shared, func() {
					aidsl.Tool("ping")
				})
			})
		})
	}

	genpkg, roots := testhelpers.RunDesign(t, design)
	got, err := ir.Build(genpkg, roots)
	require.NoError(t, err)

	require.Len(t, got.Toolsets, 1)
	ts := got.Toolsets[0]
	require.Equal(t, "shared", ts.Name)
	require.Equal(t, ir.OwnerKindAgentExport, ts.Owner.Kind)
	require.Equal(t, "bravo", ts.Owner.ServiceName)
	require.Equal(t, "provider", ts.Owner.AgentName)
	require.NotEmpty(t, ts.Owner.AgentSlug)
}

func TestBuild_ToolsetOwnership_ServiceExportWins(t *testing.T) {
	design := func() {
		API("multi", func() {})

		var Shared = aidsl.Toolset("shared", func() {
			aidsl.Tool("ping", "Ping", func() {
				aidsl.Args(func() {
					Attribute("msg", String, "Message")
					Required("msg")
				})
				aidsl.Return(func() {
					Attribute("ok", Boolean, "OK")
					Required("ok")
				})
			})
		})

		Service("bravo", func() {
			aidsl.Export(Shared, func() {
				aidsl.Tool("ping")
			})
		})
		Service("alpha", func() {
			aidsl.Agent("consumer", "Consumer", func() {
				aidsl.Use(Shared, func() {
					aidsl.Tool("ping")
				})
			})
		})
	}

	genpkg, roots := testhelpers.RunDesign(t, design)
	got, err := ir.Build(genpkg, roots)
	require.NoError(t, err)

	require.Len(t, got.Toolsets, 1)
	ts := got.Toolsets[0]
	require.Equal(t, "shared", ts.Name)
	require.Equal(t, ir.OwnerKindService, ts.Owner.Kind)
	require.Equal(t, "bravo", ts.Owner.ServiceName)
	require.Equal(t, "bravo", ts.Owner.ServicePathName)
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
	_, err := ir.Build(genpkg, roots)
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

	_, err := ir.Build(genpkg, roots)

	require.Error(t, err)
	require.ErrorContains(t, err, `agent "!!!" has no sanitized identifier`)
}

func TestBuild_ServiceAgentAndCompletionLayout(t *testing.T) {
	design := func() {
		API("svc", func() {})

		var Shared = aidsl.Toolset("shared_tools", func() {
			aidsl.Tool("ping", "Ping", func() {})
		})

		Service("svc", func() {
			aidsl.Completion("draft", "Draft completion", func() {
				aidsl.Return(func() {
					Attribute("text", String, "Draft text")
				})
			})
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Use(Shared, func() {
					aidsl.Tool("ping")
				})
			})
		})
	}

	genpkg, roots := testhelpers.RunDesign(t, design)
	got, err := ir.Build(genpkg, roots)
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
