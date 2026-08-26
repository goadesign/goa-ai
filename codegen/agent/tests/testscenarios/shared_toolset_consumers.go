package testscenarios

// This file defines one reusable tool contract consumed from two services. It
// catches generators that mistake the package location for the service that
// executes the tool.

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// SharedToolsetConsumers declares one top-level toolset used by two agents in
// different services.
func SharedToolsetConsumers() func() {
	return func() {
		API("shared_toolset_consumers", func() {})

		shared := aidsl.Toolset("shared", func() {
			aidsl.Tool("ping", "Return the supplied message.", func() {
				aidsl.Args(func() {
					Attribute("message", String, "Message to return.")
					Required("message")
				})
				aidsl.Return(String, "Returned message.")
			})
		})

		Service("alpha", func() {
			aidsl.Agent("alpha_worker", "Runs shared tools for alpha.", func() {
				aidsl.Use(shared)
			})
		})
		Service("beta", func() {
			aidsl.Agent("beta_worker", "Runs shared tools for beta.", func() {
				aidsl.Use(shared)
			})
		})
	}
}
