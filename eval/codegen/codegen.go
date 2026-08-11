// Package codegen generates typed evaluation suites and application scaffolds.
package codegen

import (
	"path/filepath"

	agentir "goa.design/goa-ai/codegen/ir"
	evalexpr "goa.design/goa-ai/eval/expr"
	goacodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
)

func init() {
	goacodegen.RegisterPlugin("eval", "gen", nil, Generate)
	goacodegen.RegisterPlugin("eval", "example", nil, GenerateExample)
}

// Generate emits one package per suite under gen/evals. Each package exposes
// generated input types, a direct Hooks interface, and a validating
// constructor. Agent-attached suites also expose their static reachable tool
// contracts.
func Generate(genpkg string, roots []eval.Root, files []*goacodegen.File) ([]*goacodegen.File, error) {
	root := evaluationRoot(roots)
	if root == nil {
		return files, nil
	}
	var design *agentir.Design
	for _, suite := range root.Suites {
		if suite.Agent == nil {
			continue
		}
		var err error
		design, err = agentir.Build(genpkg, roots)
		if err != nil {
			return nil, err
		}
		break
	}
	for _, suite := range root.Suites {
		data, err := buildSuiteData(genpkg, suite)
		if err != nil {
			return nil, err
		}
		files = append(files, &goacodegen.File{
			Path:             filepath.Join(goacodegen.Gendir, "evals", suite.Name, "suite.go"),
			SectionTemplates: suiteSections(data),
		})
		contract, err := buildContractData(suite, design)
		if err != nil {
			return nil, err
		}
		if contract == nil {
			continue
		}
		files = append(files, &goacodegen.File{
			Path:             filepath.Join(goacodegen.Gendir, "evals", suite.Name, "contract.go"),
			SectionTemplates: contractSections(data.Package, contract),
		})
	}
	return files, nil
}

// evaluationRoot locates the evaluated generic suite root supplied by Goa.
func evaluationRoot(roots []eval.Root) *evalexpr.RootExpr {
	for _, root := range roots {
		if suites, ok := root.(*evalexpr.RootExpr); ok {
			return suites
		}
	}
	return nil
}
