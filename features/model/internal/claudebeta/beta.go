// Package claudebeta owns Anthropic beta identifiers shared by provider
// adapters. Adapters attach these identifiers only when the corresponding
// canonical request fields are present.
package claudebeta

// ToolChanges enables tool_addition and tool_removal system message blocks.
const ToolChanges = "mid-conversation-tool-changes-2026-07-01"

const (
	// ToolExamples enables provider-native input_examples on custom tools.
	ToolExamples = "tool-examples-2025-10-29"
)
