package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

var _ = Service("atlas_data_agent", func() {
	Description("Provides one nested generated agent for eval contract lookup.")
	Agent("atlas_data", "Retrieves facility data.", func() {
		Export("ada", func() {
			Tool("fetch", "Fetch facility data.", func() {
				Args(func() {
					Attribute("query", String, "Facility data query.")
					Required("query")
				})
				Return(func() {
					Attribute("answer", String, "Fetched data.")
					Required("answer")
				})
			})
		})
	})
})

var _ = Service("chat_agent", func() {
	Description("Owns the agent attached to the generated eval suite.")
	Method("get", func() {
		Description("Returns the fixture status for generated-consumer tests.")
		Result(String)
	})
	Agent("chat", "Answers product questions.", func() {
		Use("chat_tools", func() {
			Tool("answer", "Answer a product question.", func() {
				Args(func() {
					Attribute("question", String, "Product question.")
					Required("question")
				})
				Return(func() {
					Attribute("answer", String, "Product answer.")
					Required("answer")
				})
			})
		})
		Use(AgentToolset("atlas_data_agent", "atlas_data", "ada"))
		ChatEvalSuite()
	})
})
