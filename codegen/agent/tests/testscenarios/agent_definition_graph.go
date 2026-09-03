// Package testscenarios contains complete Goa designs used by agent generator tests.
package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// AgentDefinitionGrandchild declares a root agent that calls a child agent,
// which in turn calls a grandchild agent.
func AgentDefinitionGrandchild() func() {
	return func() {
		API("agent_definition_grandchild", func() {})
		var WorkInput = Type("WorkInput", func() {
			Attribute("request", String, "Requested work")
			Required("request")
		})
		childTools := Toolset("child_work", func() {
			Tool("delegate", "Delegate work to the child.", func() {
				Args(WorkInput)
				Return(String)
			})
		})
		grandchildTools := Toolset("grandchild_work", func() {
			Tool("finish", "Finish work in the grandchild.", func() {
				Args(WorkInput)
				Return(String)
			})
		})
		Service("leaf", func() {
			Agent("grandchild", "Finishes delegated work.", func() {
				Export(grandchildTools)
			})
		})
		Service("middle", func() {
			Agent("child", "Delegates work to a grandchild.", func() {
				Export(childTools)
				Use(grandchildTools)
			})
		})
		Service("entry", func() {
			Agent("root", "Starts delegated work.", func() {
				Use(childTools)
			})
		})
	}
}

// AgentDefinitionCycle declares two agents that can call each other. The
// generated definition graph must stay finite even though the design graph is
// cyclic.
func AgentDefinitionCycle() func() {
	return func() {
		API("agent_definition_cycle", func() {})
		var WorkInput = Type("WorkInput", func() {
			Attribute("request", String, "Requested work")
			Required("request")
		})
		alphaTools := Toolset("alpha_work", func() {
			Tool("alpha", "Run alpha work.", func() {
				Args(WorkInput)
				Return(String)
			})
		})
		betaTools := Toolset("beta_work", func() {
			Tool("beta", "Run beta work.", func() {
				Args(WorkInput)
				Return(String)
			})
		})
		Service("alpha", func() {
			Agent("worker", "Runs alpha work.", func() {
				Export(alphaTools)
				Use(betaTools)
			})
		})
		Service("beta", func() {
			Agent("worker", "Runs beta work.", func() {
				Export(betaTools)
				Use(alphaTools)
			})
		})
	}
}
