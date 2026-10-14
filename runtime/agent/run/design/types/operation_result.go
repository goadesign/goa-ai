// This file declares the caller-safe result of the selected operation. The
// execution owner stores it with the terminal run record. Observer messages,
// private sidecars, recovery instructions and provider error causes stay private.
package types

import (
	agenttypes "goa.design/goa-ai/runtime/agent/design/types"
	"goa.design/goa-ai/runtime/agent/planner"
	contenttypes "goa.design/goa-ai/runtime/content/design/types"
	. "goa.design/goa/v3/dsl"
)

var (
	// OperationResult preserves the selected operation's data and ordered content.
	OperationResult = Type("OperationResult", func() {
		Meta("struct:pkg:path", "runoutput")
		Meta("type:generate:force")
		Meta("openapi:generate", "false")
		Description("The exact result produced by the selected operation and stored with the run's completion. An execution that fails before producing a tool result has no operation result and retains its lifecycle failure instead.")
		OneOf("outcome", func() {
			TypeName("OperationOutcome")
			Field(2, "success", OperationSuccess, "The operation completed with its declared result value or explicit absence of a result value.")
			Field(3, "failure", OperationFailure, "The selected operation returned a failure.")
		})
		Required("outcome")
	})

	// OperationSuccess distinguishes a declared no-result operation from JSON data.
	OperationSuccess = Type("OperationSuccess", func() {
		Meta("struct:pkg:path", "runoutput")
		OneOf("value", func() {
			TypeName("OperationValue")
			Field(1, "empty", EmptyOperationResult, "The selected operation declares no result value.")
			Field(2, "json", Bytes, "Exact canonical JSON produced under the selected operation's result schema, including JSON null or empty objects when allowed.", func() {
				Meta("struct:field:type", "json.RawMessage", "encoding/json")
			})
		})
		Field(3, "bounds", agenttypes.ResultBounds, "Original coverage data supplied by the operation.")
		Field(4, "blocks", ArrayOfRequired(contenttypes.Block), "Ordered text, media and resource content returned by the successful operation.")
		Required("value")
	})

	// EmptyOperationResult explicitly records successful absence of result data.
	EmptyOperationResult = Type("EmptyOperationResult", func() {
		Meta("struct:pkg:path", "runoutput")
		Description("The selected operation declares no result data. This differs from a JSON null, empty object, empty array or empty string.")
	})

	// OperationFailure exposes the tool's failure category and safe summary.
	OperationFailure = Type("OperationFailure", func() {
		Meta("struct:pkg:path", "runoutput")
		Field(1, "kind", String, "Existing runtime category of the operation failure.", func() {
			Enum(string(planner.FailureInvalidCall), string(planner.FailureDomainRejection), string(planner.FailureUnavailable), string(planner.FailureRateLimited), string(planner.FailureTimeout), string(planner.FailureMalformedResult), string(planner.FailureInternal))
		})
		Field(2, "message", String, "Caller-safe failure summary from the operation.")
		Field(3, "blocks", ArrayOfRequired(contenttypes.Block), "Ordered text, media and resource content returned with the failure.")
		Required("kind", "message")
	})
)
