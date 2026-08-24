// Package codegen generates typed evaluation suites and application scaffolds.
// This file stores all values needed by one eval generation run.
package codegen

import (
	"errors"
	"path"
	"path/filepath"

	agentir "goa.design/goa-ai/codegen/ir"
	evalexpr "goa.design/goa-ai/eval/expr"
	goacodegen "goa.design/goa/v3/codegen"
	goagenerator "goa.design/goa/v3/codegen/generator"
	"goa.design/goa/v3/eval"
)

type (
	// evalPlan stores the Goa generation, generated eval files, and whether the
	// command is goa example.
	evalPlan struct {
		generation *goacodegen.Generation
		suites     []*suitePlan
		examples   []*examplePlan
		example    bool
	}
)

func init() {
	goagenerator.RegisterPlugin("eval", "gen", newEvalPluginFactory(false))
	goagenerator.RegisterPlugin("eval", "example", newEvalPluginFactory(true))
}

// newEvalPluginFactory returns a fresh eval plan for each Goa command.
func newEvalPluginFactory(example bool) goagenerator.PluginFactory {
	return func() goagenerator.Plugin {
		planned := &evalPlan{example: example}
		return goagenerator.Plugin{
			Plan:     planned.planPlugin,
			Generate: planned.generatePlugin,
		}
	}
}

// planPlugin records all suite values before Goa finalizes generated names.
func (p *evalPlan) planPlugin(plan *goagenerator.Plan) error {
	return p.plan(plan.Generation())
}

// generatePlugin writes files from the values saved by planPlugin.
func (p *evalPlan) generatePlugin(
	plan *goagenerator.Plan,
	files []*goacodegen.File,
) ([]*goacodegen.File, error) {
	if plan.Generation() != p.generation {
		return nil, errors.New("eval generation received a different Goa plan")
	}
	return p.generate(files)
}

// plan copies each evaluation suite and declares every package-level Go name.
func (p *evalPlan) plan(generation *goacodegen.Generation) error {
	if p.generation != nil {
		return errors.New("eval generation was planned more than once")
	}
	p.generation = generation
	roots := generation.Roots()
	root := evaluationRoot(roots)
	if root == nil {
		return nil
	}
	if p.example {
		return p.planExamples(root)
	}
	return p.planSuites(root, roots)
}

// planSuites saves each suite and its tool contract.
func (p *evalPlan) planSuites(root *evalexpr.RootExpr, roots []eval.Root) error {
	var design *agentir.Design
	for _, suite := range root.Suites {
		if suite.Agent == nil {
			continue
		}
		var err error
		design, err = agentir.Build(p.generation.GenPkg(), roots)
		if err != nil {
			return err
		}
		break
	}
	for _, suite := range root.Suites {
		contract, err := planContract(suite, design)
		if err != nil {
			return err
		}
		planned, err := planSuite(p.generation, suite, contract)
		if err != nil {
			return err
		}
		p.suites = append(p.suites, planned)
	}
	return nil
}

// planExamples records one starter command for each suite. Goa writes the file
// only when it does not already exist.
func (p *evalPlan) planExamples(root *evalexpr.RootExpr) error {
	for _, suite := range root.Suites {
		plannedSuite, err := planSuite(p.generation, suite, nil)
		if err != nil {
			return err
		}
		outputDirectory := filepath.Join("cmd", suite.Name+"-evals")
		planned, err := planExample(p.generation, plannedSuite, outputDirectory)
		if err != nil {
			return err
		}
		p.examples = append(p.examples, planned)
	}
	return nil
}

// generate uses final package names and adds the files saved by Plan.
func (p *evalPlan) generate(files []*goacodegen.File) ([]*goacodegen.File, error) {
	if p.generation == nil {
		return nil, errors.New("eval generation was not planned")
	}
	if !p.generation.Frozen() {
		return nil, errors.New("eval generation names are not frozen")
	}
	if p.example {
		for _, planned := range p.examples {
			files = append(files, exampleFile(planned.Output, linkExampleData(planned)))
		}
		return files, nil
	}
	for _, planned := range p.suites {
		data := linkSuiteData(planned, "")
		files = append(files, &goacodegen.File{
			Path:             planned.Output,
			SectionTemplates: suiteSections(data),
		})
		if planned.Contract != nil {
			files = append(files, &goacodegen.File{
				Path: filepath.Join(
					filepath.Dir(planned.Output),
					"contract.go",
				),
				SectionTemplates: contractSections(data.Package, linkContractData(planned)),
			})
		}
	}
	return files, nil
}

// planExample declares every package name written by one starter command.
func planExample(
	generation *goacodegen.Generation,
	suite *suitePlan,
	outputDirectory string,
) (*examplePlan, error) {
	importPath := path.Join(path.Dir(generation.GenPkg()), filepath.ToSlash(outputDirectory))
	pkg, err := generation.ClaimOutputPackage(importPath, outputDirectory)
	if err != nil {
		return nil, err
	}
	planned := &examplePlan{
		Suite:       suite,
		Output:      filepath.Join(outputDirectory, "main.go"),
		PackagePlan: pkg,
	}
	planned.Hooks, err = declareEvalName(pkg, goacodegen.NameType, "hooks", goacodegen.UnexportedName, suite.Name, "example")
	if err != nil {
		return nil, err
	}
	planned.Values, err = declareEvalName(pkg, goacodegen.NameType, "values", goacodegen.UnexportedName, suite.Name, "example")
	if err != nil {
		return nil, err
	}
	planned.Options, err = declareEvalName(pkg, goacodegen.NameType, "options", goacodegen.UnexportedName, suite.Name, "example")
	if err != nil {
		return nil, err
	}
	planned.Run, err = declareEvalName(pkg, goacodegen.NameFunction, "run", goacodegen.UnexportedName, suite.Name, "example")
	if err != nil {
		return nil, err
	}
	planned.ScenarioInputs, err = declareEvalName(pkg, goacodegen.NameFunction, "scenarioInputs", goacodegen.UnexportedName, suite.Name, "example")
	if err != nil {
		return nil, err
	}
	planned.Main = goacodegen.NewExactName(goacodegen.NameFunction, "main")
	if err := pkg.DeclareName(planned.Main); err != nil {
		return nil, err
	}
	return planned, nil
}

// evaluationRoot finds the evaluation suites supplied to Goa.
func evaluationRoot(roots []eval.Root) *evalexpr.RootExpr {
	for _, root := range roots {
		if suites, ok := root.(*evalexpr.RootExpr); ok {
			return suites
		}
	}
	return nil
}
