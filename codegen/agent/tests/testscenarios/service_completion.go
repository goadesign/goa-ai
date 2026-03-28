package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ServiceCompletion returns a DSL with a service-owned typed completion that
// exercises nested user types and OneOf result shapes.
func ServiceCompletion() func() {
	return func() {
		API("tasks", func() {})

		var CompletionStep = Type("CompletionStep", func() {
			Attribute("label", String, "User-visible step label")
			Required("label")
		})

		var CompletionDraft = Type("CompletionDraft", func() {
			Attribute("steps", ArrayOf(CompletionStep), "Draft steps")
			Required("steps")
		})

		var CompletionResult = Type("CompletionResult", func() {
			Attribute("assistant_text", String, "Assistant summary")
			OneOf("draft", func() {
				Attribute("new_task", CompletionDraft, "Draft a new task")
				Attribute("existing_task_id", String, "Reference an existing task")
			})
			Required("assistant_text", "draft")
		})

		Service("tasks", func() {
			Completion("draft_from_transcript", "Synthesize a task draft from a transcript", func() {
				Return(CompletionResult)
			})
		})
	}
}
