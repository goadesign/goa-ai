// Package testscenarios contains reusable Goa designs for agent generator tests.
package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// MethodHelperNameCollisions defines tools whose Go names collide with fixed
// service executor names and with one another.
func MethodHelperNameCollisions() func() {
	return func() {
		API("helper collisions", func() {})
		var Input = Type("CollisionInput", func() {
			Attribute("value", String, "Value passed to the method.")
			Required("value")
		})
		var Output = Type("CollisionOutput", func() {
			Attribute("value", String, "Value returned by the method.")
			Required("value")
		})
		Service("alpha", func() {
			for _, name := range []string{"Client", "ExecOpt", "AgentDash", "AgentUnderscore"} {
				Method(name, func() {
					Payload(Input)
					Result(Output)
				})
			}
			Agent("scribe", "Runs collision tests.", func() {
				Use("helpers", func() {
					for _, tool := range []struct {
						name   string
						method string
					}{
						{name: "client", method: "Client"},
						{name: "exec_opt", method: "ExecOpt"},
						{name: "agent-id", method: "AgentDash"},
						{name: "agent_id", method: "AgentUnderscore"},
					} {
						Tool(tool.name, "Exercises generated helper names.", func() {
							Args(Input)
							Return(Output)
							BindTo("alpha", tool.method)
						})
					}
				})
			})
		})
	}
}
