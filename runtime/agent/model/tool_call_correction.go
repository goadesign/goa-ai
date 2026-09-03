// Package model derives fixed replacement guidance when the advertised tool
// input contract rejects model output. Guidance never includes rejected values
// or mutable tool metadata.
package model

const (
	advertisedToolInputCorrection    = "The previous tool call did not match its advertised input schema. Return a replacement tool call with valid arguments."
	malformedToolArgumentsCorrection = "The previous tool call arguments were not valid JSON. Return a replacement tool call whose arguments are one JSON object matching the advertised input schema."
)
