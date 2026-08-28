// Package design defines a small evaluation suite used to prove that an
// external Goa application can generate and consume Goa-AI eval code.
package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa-ai/eval/dsl"
	. "goa.design/goa/v3/dsl"
)

var QueryEvalInput = Type("QueryEvalInput", func() {
	Attribute("query", String, "Request sent to the agent.", func() {
		MinLength(1)
	})
	Required("query")
})

var SavedQueryEvalInput = Type("SavedQueryEvalInput", func() {
	Attribute("saved_query_id", String, "Saved query identifier.", func() {
		Format(FormatUUID)
	})
	Required("saved_query_id")
})

// AssistantEvalSuite declares two scenarios with distinct typed inputs so the
// fixture exercises generated hook signatures and scenario selection.
func AssistantEvalSuite() {
	Suite("assistant_quality", func() {
		Description("Checks direct and saved queries through generated evaluation hooks.")
		Timeout("5s")

		Scenario("record_summary", func() {
			Description("Summarizes records returned for a direct query.")
			Input(QueryEvalInput)
			Tags("records", "direct")
		})
		Scenario("saved_query_replay", func() {
			Description("Runs a previously saved query.")
			Input(SavedQueryEvalInput)
			Tags("records", "saved")
		})
	})
}
