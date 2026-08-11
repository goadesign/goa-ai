// Package planner tool failures classify failed tool execution and define the
// only legal next planner transition.
package planner

import (
	"bytes"
	"encoding/json"
	"fmt"

	"goa.design/goa-ai/runtime/agent/rawjson"
	toolerrors "goa.design/goa-ai/runtime/agent/toolerrors"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// ToolError represents a structured tool failure and is an alias to the
	// runtime toolerrors type.
	ToolError = toolerrors.ToolError

	// FailureKind classifies why a tool failed independently from what the
	// planner may do next.
	FailureKind string

	// RecoveryAction defines the next planner transition after a tool failure.
	RecoveryAction string

	// RecoveryDirective is the runtime-enforced recovery contract for one tool
	// failure. Correction details are populated only for CorrectCall.
	RecoveryDirective struct {
		// Action selects the next planner transition.
		Action RecoveryAction `json:"action"`

		// Issues are generated-codec field issues explaining how to correct the
		// failed call.
		Issues []*tools.FieldIssue `json:"issues,omitempty"`

		// PriorInput is the exact canonical JSON payload rejected by the tool.
		PriorInput rawjson.Message `json:"prior_input,omitempty"`

		// ExampleJSON is a canonical schema-compliant payload example.
		ExampleJSON rawjson.Message `json:"example_json,omitempty"`
	}

	// ToolFailure is the canonical failed-tool result. Kind classifies the
	// failure, Error preserves its causal chain, and Recovery determines the
	// only legal next planner transition.
	ToolFailure struct {
		// Kind classifies the failure for policy, observability, and UI.
		Kind FailureKind `json:"kind"`

		// Error is the structured tool error.
		Error *ToolError `json:"error"`

		// Recovery defines the legal next planner transition.
		Recovery RecoveryDirective `json:"recovery"`
	}
)

const (
	// FailureInvalidCall means model-authored arguments or tool selection did
	// not satisfy the advertised contract.
	FailureInvalidCall FailureKind = "invalid_call"
	// FailureDomainRejection means the tool accepted the payload shape but
	// rejected its requested domain semantics.
	FailureDomainRejection FailureKind = "domain_rejection"
	// FailureUnavailable means the tool or its provider is unavailable.
	FailureUnavailable FailureKind = "unavailable"
	// FailureRateLimited means the provider exhausted its local throttling retry.
	FailureRateLimited FailureKind = "rate_limited"
	// FailureTimeout means execution exceeded its deadline or has uncertain
	// completion state.
	FailureTimeout FailureKind = "timeout"
	// FailureMalformedResult means a provider result violated its registered
	// result contract.
	FailureMalformedResult FailureKind = "malformed_result"
	// FailureInternal means an internal invariant or implementation failed.
	FailureInternal FailureKind = "internal"

	// RecoveryCorrectCall keeps the failed tool available and supplies its
	// structured correction evidence to the next planner turn. The planner may
	// retry, combine work, choose another advertised action, await input, or
	// finish from the evidence already collected.
	RecoveryCorrectCall RecoveryAction = "correct_call"
	// RecoveryReplan makes the failed tool unavailable on the next planner turn.
	// The planner may choose another advertised capability, await input, or
	// finish from the evidence already collected.
	RecoveryReplan RecoveryAction = "replan"
	// RecoveryFinish forbids further tool execution and requires final synthesis
	// from evidence already collected.
	RecoveryFinish RecoveryAction = "finish"
)

// NewToolError constructs a ToolError with the provided message.
func NewToolError(message string) *ToolError {
	return toolerrors.New(message)
}

// NewToolErrorWithCause wraps an existing error with a ToolError message.
func NewToolErrorWithCause(message string, cause error) *ToolError {
	return toolerrors.NewWithCause(message, cause)
}

// ToolErrorFromError converts an arbitrary error into a ToolError chain.
func ToolErrorFromError(err error) *ToolError {
	return toolerrors.FromError(err)
}

// ToolErrorf formats according to fmt conventions and returns a ToolError.
func ToolErrorf(format string, args ...any) *ToolError {
	return toolerrors.Errorf(format, args...)
}

// ValidateToolFailure enforces the canonical failure classification, error,
// recovery, and correction-data contract at every ingress boundary. Correct-call
// prior input is optional here because registry providers do not own it; the
// executor attaches the rejected call before the runtime requires planner-ready
// correction context.
func ValidateToolFailure(failure *ToolFailure) error {
	if failure == nil {
		return fmt.Errorf("tool failure is required")
	}
	if err := toolerrors.Validate(failure.Error); err != nil {
		return fmt.Errorf("failure error is invalid: %w", err)
	}
	switch failure.Kind {
	case FailureInvalidCall,
		FailureDomainRejection,
		FailureUnavailable,
		FailureRateLimited,
		FailureTimeout,
		FailureMalformedResult,
		FailureInternal:
	default:
		return fmt.Errorf("unknown failure kind %q", failure.Kind)
	}
	switch failure.Recovery.Action {
	case RecoveryCorrectCall:
		if failure.Kind != FailureInvalidCall &&
			failure.Kind != FailureDomainRejection {
			return fmt.Errorf("failure kind %q cannot require same-tool correction", failure.Kind)
		}
		if len(failure.Recovery.Issues) > 0 {
			if err := tools.ValidateFieldIssues(failure.Recovery.Issues); err != nil {
				return fmt.Errorf("correct-call recovery issues are invalid: %w", err)
			}
		}
		if len(failure.Recovery.PriorInput) > 0 {
			if err := validateToolPayload(failure.Recovery.PriorInput); err != nil {
				return fmt.Errorf("correct-call recovery prior input is invalid: %w", err)
			}
		}
		if len(failure.Recovery.ExampleJSON) > 0 {
			if err := validateToolPayload(failure.Recovery.ExampleJSON); err != nil {
				return fmt.Errorf("correct-call recovery example is invalid: %w", err)
			}
		}
	case RecoveryReplan, RecoveryFinish:
		if len(failure.Recovery.Issues) > 0 ||
			len(failure.Recovery.PriorInput) > 0 ||
			len(failure.Recovery.ExampleJSON) > 0 {
			return fmt.Errorf("recovery %q cannot carry correction data", failure.Recovery.Action)
		}
	default:
		return fmt.Errorf("unknown recovery action %q", failure.Recovery.Action)
	}
	return nil
}

// CloneToolFailure deep-copies a failed-tool value across workflow,
// persistence, and replay ownership boundaries.
func CloneToolFailure(in *ToolFailure) *ToolFailure {
	if in == nil {
		return nil
	}
	return &ToolFailure{
		Kind:  in.Kind,
		Error: toolerrors.Clone(in.Error),
		Recovery: RecoveryDirective{
			Action:      in.Recovery.Action,
			Issues:      tools.CloneFieldIssues(in.Recovery.Issues),
			PriorInput:  append(rawjson.Message(nil), in.Recovery.PriorInput...),
			ExampleJSON: append(rawjson.Message(nil), in.Recovery.ExampleJSON...),
		},
	}
}

// AllowsToolTurn reports whether recovery permits another planner tool turn.
func (f *ToolFailure) AllowsToolTurn() bool {
	if f == nil {
		return false
	}
	switch f.Recovery.Action {
	case RecoveryCorrectCall, RecoveryReplan:
		return true
	case RecoveryFinish:
		return false
	default:
		panic("planner: unknown recovery action " + f.Recovery.Action)
	}
}

// validateToolPayload verifies optional correction context is a non-empty JSON
// object when present.
func validateToolPayload(payload rawjson.Message) error {
	data := bytes.TrimSpace(payload)
	if len(data) == 0 {
		return fmt.Errorf("payload is empty")
	}
	if !json.Valid(data) {
		return fmt.Errorf("payload is not valid JSON")
	}
	if data[0] != '{' {
		return fmt.Errorf("payload must be a JSON object")
	}
	return nil
}
