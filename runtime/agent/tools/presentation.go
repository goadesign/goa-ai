// Package tools selects generated model contracts for accepted execution policies.
// Selection never modifies schemas or rewrites model-authored arguments.
package tools

// ForTextOnly returns this tool's precomputed contract for ordinary messages.
// Tools without a generated contract must be excluded before this is called.
func (s ToolSpec) ForTextOnly() ToolSpec {
	if s.TextOnly == nil {
		panic("tool has no generated text-only contract")
	}
	s.Description = s.TextOnly.Description
	s.Search = s.TextOnly.Search
	s.Payload = s.TextOnly.Payload
	s.ResultReminder = s.TextOnly.ResultReminder
	s.ExecutionPayloadSchema = s.TextOnly.ExecutionSchema
	s.ExecutionPayloadCodec = s.TextOnly.ExecutionCodec
	return s
}
