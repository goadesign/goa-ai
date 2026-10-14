// Package types defines the executor identity saved with each run. Its generated
// union requires either an Agent executor or a service tool with its Registry
// source. This identity describes the work; it does not grant permission to run it.
package types

import . "goa.design/goa/v3/dsl"

// ServiceToolSubject identifies the selected service tool and its configured source.
var ServiceToolSubject = Type("ServiceToolSubject", func() {
	Meta("struct:pkg:path", "runidentity")
	Description("The selected service tool and the configured Registry source that owns it.")
	Field(1, "registry", String, "Configured Registry source that owns the selected tool.", func() {
		Meta("struct:tag:json", "registry")
		MinLength(1)
	})
	Field(2, "tool", String, "Qualified tool name in that Registry source, with nonempty text before and after the final dot.", func() {
		Meta("struct:tag:json", "tool")
		Pattern(`^[\s\S]+\.[^.]+$`)
	})
	Required("registry", "tool")
})

// RunSubject selects the executor whose identity remains fixed for one run.
var RunSubject = Type("RunSubject", func() {
	Meta("struct:pkg:path", "runidentity")
	Meta("type:generate:force")
	Meta("openapi:generate", "false")
	Description("Exactly one Agent executor or service tool executed by a run. The selected identity remains fixed for that run and does not grant permission to execute it.")
	OneOf("executor", func() {
		Meta("struct:tag:json", "executor")
		TypeName("RunExecutor")
		Field(1, "agent", String, "Agent executor that processes the run.", func() {
			MinLength(1)
		})
		Field(2, "service_tool", ServiceToolSubject, "Service tool directly executed by the run.")
	})
	Required("executor")
})
