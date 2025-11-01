package codegen_test

import (
	"bytes"
	"maps"
	"path/filepath"
	"testing"
	"text/template"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agents"
	. "goa.design/goa-ai/dsl"
	agentsExpr "goa.design/goa-ai/expr/agents"
	gcodegen "goa.design/goa/v3/codegen"
	. "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// Verifies service toolset template exposes an executor-first API (no adapters/clients).
func TestServiceToolset_ConfigNoDefaults(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))
	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		API("svc", func() {})
		// Service with method that has session_id in payload and two result fields
		Service("svc", func() {
			Method("Do", func() {
				Payload(func() {
					Attribute("session_id", String)
					Attribute("q", String)
					Required("session_id")
				})
				Result(func() {
					Attribute("ok", Boolean)
					Attribute("msg", String)
					Required("ok")
				})
			})
			// Agent with a tool bound to svc.Do (within service DSL)
			Agent("a", "", func() {
				Uses(func() {
					Toolset("ts", func() {
						Tool("do", "", func() {
							Args(String)
							Return(Boolean)
							BindTo("Do")
						})
					})
				})
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := codegen.Generate("goa.design/goa-ai", []eval.Root{goaexpr.Root, agentsExpr.Root}, nil)
	require.NoError(t, err)

	// Find generated service_toolset.go and render content
	var content string
	for _, f := range files {
		if filepath.ToSlash(f.Path) != filepath.ToSlash("gen/svc/agents/a/ts/service_toolset.go") {
			continue
		}
		var buf bytes.Buffer
		for _, s := range f.SectionTemplates {
			tmpl := template.New(s.Name).Funcs(template.FuncMap{
				"comment":     gcodegen.Comment,
				"commandLine": func() string { return "" },
			})
			if s.FuncMap != nil {
				fm := template.FuncMap{}
				maps.Copy(fm, s.FuncMap)
				tmpl = tmpl.Funcs(fm)
			}
			pt, err := tmpl.Parse(s.Source)
			require.NoError(t, err)
			var sb bytes.Buffer
			require.NoError(t, pt.Execute(&sb, s.Data))
			buf.Write(sb.Bytes())
		}
		content = buf.String()
		break
	}
	require.NotEmpty(t, content, "service_toolset.go not generated")

	// Executor-based constructor
	require.Contains(t, content, "func NewATsToolsetRegistration(exec runtime.ToolCallExecutor)")
	// Requires non-nil executor and delegates to exec.Execute
	require.Contains(t, content, "executor is required")
	require.Contains(t, content, "return exec.Execute(ctx, meta, call)")
	// No config/adapters emitted
	require.NotContains(t, content, "type TsConfig struct {")
	require.NotContains(t, content, "Adapter TsAdapter")
}
