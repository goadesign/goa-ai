// Package codegen lets external tests run the private eval generation plan.
package codegen

import (
	goacodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
)

type (
	// PlanTestHandle lets external tests run eval planning and file writing.
	PlanTestHandle struct {
		generation *goacodegen.Generation
		plan       *evalPlan
	}
)

// PlanForTest prepares eval files for an external test of Goa's planning and
// file-writing order.
func PlanForTest(genpkg string, roots []eval.Root, example bool) (*PlanTestHandle, error) {
	generation, err := goacodegen.NewGeneration(genpkg, roots)
	if err != nil {
		return nil, err
	}
	planned := &evalPlan{example: example}
	if err := planned.plan(generation); err != nil {
		return nil, err
	}
	return &PlanTestHandle{
		generation: generation,
		plan:       planned,
	}, nil
}

// Generate freezes Goa's names and returns the files saved during planning.
func (h *PlanTestHandle) Generate(files []*goacodegen.File) ([]*goacodegen.File, error) {
	if err := h.generation.Freeze(); err != nil {
		return nil, err
	}
	generated, err := h.plan.generate(files)
	if err != nil {
		return nil, err
	}
	return generated, nil
}

// Generation returns the Goa generation used by this test plan.
func (h *PlanTestHandle) Generation() *goacodegen.Generation {
	return h.generation
}
