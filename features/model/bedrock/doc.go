// Package bedrock wires Amazon Bedrock model clients into goa-ai planners.
//
// New uses Bedrock Converse for models whose canonical contract is represented
// by that API. NewAnthropic uses Anthropic Messages over Bedrock InvokeModel for
// Claude deployments that need Anthropic-native tools, examples, thinking, and
// structured output on every turn. Both constructors return validated model
// clients and preserve exact token counting through the counter appropriate to
// their request representation.
package bedrock
