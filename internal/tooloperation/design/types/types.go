// Package types describes the runtime's saved operations on one unfinished tool
// invocation. The registry and private value generator share these definitions;
// model-facing tool arguments never contain operation identity or host answers.
// Framework type names identify this owner so importing the registry schema
// leaves ordinary domain type names available to application designs.
package types

import . "goa.design/goa/v3/dsl"

// InputContinuation stores the service's state and the host's accepted answers.
var InputContinuation = Type("ToolOperationInputContinuation", func() {
	Meta("struct:pkg:path", "tooloperations")
	Description("Exact service state and accepted host answers for continuing the original tool invocation.")
	Field(1, "state", String, "Opaque state returned by the service, including an explicitly empty string.")
	Field(2, "responses", MapOf(String, Bytes), "Host answer JSON bytes keyed by the exact server request identifiers.")
})

// TaskAnswers stores the answers submitted to one server-owned Task.
var TaskAnswers = Type("ToolOperationTaskAnswers", func() {
	Meta("struct:pkg:path", "tooloperations")
	Description("Accepted host answers for one existing Task. An empty answer object is valid and does not select a different operation.")
	Field(1, "task_id", String, "Exact server-owned Task identifier, including an empty string.")
	Field(2, "responses", MapOf(String, Bytes), "Host answer JSON bytes for outstanding Task input requests.")
	Required("task_id", "responses")
})

// ExecutionContinuation selects one later operation without changing tool arguments.
var ExecutionContinuation = Type("ToolOperationExecutionContinuation", func() {
	Meta("struct:pkg:path", "tooloperations")
	Description("One runtime-selected operation on the unfinished original tool invocation. Its explicit branch determines the method; field presence never chooses whether to query, answer or cancel.")
	OneOf("operation", func() {
		Field(1, "input", InputContinuation, "Continue the original tool invocation with accepted host answers and saved service state.")
		Field(2, "task_get", String, "Read one existing Task by its exact server-owned identifier.")
		Field(3, "task_update", TaskAnswers, "Submit accepted host answers to one existing Task.")
		Field(4, "task_cancel", String, "Request cancellation of one existing Task without repeating the original tool call.")
	})
	Required("operation")
})

// HostRequest retains one exact server request until the host answers it.
var HostRequest = Type("ToolOperationHostRequest", func() {
	Meta("struct:pkg:path", "tooloperations")
	Description("One server-authored host interaction. Its parameter bytes remain exact; the MCP interaction validator checks their content before admission.")
	Field(1, "method", String, "Exact MCP interaction method.")
	Field(2, "params", Bytes, "Original JSON object describing the host interaction.")
	Required("method", "params")
})

// PendingInput retains ordinary multi-round input without a Task identity.
var PendingInput = Type("ToolOperationPendingInput", func() {
	Meta("struct:pkg:path", "tooloperations")
	Description("Host questions and optional service state for one ordinary unfinished tool call.")
	Field(1, "state", String, "Exact optional service state, including an explicitly empty string.")
	Field(2, "requests", MapOf(String, HostRequest), "Host questions keyed by exact server identifiers. An explicit empty object is valid.", func() {
		Meta("struct:tag:json", "requests,omitzero")
	})
})

// TaskWait identifies the Task whose next observation the workflow must read.
var TaskWait = Type("ToolOperationTaskWait", func() {
	Meta("struct:pkg:path", "tooloperations")
	Description("An existing Task to observe after the accepted creation, a working observation or an acknowledged update. The saved operation identifies which event occurred.")
	Field(1, "task_id", String, "Exact server-owned Task identifier, including an empty string.")
	Field(2, "poll_interval_ms", Int64, "Optional server guidance for the next observation in integer milliseconds; this is not a Task lifetime limit.")
	Required("task_id")
})

// TaskInput retains questions that must be answered through tasks/update.
var TaskInput = Type("ToolOperationTaskInput", func() {
	Meta("struct:pkg:path", "tooloperations")
	Description("Outstanding host questions for one existing Task. Questions are answered with tasks/update rather than repeating the original tool call.")
	Field(1, "task_id", String, "Exact server-owned Task identifier, including an empty string.")
	Field(2, "poll_interval_ms", Int64, "Optional server guidance for observing the Task after host answers are acknowledged.")
	Field(3, "requests", MapOf(String, HostRequest), "Outstanding host questions keyed by exact identifiers unique over this Task's lifetime. An empty object is valid.")
	Required("task_id", "requests")
})

// PendingExecution selects the next action for one unfinished tool invocation.
var PendingExecution = Type("ToolOperationPendingExecution", func() {
	Meta("struct:pkg:path", "tooloperations")
	Meta("type:generate:force")
	Description("Exactly one unfinished execution branch. Ordinary input continues the tool call; Task waiting reads an existing Task; Task input submits host answers to that Task.")
	OneOf("outcome", func() {
		Field(1, "input", PendingInput, "Ask the host to continue the original non-Task tool call.")
		Field(2, "task_wait", TaskWait, "Wait before observing the existing Task.")
		Field(3, "task_input", TaskInput, "Ask the host for outstanding Task answers.")
	})
	Required("outcome")
})
