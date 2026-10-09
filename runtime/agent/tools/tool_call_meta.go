// Package tools defines the shared metadata supplied with each tool call.
// Generated providers and local executors pass these values to injection code
// without requiring the agent engine or a model client.
package tools

type (
	// ToolCallMeta carries server-supplied identifiers and labels for one tool
	// invocation. Generated injection functions use it to fill fields that the
	// model does not supply. JSON field names preserve saved execution records.
	ToolCallMeta struct {
		// TextOnly disables UI interaction for this call when the run requires it.
		TextOnly bool `json:",omitempty"` //nolint:tagliatelle // Saved execution records retain Go field names.

		// RunID identifies the workflow run that owns this call and stays the
		// same when the call is retried.
		RunID string

		// SessionID groups related runs in one conversation or interaction.
		SessionID string

		// TurnID identifies the conversation turn that produced this call.
		TurnID string

		// ToolCallID identifies this invocation for events and related calls.
		ToolCallID string

		// ParentToolCallID identifies the call that started this child call.
		ParentToolCallID string

		// Labels contains the run labels and values added by the runtime for
		// this call. Changing this copy does not change another call's labels.
		// For a tool that runs when the agent stops, goa-ai.finalization_reason
		// contains the exact reason the agent stopped. Ordinary calls do not
		// receive that reserved label.
		Labels map[string]string
	}
)
