package ir_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	ir "goa.design/goa-ai/codegen/ir"
	"goa.design/goa-ai/codegen/testhelpers"
	. "goa.design/goa-ai/dsl"
	agentsExpr "goa.design/goa-ai/expr/agent"
	. "goa.design/goa/v3/dsl"
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
	goaRoot := roots[0].(*goaexpr.RootExpr)
	agentsRoot := roots[1].(*agentsExpr.RootExpr)
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
	goaRoot := roots[0].(*goaexpr.RootExpr)
	agentsRoot := roots[1].(*agentsExpr.RootExpr)
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
