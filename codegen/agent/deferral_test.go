// These tests resolve authored Deferred names only after generation has the
// complete local or MCP tool list, and reject names that do not match it.
package codegen_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	agentcodegen "goa.design/goa-ai/codegen/agent"
	"goa.design/goa-ai/codegen/testhelpers"
	. "goa.design/goa-ai/dsl"
	agentexpr "goa.design/goa-ai/expr/agent"
	. "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
)

func TestDeferredResolvesCompiledToolNames(t *testing.T) {
	for _, source := range []string{"local", "inline", "external MCP", "Goa MCP"} {
		for _, name := range []string{"search-records", "missing", "records.search-records", "SearchRecords", " search-records", "lookup"} {
			t.Run(source+"/"+name, func(t *testing.T) {
				roots := testhelpers.SetupEvalRoots(t)
				require.True(t, eval.Execute(func() {
					API("discovery", func() {})
					Service("provider", func() {
						if source == "Goa MCP" {
							MCP("remote", "1.0.0")
							JSONRPC(func() { POST("/rpc") })
							Method("search", func() {
								Result(String)
								Tool("search-records", "Search records.")
							})
						}
					})
					definition := func() {
						Tool("search-records", "Search records.", func() { Return(String) })
					}
					var records *agentexpr.ToolsetExpr
					switch source {
					case "local":
						records = Toolset("records", definition)
					case "external MCP":
						records = Toolset("records", FromExternalMCP("provider", "remote"), definition)
					case "Goa MCP":
						records = Toolset("records", FromMCP("provider", "remote"))
					}
					Service("discovery", func() {
						Agent("reader", "Read records.", func() {
							if source == "inline" {
								Use("records", func() {
									Deferred(name)
									definition()
								})
							} else {
								Use(records, func() { Deferred(name) })
							}
							Use("other", func() { Tool("lookup", "Look up another resource.") })
						})
					})
				}, nil), eval.Context.Error())
				require.NoError(t, eval.RunDSL(), "name resolution must wait for the complete compiled tools")
				files, err := agentcodegen.BuildFilesForTest("generated.local/gen", roots, false)
				if name == "search-records" {
					require.NoError(t, err)
					require.NotEmpty(t, files)
				} else {
					require.ErrorContains(t, err, `agent "discovery.reader" toolset "records": Deferred selects unknown tool`)
					require.Empty(t, files)
				}
			})
		}
	}
}
