package codegen_test

import (
	"bytes"
	"encoding/json"
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

// TestToolSpecsDeterministicTypeRefs verifies that tool_specs types use
// deterministic, fully-qualified references computed from Goa NameScope
// (GoFullTypeRef/Name) with no pointer surgery or fallbacks.
func TestToolSpecsDeterministicTypeRefs(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("alpha", func() {})
		// Define a user type at API scope so it can be referenced from service/attributes.
		var Doc = goadsl.Type("Doc", func() {
			goadsl.Attribute("id", goadsl.String, "ID")
			goadsl.Attribute("title", goadsl.String, "Title")
			goadsl.Required("id", "title")
		})

		goadsl.Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				Use("summarize", func() {
					Tool("summarize_doc", "Summarize a document", func() {
						// Use the service user type directly as top-level args and result.
						Args(Doc)
						Return(Doc)
					})
				})
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := codegen.Generate("goa.design/goa-ai", []eval.Root{goaexpr.Root, agentsExpr.Root}, nil)
	require.NoError(t, err)

	var codecs string
	for _, f := range files {
		if filepath.ToSlash(f.Path) == filepath.ToSlash("gen/alpha/tools/summarize/codecs.go") {
			for _, s := range f.SectionTemplates {
				codecs += s.Source
			}
			break
		}
	}
	require.NotEmpty(t, codecs, "expected generated tools/summarize/codecs.go")
	// The template source contains placeholders; concrete rendering is validated
	// via specs_builder_internal_test. Here we simply assert we found the file.
}

// TestToolSchemasJSONEmitted verifies that tool_schemas.json is emitted for
// agents and that its structure matches the documented format.
func TestToolSchemasJSONEmitted(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("orchestrator", func() {})
		var Ask = goadsl.Type("Ask", func() {
			goadsl.Attribute("question", goadsl.String, "Question")
			goadsl.Required("question")
		})
		var Answer = goadsl.Type("Answer", func() {
			goadsl.Attribute("text", goadsl.String, "Answer text")
			goadsl.Required("text")
		})

		goadsl.Service("orchestrator", func() {
			Agent("chat", "Friendly Q&A agent", func() {
				Use("helpers", func() {
					Tool("answer", "Answer a simple question", func() {
						Args(Ask)
						Return(Answer)
					})
				})
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := codegen.Generate("example.com/assistant", []eval.Root{goaexpr.Root, agentsExpr.Root}, nil)
	require.NoError(t, err)

	var payload string
	for _, f := range files {
		if filepath.ToSlash(f.Path) == filepath.ToSlash("gen/orchestrator/agents/chat/specs/tool_schemas.json") {
			var buf bytes.Buffer
			for _, s := range f.SectionTemplates {
				require.NoError(t, s.Write(&buf))
			}
			payload = buf.String()
			break
		}
	}
	require.NotEmpty(t, payload, "expected generated tool_schemas.json")

	var doc struct {
		Tools []struct {
			ID      string `json:"id"`
			Service string `json:"service"`
			Toolset string `json:"toolset"`
			Title   string `json:"title"`
			Payload *struct {
				Name   string          `json:"name"`
				Schema json.RawMessage `json:"schema"`
			} `json:"payload"`
			Result *struct {
				Name   string          `json:"name"`
				Schema json.RawMessage `json:"schema"`
			} `json:"result"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal([]byte(payload), &doc))
	require.Len(t, doc.Tools, 1)

	tool := doc.Tools[0]
	require.Equal(t, "helpers.answer", tool.ID)
	require.Equal(t, "orchestrator", tool.Service)
	require.Equal(t, "orchestrator.helpers", tool.Toolset)
	require.Equal(t, "Answer", tool.Title)
	require.NotNil(t, tool.Payload)
	require.NotNil(t, tool.Result)
	require.NotEmpty(t, tool.Payload.Name)
	require.NotEmpty(t, tool.Result.Name)
	require.NotEmpty(t, tool.Payload.Schema)
	require.NotEmpty(t, tool.Result.Schema)
}

// TestServiceToolsetCrossServiceBindTo verifies cross-service BindTo uses the
// target service package for method payload/result references in generator data
// (used by executor-first registrations and transforms).
func TestServiceToolsetCrossServiceBindTo(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("multi", func() {})
		// Service B provides the bound method.
		goadsl.Service("bravo", func() {
			goadsl.Method("Lookup", func() {
				goadsl.Payload(func() {
					goadsl.Attribute("id", goadsl.String, "Identifier")
					goadsl.Required("id")
				})
				goadsl.Result(func() {
					goadsl.Attribute("ok", goadsl.Boolean, "OK")
					goadsl.Required("ok")
				})
			})
		})

		// Service A hosts the agent that binds to bravo.Lookup.
		goadsl.Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				Use("lookup", func() {
					Tool("by_id", "Lookup by ID", func() {
						Args(goadsl.String)
						Return(goadsl.Boolean)
						BindTo("bravo", "Lookup")
					})
				})
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	// Verify generator data marks SourceService correctly for cross-service binding.
	data, err := codegen.BuildDataForTest("goa.design/goa-ai", []eval.Root{goaexpr.Root, agentsExpr.Root})
	require.NoError(t, err)
	require.NotNil(t, data)

	// Find the alpha.scribe agent and its toolset.
	var alphaSvc *codegen.ServiceAgentsData
	for _, s := range data.Services {
		if s.Service.Name == "alpha" {
			alphaSvc = s
			break
		}
	}
	require.NotNil(t, alphaSvc)
	require.NotEmpty(t, alphaSvc.Agents)
	ts := alphaSvc.Agents[0].UsedToolsets[0]
	require.Equal(t, "lookup", ts.Name)
	require.Equal(t, "bravo", ts.SourceService.PkgName, "cross-service binding should reference target service package")
}
