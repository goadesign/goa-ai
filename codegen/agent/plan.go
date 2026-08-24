// Package codegen remembers which agent files to write and the service types and
// package names those files use.
package codegen

import (
	"fmt"

	agentir "goa.design/goa-ai/codegen/ir"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	goacodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/eval"
)

type (
	// Plan stores the agent design, Goa service types, generated package names,
	// and command mode used to write one run's files.
	Plan struct {
		roots    []eval.Root
		design   *agentir.Design
		service  *service.Plan
		specs    *toolSpecsPlan
		registry *registryClientPlan
		mcp      *mcpexpr.RootExpr
		data     *GeneratorData
		example  bool
	}
)

// NewPlan records the agent files produced by the goa gen command. generation
// and servicePlan must belong to the same Goa run.
func NewPlan(generation *goacodegen.Generation, servicePlan *service.Plan) (*Plan, error) {
	return newPlan(generation, servicePlan, false)
}

// NewExamplePlan records the application files produced by the goa example
// command. generation and servicePlan must belong to the same Goa run.
func NewExamplePlan(generation *goacodegen.Generation, servicePlan *service.Plan) (*Plan, error) {
	return newPlan(generation, servicePlan, true)
}

// Link reads the service types and package names chosen by Goa. Call it after
// Goa and the service generator have finished choosing names.
func (p *Plan) Link() error {
	if p.data != nil {
		return fmt.Errorf("agent plan is already linked")
	}
	data, err := buildGeneratorDataFromIR(p.design, p.service.Services(), p.mcp)
	if err != nil {
		return err
	}
	if p.specs != nil {
		if err := p.specs.link(data); err != nil {
			return err
		}
	}
	if p.registry != nil {
		p.registry.link(data)
	}
	p.data = data
	return nil
}

// Files appends the agent or application files selected by this plan. Link
// must run first.
func (p *Plan) Files(files []*goacodegen.File) ([]*goacodegen.File, error) {
	if p.data == nil {
		return nil, fmt.Errorf("agent plan is not linked")
	}
	if p.example {
		return generateExampleFiles(p.data, files)
	}
	return generateAgentFiles(p.data, p.roots, p.specs, files)
}

// newPlan records one agent design and declares the packages used by its
// generated files.
func newPlan(generation *goacodegen.Generation, servicePlan *service.Plan, example bool) (*Plan, error) {
	roots := generation.Roots()
	design, err := agentir.Build(generation.GenPkg(), roots)
	if err != nil {
		return nil, err
	}
	if servicePlan.Root() != design.GoaRoot {
		return nil, fmt.Errorf("agent and Goa service plans use different design roots")
	}
	plan := &Plan{
		roots:   roots,
		design:  design,
		service: servicePlan,
		mcp:     mcpDesignRoot(roots),
		example: example,
	}
	plan.specs, err = planToolSpecs(generation, design, design.GoaRoot.API, plan.mcp)
	if err != nil {
		return nil, err
	}
	if !example {
		plan.registry, err = planRegistryClients(generation, design)
		if err != nil {
			return nil, err
		}
	}
	return plan, nil
}

// mcpDesignRoot returns the MCP definitions supplied to one Goa generation.
func mcpDesignRoot(roots []eval.Root) *mcpexpr.RootExpr {
	for _, root := range roots {
		if mcpRoot, ok := root.(*mcpexpr.RootExpr); ok {
			return mcpRoot
		}
	}
	return nil
}
