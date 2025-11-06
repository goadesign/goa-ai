package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	codegen "goa.design/goa-ai/codegen/agent"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	agentsExpr "goa.design/goa-ai/expr/agent"
	gcodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

func main() {
	// Initialize roots
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	must(eval.Register(goaexpr.Root))
	must(eval.Register(goaexpr.GeneratedResultTypes))
	agentsExpr.Root = &agentsExpr.RootExpr{}
	must(eval.Register(agentsExpr.Root))

	// Execute scenario
	design := testscenarios.MethodComplexEmbedded()
	ok := eval.Execute(design, nil)
	if !ok {
		panic(eval.Context.Error())
	}
	must(eval.RunDSL())

	// Generate
	files, err := codegen.Generate("goa.design/goa-ai", []eval.Root{goaexpr.Root, agentsExpr.Root}, nil)
	must(err)

	// Render codecs.go for profiles toolset
	target := filepath.ToSlash("gen/alpha/agents/scribe/specs/profiles/codecs.go")
	var content string
	for _, f := range files {
		if filepath.ToSlash(f.Path) != target {
			continue
		}
		var buf bytes.Buffer
		for _, s := range f.SectionTemplates {
			// mimic test helper rendering
			tmpl := template.New(s.Name)
			fm := template.FuncMap{
				"comment":     gcodegen.Comment,
				"commandLine": func() string { return "" },
			}
			if s.FuncMap != nil {
				for k, v := range s.FuncMap {
					fm[k] = v
				}
			}
			tmpl = tmpl.Funcs(fm)
			pt, err := tmpl.Parse(s.Source)
			must(err)
			var sb bytes.Buffer
			must(pt.Execute(&sb, s.Data))
			buf.Write(sb.Bytes())
		}
		content = buf.String()
		break
	}
	if content == "" {
		panic("profiles/codecs.go not generated")
	}

	golden := filepath.Join("codegen", "agent", "tests", "testdata", "golden", "method_complex_embedded", "codecs.go.golden")
	if err := os.WriteFile(golden, []byte(content), 0600); err != nil {
		panic(fmt.Errorf("write golden: %w", err))
	}
	fmt.Println("Updated:", golden)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
