// Package types describes the runtime's saved operations on one unfinished tool
// invocation. The registry and private value generator share these definitions;
// model-facing tool arguments never contain operation identity or host answers.
package types

import . "goa.design/goa/v3/dsl"

// InputContinuation stores the service's state and the host's accepted answers.
var InputContinuation = Type("InputContinuation", func() {
	Meta("struct:pkg:path", "tooloperations")
	Description("Exact service state and accepted host answers for continuing the original tool invocation.")
	Field(1, "state", String, "Opaque state returned by the service, including an explicitly empty string.")
	Field(2, "responses", MapOf(String, Bytes), "Host answer JSON bytes keyed by the exact server request identifiers.")
})

// TaskAnswers stores the answers submitted to one server-owned Task.
var TaskAnswers = Type("TaskAnswers", func() {
	Meta("struct:pkg:path", "tooloperations")
	Description("Accepted host answers for one existing Task. An empty answer object is valid and does not select a different operation.")
	Field(1, "task_id", String, "Exact server-owned Task identifier, including an empty string.")
	Field(2, "responses", MapOf(String, Bytes), "Host answer JSON bytes for outstanding Task input requests.")
	Required("task_id", "responses")
})

// ExecutionContinuation selects one later operation without changing tool arguments.
var ExecutionContinuation = Type("ExecutionContinuation", func() {
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
