// This file declares the question and answer values exposed for one suspended
// run. The run owner chooses its first saved request and derives private request
// identifiers. Only maps of independent host questions or tool calls expose keys.
package types

import (
	registrytypes "goa.design/goa-ai/registry/design/types"
	agenttypes "goa.design/goa-ai/runtime/agent/design/types"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
	contenttypes "goa.design/goa-ai/runtime/content/design/types"
	hosttypes "goa.design/goa-ai/runtime/mcp/design/types"
	. "goa.design/goa/v3/dsl"
)

var (
	// RequestedInput returns the first outstanding question for one saved run.
	RequestedInput = Type("RequestedInput", func() {
		Meta("struct:pkg:path", "runinput")
		Meta("type:generate:force")
		Meta("openapi:generate", "false")
		Description("The first saved request awaiting a caller response. The observed current Run ID selects the request; callers never provide private suspension identifiers or service state.")
		OneOf("request", func() {
			TypeName("RunInputRequest")
			Field(1, "mcp", MapOf(String, hosttypes.InputRequest), "Host questions keyed by their original server identifiers.")
			Field(2, "clarification", InputClarification, "One free-text question.")
			Field(3, "confirmation", InputConfirmation, "Approval requested before the selected action.")
			Field(4, "questions", InputQuestions, "Structured questions with their saved answer contract.")
			Field(5, "external_tools", MapOf(String, InputToolCall), "External tool calls keyed by their exact saved call identifiers.")
		})
		Required("request")
	})

	// InputResponse answers exactly the saved request selected by the Run ID.
	InputResponse = Type("InputResponse", func() {
		Meta("struct:pkg:path", "runinput")
		Meta("type:generate:force")
		Meta("openapi:generate", "false")
		Description("An answer to the first saved request of the specified current Run. The runtime derives private request identifiers and validates tool results against the retained original schema.")
		OneOf("answer", func() {
			TypeName("RunInputAnswer")
			Field(1, "mcp", MapOf(String, Bytes), "Original host response JSON keyed by exact server question identifiers. The MCP owner validates form or URL answers and preserves open protocol extension fields.")
			Field(2, "clarification", String, "Free-text answer to the displayed question.")
			Field(3, "confirmation", Boolean, "Whether the caller approves the displayed action.")
			Field(4, "questions", InputToolResult, "One structured answer using the saved result contract.")
			Field(5, "external_tools", MapOf(String, InputToolResult), "Results keyed by the exact requested tool call identifiers.")
		})
		Required("answer")
	})

	// InputClarification describes the information missing from the current task.
	InputClarification = Type("InputClarification", func() {
		Meta("struct:pkg:path", "runinput")
		Field(1, "question", String, "Question shown to the caller.")
		Field(2, "missing_fields", ArrayOf(String), "Fields needing clarification, when provided.")
		Field(3, "example_json", Bytes, "Original example accepted by the requested tool, when provided.")
		Required("question")
	})

	// InputConfirmation describes the action whose execution needs approval.
	InputConfirmation = Type("InputConfirmation", func() {
		Meta("struct:pkg:path", "runinput")
		Field(1, "title", String, "Optional approval title.")
		Field(2, "prompt", String, "Approval question shown to the caller.")
		Field(3, "tool_name", String, "Tool that will execute after approval.")
		Field(4, "payload", Bytes, "Exact arguments for the action being approved.")
		Required("prompt", "tool_name", "payload")
	})

	// InputQuestions pairs displayed choices with the original tool result schema.
	InputQuestions = Type("InputQuestions", func() {
		Meta("struct:pkg:path", "runinput")
		Field(1, "title", String, "Optional question title.")
		Field(2, "questions", ArrayOfRequired(InputQuestion), "Questions shown in their original order.")
		Field(3, "result_contract", InputResultContract, "Saved contract that validates the submitted answer.")
		Required("questions", "result_contract")
	})

	// InputQuestion retains one question and its available choices.
	InputQuestion = Type("InputQuestion", func() {
		Meta("struct:pkg:path", "runinput")
		Field(1, "id", String, "Question identifier within this request.")
		Field(2, "prompt", String, "Question text.")
		Field(3, "options", ArrayOfRequired(InputQuestionOption), "Selectable answers in their original order.")
		Field(4, "allow_multiple", Boolean, "Whether the caller may select several options.")
		Required("id", "prompt", "options", "allow_multiple")
	})

	// InputQuestionOption retains one available answer.
	InputQuestionOption = Type("InputQuestionOption", func() {
		Meta("struct:pkg:path", "runinput")
		CreateFrom(planner.AwaitQuestionOption{})
		ConvertTo(planner.AwaitQuestionOption{})
		Field(1, "id", String, "Answer identifier within this question.")
		Field(2, "label", String, "Answer text shown to the caller.")
		Required("id", "label")
	})

	// InputToolCall describes one external call still awaiting its result.
	InputToolCall = Type("InputToolCall", func() {
		Meta("struct:pkg:path", "runinput")
		Field(1, "name", String, "Exact requested tool name.")
		Field(2, "payload", Bytes, "Original tool arguments.")
		Field(3, "result_contract", InputResultContract, "Saved contract for the submitted result.")
		Required("name", "payload", "result_contract")
	})

	// InputResultContract describes the result accepted by the saved invocation.
	InputResultContract = Type("InputResultContract", func() {
		Meta("struct:pkg:path", "runinput")
		Field(1, "schema", Bytes, "Original result JSON schema; absent only when the tool has no result value.")
		Field(2, "bounds", registrytypes.ToolBounds, "Original saved result-bounds and pagination contract, when declared.")
	})

	// InputToolResult preserves content and either successful data or failure facts.
	InputToolResult = Type("InputToolResult", func() {
		Meta("struct:pkg:path", "runinput")
		Field(1, "blocks", ArrayOfRequired(contenttypes.Block), "Ordered text, media and resources returned by the tool.")
		OneOf("outcome", func() {
			TypeName("InputToolOutcome")
			Field(2, "success", InputToolSuccess, "Successful result validated with the saved schema.")
			Field(3, "failure", InputToolFailure, "Failure facts supplied by the external executor.")
		})
		Required("outcome")
	})

	// InputToolSuccess retains successful data and optional coverage information.
	InputToolSuccess = Type("InputToolSuccess", func() {
		Meta("struct:pkg:path", "runinput")
		Field(1, "result", Bytes, "Original JSON result; absent only when the tool has no result value.")
		Field(2, "bounds", agenttypes.ResultBounds, "Coverage information required by the saved tool contract.")
	})

	// InputToolFailure retains only the existing external failure contract.
	InputToolFailure = Type("InputToolFailure", func() {
		Meta("struct:pkg:path", "runinput")
		Field(1, "kind", String, "Why external execution failed.", func() {
			Enum(string(planner.FailureInvalidCall), string(planner.FailureDomainRejection), string(planner.FailureUnavailable), string(planner.FailureRateLimited), string(planner.FailureTimeout), string(planner.FailureMalformedResult), string(planner.FailureInternal))
		})
		Field(2, "message", String, "Description of the external failure.")
		Field(3, "action", String, "Existing recovery action for the failed tool.", func() {
			Enum(string(planner.RecoveryCorrectCall), string(planner.RecoveryReplan), string(planner.RecoveryFinish))
		})
		Field(4, "issues", ArrayOfRequired(InputFieldIssue), "Generated field validation issues for rejected arguments.")
		Required("kind", "message", "action")
	})

	// InputFieldIssue retains the generated codec's existing correction details.
	InputFieldIssue = Type("InputFieldIssue", func() {
		Meta("struct:pkg:path", "runinput")
		CreateFrom(tools.FieldIssue{})
		ConvertTo(tools.FieldIssue{})
		Field(1, "field", String, "Field that failed validation.")
		Field(2, "constraint", String, "Existing validation constraint name.")
		Field(3, "allowed", ArrayOf(String), "Allowed field names or values.")
		Field(4, "min_len", Int, "Declared minimum length, when present.")
		Field(5, "max_len", Int, "Declared maximum length, when present.")
		Field(6, "pattern", String, "Declared pattern, when present.")
		Field(7, "format", String, "Declared format, when present.")
		Field(8, "expected_json_type", String, "Expected JSON type, when present.")
		Field(9, "actual_json_type", String, "Received JSON type, when present.")
		Required("field", "constraint")
	})
)
