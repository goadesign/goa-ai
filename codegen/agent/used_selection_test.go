// These tests check that consumer tool selections narrow generated agent catalogs
// while shared provider specifications continue to describe every declared tool.
package codegen_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	codegen "goa.design/goa-ai/codegen/agent"
	"goa.design/goa-ai/codegen/testhelpers"
	. "goa.design/goa-ai/dsl"
	agentexpr "goa.design/goa-ai/expr/agent"
	goadsl "goa.design/goa/v3/dsl"
)

func TestUsedToolSelectionKeepsProviderComplete(t *testing.T) {
	for _, source := range []string{"local", "external MCP", "Goa MCP", "agent"} {
		t.Run(source, func(t *testing.T) {
			genpkg, roots := testhelpers.RunDesign(t, func() {
				goadsl.API("selection", func() {})
				definition := func() {
					Tool("read", "Read a record.", func() { Return(goadsl.String) })
					Tool("write", "Write a record.", func() { Return(goadsl.String) })
				}
				var shared *agentexpr.ToolsetExpr
				switch source {
				case "external MCP":
					shared = Toolset("remote_records", FromExternalMCP("provider", "remote"), definition)
				case "Goa MCP":
					shared = Toolset("remote_records", FromMCP("provider", "remote"))
				default:
					shared = Toolset("records", definition)
				}
				goadsl.Service("provider", func() {
					if source == "Goa MCP" {
						MCP("remote", "1.0.0")
						goadsl.JSONRPC(func() { goadsl.POST("/rpc") })
						goadsl.Method("read", func() {
							goadsl.Result(goadsl.String)
							Tool("read", "Read a record.")
						})
						goadsl.Method("write", func() {
							goadsl.Result(goadsl.String)
							Tool("write", "Write a record.")
						})
					}
					if source == "agent" {
						Agent("worker", "Process records.", func() { Export(shared) })
					}
				})
				goadsl.Service("consumer", func() {
					Agent("reader", "Read records.", func() { Use(shared, func() { Tool("read") }) })
					Agent("writer", "Write records.", func() { Use(shared, func() { Tool("write") }) })
					Agent("all", "Use every tool.", func() { Use(shared) })
					Agent("deferred", "Find tools on demand.", func() { Use(shared, func() { Deferred() }) })
					Agent("inline", "Use a local tool.", func() {
						Use("inline", func() { Tool("local", "Use a local tool.") })
					})
				})
			})
			files, err := codegen.BuildFilesForTest(genpkg, roots, false)
			require.NoError(t, err)
			for _, consumer := range []struct{ name, included, excluded string }{
				{"reader", "SpecRead()", "SpecWrite()"},
				{"writer", "SpecWrite()", "SpecRead()"},
			} {
				content := testhelpers.FileContent(t, files, "gen/consumer/agents/"+consumer.name+"/specs/specs.go")
				assert.Contains(t, content, consumer.included)
				assert.NotContains(t, content, consumer.excluded)
				assert.NotContains(t, content, ".Specs()...")
			}
			for _, name := range []string{"all", "deferred"} {
				content := testhelpers.FileContent(t, files, "gen/consumer/agents/"+name+"/specs/specs.go")
				assert.Contains(t, content, "SpecRead()")
				assert.Contains(t, content, "SpecWrite()")
			}
			inline := testhelpers.FileContent(t, files, "gen/consumer/agents/inline/specs/specs.go")
			assert.Contains(t, inline, "SpecLocal()")
			sharedPath := "gen/consumer/toolsets/records/specs.go"
			if source == "external MCP" || source == "Goa MCP" {
				sharedPath = "gen/provider/toolsets/remote_records/specs.go"
			}
			if source == "agent" {
				sharedPath = "gen/provider/agents/worker/exports/records/specs.go"
			}
			if source == "local" {
				registry := testhelpers.FileContent(t, files, "gen/consumer/agents/reader/registry.go")
				assert.Contains(t, registry, "records.Specs()")
			}
			provider := testhelpers.FileContent(t, files, sharedPath)
			assert.Contains(t, provider, "newSpecRead()")
			assert.Contains(t, provider, "newSpecWrite()")
		})
	}
}

func TestUsedToolSelectionRejectsUnknownNames(t *testing.T) {
	genpkg, roots := testhelpers.RunDesign(t, func() {
		goadsl.API("selection", func() {})
		shared := Toolset("records", func() { Tool("read", "Read a record.") })
		goadsl.Service("consumer", func() {
			Agent("reader", "Read records.", func() { Use(shared, func() { Tool("missing") }) })
		})
	})
	_, err := codegen.BuildFilesForTest(genpkg, roots, false)
	require.ErrorContains(t, err, `Use selects unknown tool "missing"`)
}

func TestUsedToolSelectionPreservesBoundInput(t *testing.T) {
	genpkg, roots := testhelpers.RunDesign(t, func() {
		goadsl.API("selection", func() {})
		shared := Toolset("records", func() {
			Tool("read", "Read a record.", func() {
				Args(func() {
					goadsl.Attribute("scope", goadsl.String, "Record scope supplied by the application.")
					goadsl.Required("scope")
				})
				Return(goadsl.String)
			})
			Tool("write", "Write a record.")
		})
		goadsl.Service("consumer", func() {
			Agent("bound", "Use all tools with a fixed read scope.", func() {
				Use(shared, func() {
					BindInput("read", "scope")
				})
			})
			Agent("selected", "Read within a fixed scope.", func() {
				Use(shared, func() {
					Tool("read")
					BindInput("read", "scope")
				})
			})
		})
	})
	files, err := codegen.BuildFilesForTest(genpkg, roots, false)
	require.NoError(t, err)
	bound := testhelpers.FileContent(t, files, "gen/consumer/agents/bound/specs/specs.go")
	assert.Contains(t, bound, "SpecRead()")
	assert.Contains(t, bound, "SpecWrite()")
	selected := testhelpers.FileContent(t, files, "gen/consumer/agents/selected/specs/specs.go")
	assert.Contains(t, selected, "SpecRead()")
	assert.NotContains(t, selected, "SpecWrite()")
	for _, agent := range agentexpr.Root.Agents {
		require.Len(t, agent.Used.Toolsets[0].InputBindings, 1)
		assert.Equal(t, "read", agent.Used.Toolsets[0].InputBindings[0].Tool)
		assert.Equal(t, []string{"scope"}, agent.Used.Toolsets[0].InputBindings[0].Fields)
	}
}

func TestUsedToolSelectionRejectsExcludedConfiguration(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func()
		want      string
	}{
		{
			name: "duplicate selection",
			configure: func() {
				Tool("read")
				Tool("read")
			},
			want: `Use selects duplicate tool "read"`,
		},
		{
			name: "deferred excluded tool",
			configure: func() {
				Tool("write")
				Deferred("read")
			},
			want: `Deferred selects unknown tool "read"`,
		},
		{
			name: "bound excluded tool",
			configure: func() {
				Tool("write")
				BindInput("read", "scope")
			},
			want: `input binding tool "read" has no compiled declaration`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			genpkg, roots := testhelpers.RunDesign(t, func() {
				goadsl.API("selection", func() {})
				shared := Toolset("records", func() {
					Tool("read", "Read a record.", func() {
						Args(func() {
							goadsl.Attribute("scope", goadsl.String, "Record scope supplied by the application.")
							goadsl.Required("scope")
						})
					})
					Tool("write", "Write a record.")
				})
				goadsl.Service("consumer", func() {
					Agent("reader", "Use selected tools.", func() {
						Use(shared, test.configure)
					})
				})
			})
			_, err := codegen.BuildFilesForTest(genpkg, roots, false)
			require.ErrorContains(t, err, test.want)
		})
	}
}
