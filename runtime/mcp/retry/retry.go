// Package retry defines shared types and helpers for producing standardized
// retryable errors and compact repair prompts used across generated clients.
//
// Generated clients wrap transport errors for invalid parameters into
// RetryableError with a Prompt intended for LLM-driven correction. The LLM is
// expected to redo the same operation (e.g., JSON-RPC tools/call for MCP tools)
// with valid parameters based on the prompt constraints.
package retry

import "fmt"

// promptTemplate is the canonical format for repair prompts consumed by LLMs.
// Keep this concise and deterministic. The schema (when provided) is injected
// above the Error line. The LLM must return only the corrected params JSON,
// which will be used to retry the operation.
const promptTemplate = `
Operation: %s
%sError: %s
Redo the operation now with valid parameters.
Use only valid schema fields and ensure required fields and types/enums are valid.
Example params: %s`

// RetryableError is returned by clients when the server reports invalid
// parameters and a structured repair prompt is available.
//
// Prompt is human-readable and designed for LLM-driven correction. Typical flow:
//  1. Present Prompt to the LLM
//  2. Capture the JSON-only corrected params
//  3. Decode into the operation payload type
//  4. Retry the same operation
type RetryableError struct {
	Prompt string
	Cause  error
}

// Error returns the error message.
func (e *RetryableError) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause == nil {
		return e.Prompt
	}
	return fmt.Sprintf("%s: %v", e.Prompt, e.Cause)
}

// BuildRepairPrompt constructs a deterministic, compact repair instruction.
// schema is an optional compact JSON schema excerpt; exampleJSON is a minimal
// valid example of the params payload.
func BuildRepairPrompt(op string, errMsg string, exampleJSON string, schema string) string {
	schemaPart := ""
	if schema != "" {
		schemaPart = "Schema: " + schema + "\n"
	}
	return fmt.Sprintf(promptTemplate, op, schemaPart, errMsg, exampleJSON)
}
