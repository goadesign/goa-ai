package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

var _ = Service("catalog_service", func() {
	Description("Provides one nested generated agent for evaluation contract lookup.")
	Agent("catalog", "Retrieves records from a catalog.", func() {
		Export("records", func() {
			Tool("lookup", "Look up catalog records.", func() {
				Args(func() {
					Attribute("query", String, "Catalog query.")
					Required("query")
				})
				Return(func() {
					Attribute("answer", String, "Matching catalog records.")
					Required("answer")
				})
			})
		})
	})
})

var _ = Service("assistant_service", func() {
	Description("Owns the agent attached to the generated eval suite.")
	Method("get", func() {
		Description("Returns the fixture status for generated-consumer tests.")
		Result(String)
	})
	Agent("assistant", "Answers questions from catalog records.", func() {
		Use("assistant_tools", func() {
			Tool("answer", "Answer a catalog question.", func() {
				Args(func() {
					Attribute("question", String, "Catalog question.")
					Required("question")
				})
				Return(func() {
					Attribute("answer", String, "Catalog answer.")
					Required("answer")
				})
			})
		})
		Use(AgentToolset("catalog_service", "catalog", "records"))
		AssistantEvalSuite()
	})
})
