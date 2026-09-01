// Package codegen stores the agent design and exact Goa service plan used by one
// generation command. File writing uses only these saved values.
package codegen

import (
	"fmt"

	goacodegen "goa.design/goa/v3/codegen"
	goagenerator "goa.design/goa/v3/codegen/generator"
)

type (
	// agentPluginPlan stores the Goa plan, agent plan, and command mode used by
	// one plugin run.
	agentPluginPlan struct {
		core    *goagenerator.Plan
		agent   *Plan
		example bool
	}
)

// newAgentPluginFactory creates new Prepare, Plan, and Generate functions with
// separate state for each command run.
func newAgentPluginFactory(example bool) goagenerator.PluginFactory {
	return func() goagenerator.Plugin {
		planned := &agentPluginPlan{example: example}
		return goagenerator.Plugin{
			Prepare:  Prepare,
			Plan:     planned.plan,
			Generate: planned.generate,
		}
	}
}

// plan saves the agent definitions, service types, and package-level Go names
// needed to write agent files.
func (p *agentPluginPlan) plan(core *goagenerator.Plan) error {
	p.core = core
	goaRoot, agentRoot := agentDesignRoots(core.Generation().Roots())
	if agentRoot == nil {
		return fmt.Errorf("agent root not found in evaluated designs")
	}
	if goaRoot == nil {
		return fmt.Errorf("goa service root not found in evaluated designs")
	}
	servicePlan := core.Service(goaRoot)
	var err error
	if p.example {
		p.agent, err = NewExamplePlan(core.Generation(), servicePlan)
	} else {
		p.agent, err = NewPlan(core.Generation(), servicePlan)
	}
	return err
}

// generate reads the saved service types and chosen Go names, then adds the
// agent files for this command.
func (p *agentPluginPlan) generate(core *goagenerator.Plan, files []*goacodegen.File) ([]*goacodegen.File, error) {
	if core != p.core {
		return nil, fmt.Errorf("agent generation received a different Goa plan")
	}
	if err := p.agent.Link(); err != nil {
		return nil, err
	}
	return p.agent.Files(files)
}
