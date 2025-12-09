package tools

import "strings"

// Ident is the strong type for globally unique tool identifiers. Tool IDs are
// canonical strings of the form "toolset.tool" (e.g., "helpers.search",
// "weather.get_forecast"). This format is consistent across all goa-ai surfaces:
// planners, hooks, streams, policies, and generated code.
//
// Using Ident instead of raw strings provides:
//   - Type safety: prevents accidental mixing with session IDs, run IDs, etc.
//   - Documentation: call sites show intent (tool vs. arbitrary string)
//   - Provider agnosticism: platform adapters handle format translation
//
// Generated code produces typed Ident constants for each tool:
//
//	// In generated specs package
//	const ToolSearch tools.Ident = "helpers.search"
//
//	// Usage
//	if call.Name == specs.ToolSearch { ... }
//
// Platform adapters (Bedrock, OpenAI) are responsible for translating between
// canonical Idents and platform-specific formats. For example, Bedrock requires
// underscores instead of dots, so the adapter maps "helpers.search" to
// "helpers_search" on outbound calls and reverses the mapping on inbound tool
// calls. User code never needs to handle these transformations.
type Ident string

// String returns the string representation of the identifier. Useful for
// logging, error messages, and JSON serialization where the raw string is needed.
func (id Ident) String() string {
	return string(id)
}

// Toolset returns the toolset component of the identifier (the part before
// the last dot). For "helpers.search", this returns "helpers". Returns an
// empty string if the identifier has no dot separator.
func (id Ident) Toolset() string {
	parts := strings.Split(string(id), ".")
	if len(parts) < 2 {
		return ""
	}
	return parts[0]
}

// Tool returns the tool name component of the identifier (the part after
// the last dot). For "helpers.search", this returns "search". Returns an
// empty string if the identifier is empty.
func (id Ident) Tool() string {
	parts := strings.Split(string(id), ".")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}
