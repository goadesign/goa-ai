// Package model defines the provider-neutral model request, response, and
// streaming contracts. This file names the closed reasons that completed
// provider output can fail a request contract without retaining that output.
package model

import "errors"

type (
	// OutputValidationKind identifies the first request-contract rule that
	// rejects model output. Its fixed values are safe to persist and aggregate:
	// they contain no response text, provider error text, tool names, arguments,
	// identifiers, or schema paths. A kind diagnoses the failed boundary; it
	// never authorizes a retry or model recovery by itself.
	OutputValidationKind string

	// outputValidationFailure carries a category between the exact check that
	// rejects output and the boundary that constructs OutputValidationError.
	outputValidationFailure struct {
		kind  OutputValidationKind
		cause error
	}
)

const (
	// OutputValidationResponseShape means the provider-neutral response or
	// chunk did not have a valid canonical structure.
	OutputValidationResponseShape OutputValidationKind = "response_shape"

	// OutputValidationOutputBounds means provider output exceeded a byte,
	// nesting, collection, or retained-value limit.
	OutputValidationOutputBounds OutputValidationKind = "output_bounds"

	// OutputValidationToolIdentity means a tool call lacked a stable identity,
	// named an unadvertised tool, or could not be mapped to an advertised tool.
	OutputValidationToolIdentity OutputValidationKind = "tool_identity"

	// OutputValidationToolArguments means tool arguments were not canonical JSON
	// or did not satisfy the advertised tool input contract.
	OutputValidationToolArguments OutputValidationKind = "tool_arguments"

	// OutputValidationToolChoice means the returned tool-call presence, count,
	// or selection violated the request's tool-choice rule.
	OutputValidationToolChoice OutputValidationKind = "tool_choice"

	// OutputValidationStructuredOutput means a requested structured completion
	// was missing, malformed, or failed its response schema or generated codec.
	OutputValidationStructuredOutput OutputValidationKind = "structured_output"

	// OutputValidationStreamProtocol means streamed events were out of order or
	// did not reconcile with the provider's complete response.
	OutputValidationStreamProtocol OutputValidationKind = "stream_protocol"

	// OutputValidationUsage means token accounting was malformed or inconsistent
	// across streamed events and the complete response.
	OutputValidationUsage OutputValidationKind = "usage"
)

// validOutputValidationKind reports whether kind is one of the closed public
// values that a new rejection may expose.
func validOutputValidationKind(kind OutputValidationKind) bool {
	switch kind {
	case OutputValidationResponseShape,
		OutputValidationOutputBounds,
		OutputValidationToolIdentity,
		OutputValidationToolArguments,
		OutputValidationToolChoice,
		OutputValidationStructuredOutput,
		OutputValidationStreamProtocol,
		OutputValidationUsage:
		return true
	default:
		return false
	}
}

// Error preserves the private validation cause for in-process error matching.
func (e *outputValidationFailure) Error() string {
	return e.cause.Error()
}

// Unwrap preserves the original cause without exposing it through the public
// OutputValidationError summary.
func (e *outputValidationFailure) Unwrap() error {
	return e.cause
}

// classifyOutputValidation marks the check that first rejected output.
func classifyOutputValidation(kind OutputValidationKind, cause error) error {
	return &outputValidationFailure{kind: kind, cause: cause}
}

// classifiedOutputValidation returns the category attached by the first
// rejecting check.
func classifiedOutputValidation(err error) (OutputValidationKind, bool) {
	var failure *outputValidationFailure
	if !errors.As(err, &failure) {
		return "", false
	}
	return failure.kind, true
}
