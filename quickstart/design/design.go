package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa-ai/eval/dsl"
	. "goa.design/goa/v3/dsl"
)

var _ = API("orchestrator", func() {})

// Input and output types with inline descriptions (required by this repo style)
var AskPayload = Type("AskPayload", func() {
	Attribute("question", String, "User question to answer")
	Example(map[string]any{"question": "What is the capital of Japan?"})
	Required("question")
})

var Answer = Type("Answer", func() {
	Attribute("text", String, "Answer text")
	Example(map[string]any{"text": "Tokyo is the capital of Japan."})
	Required("text")
})

var DraftTaskStep = Type("DraftTaskStep", func() {
	Attribute("title", String, "Short step title")
	Example(map[string]any{"title": "Review the current launch checklist"})
	Required("title")
})

var TaskDraft = Type("TaskDraft", func() {
	Attribute("assistant_text", String, "Short explanation of the generated draft")
	Attribute("name", String, "Task name")
	Attribute("goal", String, "Outcome-style goal")
	Attribute("steps", ArrayOf(DraftTaskStep), "Ordered draft steps")
	Example(map[string]any{
		"assistant_text": "Created a launch-readiness task draft.",
		"name":           "Prepare launch checklist",
		"goal":           "Confirm the service is ready to launch.",
		"steps": []map[string]any{
			{"title": "Review release notes and rollout scope"},
			{"title": "Confirm dashboards and alerts are healthy"},
			{"title": "Share the launch checklist with stakeholders"},
		},
	})
	Required("assistant_text", "name", "goal", "steps")
})

var _ = Service("orchestrator", func() {
	Completion("draft_task", "Produce a task draft directly", func() {
		Return(TaskDraft)
	})

	Agent("chat", "Friendly Q&A assistant", func() {
		Use("helpers", func() {
			Tool("answer", "Answer a simple question", func() {
				Args(AskPayload)
				Return(Answer)
			})
		})
		RunPolicy(func() {
			DefaultCaps(MaxToolCalls(2), MaxRecoveryTurns(1))
			TimeBudget("15s")
		})

		// Evaluation suite: goa gen emits one typed hook per scenario under
		// gen/evals/chat_quality and goa example scaffolds an application-owned
		// cmd/chat_quality-evals command once.
		Suite("chat_quality", func() {
			Description("Evaluates the chat agent end to end against the in-memory runtime.")
			Timeout("30s")
			Scenario("greeting_reply", func() {
				Description("The agent produces a final assistant reply to a user question.")
				Input(AskPayload)
				Tags("smoke")
			})
			Scenario("helpers_contract", func() {
				Description("The helpers.answer tool contract is reachable from the agent.")
				Tags("contract")
			})
		})
	})
})
