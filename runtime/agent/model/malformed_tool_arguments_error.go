// This file defines the error marker used when a provider returns tool
// arguments that are not valid JSON. Provider adapters add the marker at the
// exact JSON parsing failure so unrelated translation failures remain terminal.

package model

import (
	"fmt"

	"goa.design/goa-ai/runtime/agent/internal/correction"
	"goa.design/goa-ai/runtime/agent/tools"
)

type malformedToolArgumentsError struct {
	toolName tools.Ident
	cause    error
}

// NewMalformedToolArgumentsError marks a provider-returned tool argument value
// that is not valid JSON. name must be the canonical name resolved through the
// request's tool-name map, and cause must be the exact parsing failure. The
// receiving request contract checks that it advertised name before supplying
// correction guidance; the marker alone does not authorize recovery.
func NewMalformedToolArgumentsError(name tools.Ident, cause error) error {
	if name == "" {
		panic("model: malformed tool arguments require a tool name")
	}
	if cause == nil {
		panic("model: malformed tool arguments require a cause")
	}
	return &malformedToolArgumentsError{toolName: name, cause: cause}
}

// Error includes the original parsing diagnostic after the failure summary.
// OutputValidationError still presents its separate public summary.
func (e *malformedToolArgumentsError) Error() string {
	return "model tool arguments are not valid JSON: " + e.cause.Error()
}

// Unwrap preserves the provider adapter's private cause for in-process
// diagnostics.
func (e *malformedToolArgumentsError) Unwrap() error {
	return e.cause
}

// modelRecoveryCorrection names the input contract only after finding it in the
// receiving request. Unknown names and text exceeding the correction byte limit
// remain private diagnostics without model guidance.
func (e *malformedToolArgumentsError) modelRecoveryCorrection(validators map[tools.Ident]toolCallValidator) string {
	if _, advertised := validators[e.toolName]; !advertised {
		return ""
	}
	guidance := fmt.Sprintf("Input contract %q (diagnostic identifier, not a callable tool name):\n%s", e.toolName, malformedToolArgumentsCorrection)
	if len(guidance) > correction.MaxBytes {
		return ""
	}
	return guidance
}
