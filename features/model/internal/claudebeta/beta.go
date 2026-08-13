// Package claudebeta owns Anthropic beta identifiers shared by provider
// adapters. Adapters attach these identifiers only when the corresponding
// canonical request fields are present.
package claudebeta

const (
	// StructuredOutputs enables grammar-constrained JSON and strict tool use on
	// Claude integrations that still gate the capability behind its beta name.
	StructuredOutputs = "structured-outputs-2025-11-13"

	// ToolExamples enables provider-native input_examples on custom tools.
	ToolExamples = "tool-examples-2025-10-29"
)
