// This file defines the execution outcome stored when no Run has started.
// Workflow owners supply accepted identity and a sourced time; storage selects
// the first terminal outcome without inventing history or a started timestamp.
package types

import (
	registrytypes "goa.design/goa-ai/registry/design/types"
	"goa.design/goa-ai/runtime/agent/run"
	. "goa.design/goa/v3/dsl"
)

var (
	// ExecutionFailure retains the existing canonical failure fields in generated transport.
	ExecutionFailure = Type("ExecutionFailure", func() {
		Meta("struct:pkg:path", "runterminal")
		Description("Canonical caller-safe failure and internal diagnostic evidence for an execution that failed.")
		ConvertTo(run.Failure{})
		CreateFrom(run.Failure{})
		Field(1, "message", String, "Caller-safe failure summary.", func() { Meta("struct:tag:json", "message,omitempty") })
		Field(2, "debug_message", String, "Internal diagnostic text; never render as the caller-safe message.", func() { Meta("struct:tag:json", "debug_message,omitempty") })
		Field(3, "provider", String, "Provider that produced the failure, when known.", func() { Meta("struct:tag:json", "provider,omitempty") })
		Field(4, "operation", String, "Provider operation that failed, when known.", func() { Meta("struct:tag:json", "operation,omitempty") })
		Field(5, "kind", String, "Canonical failure category.", func() { Meta("struct:tag:json", "kind,omitempty") })
		Field(6, "code", String, "Provider error code, when available.", func() { Meta("struct:tag:json", "code,omitempty") })
		Field(7, "http_status", Int, "Provider HTTP status, when available.", func() { Meta("struct:tag:json", "http_status,omitempty") })
		Field(8, "retryable", Boolean, "Whether another operation may succeed without changing its input.", func() { Meta("struct:tag:json", "retryable") })
		Required("retryable")
	})

	// UnstartedExecutionTerminal identifies one failed or canceled accepted execution.
	UnstartedExecutionTerminal = Type("UnstartedExecutionTerminal", func() {
		Meta("struct:pkg:path", "runterminal")
		Meta("type:generate:force")
		Meta("openapi:generate", "false")
		Description("The execution owner's failed or canceled outcome before a Run starts. The exact original invocation distinguishes roots, children and continuations that might propose the same destination Run ID. No success, suspension or started history is implied.")
		Field(1, "run_id", String, "Exact Run identifier accepted by the engine.", func() { Meta("struct:tag:json", "run_id"); MinLength(1) })
		Field(2, "subject", RunSubject, "Executor selected in the accepted engine request.", func() { Meta("struct:tag:json", "subject") })
		Field(3, "session_id", String, "Owning Session, omitted only for sessionless execution.", func() { Meta("struct:tag:json", "session_id,omitempty"); MinLength(1) })
		Field(4, "request_digest", Bytes, "Digest supplied by the engine for its actual accepted request.", func() { Meta("struct:tag:json", "request_digest"); MinLength(32); MaxLength(32) })
		Field(5, "origin", registrytypes.ExecutionInvocation, "Original root or issuing tool call retained by the preparation owner.", func() { Meta("struct:tag:json", "origin") })
		OneOf("time", func() {
			Meta("struct:tag:json", "time")
			TypeName("ExecutionTerminalTime")
			Field(6, "workflow_decision", String, "Frozen time when workflow code selected this outcome.", func() { Meta("struct:tag:json", "workflow_decision"); Format(FormatDateTime) })
			Field(7, "engine_closure", String, "Engine-observed close time used only for repair after external closure.", func() { Meta("struct:tag:json", "engine_closure"); Format(FormatDateTime) })
		})
		OneOf("outcome", func() {
			Meta("struct:tag:json", "outcome")
			TypeName("UnstartedExecutionOutcome")
			Field(8, "failed", ExecutionFailure, "Canonical failure decided by the execution owner.", func() { Meta("struct:tag:json", "failed") })
			Field(9, "canceled", String, "Accepted cancellation provenance.", func() {
				Meta("struct:tag:json", "canceled")
				MinLength(1)
			})
		})
		Required("run_id", "subject", "request_digest", "origin", "time", "outcome")
	})

	// StartedExecution confirms that the exact accepted request already has a Run.
	StartedExecution = Type("StartedExecution", func() {
		Meta("struct:pkg:path", "runterminal")
		Description("The exact accepted execution already has a Run whose current outcome remains authoritative.")
	})

	// UnstartedExecutionTerminalResult returns the transaction's selected existing fact.
	UnstartedExecutionTerminalResult = Type("UnstartedExecutionTerminalResult", func() {
		Meta("struct:pkg:path", "runterminal")
		Meta("type:generate:force")
		Meta("openapi:generate", "false")
		Description("Exactly one transaction result: newly stored outcome, exact replay, an earlier terminal winner, or a Run already started with the same accepted request. Started requires the caller to preserve that Run's current lifecycle.")
		OneOf("result", func() {
			Meta("struct:tag:json", "result")
			TypeName("UnstartedExecutionTerminalDecision")
			Field(1, "stored", UnstartedExecutionTerminal, "The newly stored terminal outcome.", func() { Meta("struct:tag:json", "stored") })
			Field(2, "replayed", UnstartedExecutionTerminal, "The unchanged outcome from an exact command replay.", func() { Meta("struct:tag:json", "replayed") })
			Field(3, "existing", UnstartedExecutionTerminal, "The earlier terminal outcome that remains authoritative.", func() { Meta("struct:tag:json", "existing") })
			Field(4, "started", StartedExecution, "The same accepted request already owns a started Run.", func() { Meta("struct:tag:json", "started") })
		})
		Required("result")
	})

	// PreparationProceed permits the bound invocation to resolve provider input.
	PreparationProceed = Type("PreparationProceed", func() {
		Meta("struct:pkg:path", "runterminal")
		Description("No saved stop prevents this bound invocation from preparing provider input.")
	})

	// PreparationDecision returns the owner's stop decision before provider work.
	PreparationDecision = Type("PreparationDecision", func() {
		Meta("struct:pkg:path", "runterminal")
		Meta("type:generate:force")
		Meta("openapi:generate", "false")
		Description("The preparation owner binds the original invocation and either permits provider work or returns the cancellation reason already saved for that invocation. A stop is a normal result and requires no provider call.")
		OneOf("state", func() {
			Meta("struct:tag:json", "state")
			TypeName("PreparationState")
			Field(1, "proceed", PreparationProceed, "The invocation may prepare its input.", func() { Meta("struct:tag:json", "proceed") })
			Field(2, "stop", String, "The saved cancellation reason that prevents preparation.", func() {
				Meta("struct:tag:json", "stop")
				MinLength(1)
			})
		})
		Required("state")
	})
)
