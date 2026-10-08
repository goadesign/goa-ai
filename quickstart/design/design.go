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

// CapturedToolFailure keeps the facts used by the example's exact predicates.
var CapturedToolFailure = Type("CapturedToolFailure", func() {
	Attribute("kind", String, "Observed failure classification.")
	Attribute("message", String, "Observed failure explanation.")
	Attribute("recovery_action", String, "The runtime's recorded recovery action.")
	Required("kind", "message", "recovery_action")
})

// CapturedToolCall records one invocation without requiring it to succeed.
var CapturedToolCall = Type("CapturedToolCall", func() {
	Attribute("name", String, "The invoked tool.")
	Attribute("call_id", String, "The identifier linking invocation and result.")
	Attribute("parent_call_id", String, "The parent call, or empty for a root call.")
	Attribute("arguments", Bytes, "Exact tool-argument JSON bytes.")
	Attribute("result", Bytes, "Exact result JSON bytes, when available.")
	Attribute("completed", Boolean, "Whether a terminal tool result was observed.")
	Attribute("failure", CapturedToolFailure, "Observed failure, when the tool failed.")
	Required("name", "call_id", "parent_call_id", "completed")
})

// GreetingObservation accepts successful, empty, partial, and failed outcomes.
var GreetingObservation = Type("GreetingObservation", func() {
	Attribute("question", String, "The original question sent to the agent.")
	Attribute("answer", String, "Captured assistant text, including an empty answer.")
	Attribute("tool_calls", ArrayOf(CapturedToolCall), "Invocations in the collector's causal order.")
	Attribute("terminal_phase", String, "Observed final workflow phase, or empty when absent.")
	Attribute("terminal_failure", String, "Observed workflow failure explanation, or empty.")
	Attribute("run_error", String, "The product call's returned error, or empty on success.")
	Required("question", "answer", "terminal_phase", "terminal_failure", "run_error")
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
			Deferred()
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
				Observation(GreetingObservation)
				Check("run_tools", "The agent completes and answers the original question through helpers.answer.")
				Tags("smoke")
			})
			Scenario("helpers_contract", func() {
				Description("The helpers.answer tool contract is reachable from the agent.")
				Observation(Boolean)
				Check("payload_schema", "The reachable helpers.answer contract includes a payload schema.")
				Tags("contract")
			})
		})
	})
})
