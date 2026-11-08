// Package toolregistry_test verifies the 11-row error mapping matrix for both
// in-process and remote tool execution paths.
package toolregistry_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry"
	goa "goa.design/goa/v3/pkg"
)

type customNamerError struct {
	name    string
	message string
}

func (e *customNamerError) Error() string {
	return e.message
}

func (e *customNamerError) GoaErrorName() string {
	return e.name
}

type customProviderError struct {
	failure *planner.ToolFailure
}

func (e *customProviderError) Error() string {
	return "custom failure provider error"
}

func (e *customProviderError) ToolFailure(tools.Ident) *planner.ToolFailure {
	return e.failure
}

func TestDeclaredErrorMapping11Rows(t *testing.T) {
	t.Parallel()

	providerFailure := &planner.ToolFailure{
		Kind:  planner.FailureUnavailable,
		Error: planner.NewToolError("service provider failure"),
		Recovery: planner.RecoveryDirective{
			Action: planner.RecoveryReplan,
		},
	}

	declaredSpecs := []toolregistry.DeclaredErrorSpec{
		{Name: "invalid_state"},
		{Name: "temp_error", Temporary: true},
		{Name: "invalid_input"},
		{Name: "rate_limited"},
		{Name: "timeout_by_design", Timeout: true},
		{Name: "fault_by_design", Fault: true},
	}

	tests := []struct {
		row         int
		name        string
		err         error
		code        string
		wantInKind  planner.FailureKind
		wantInAct   planner.RecoveryAction
		wantRemKind planner.FailureKind
		wantRemAct  planner.RecoveryAction
	}{
		{
			row:         1,
			name:        "eligible declared error reaching default (ServiceError invalid_state)",
			err:         goa.PermanentError("invalid_state", "state is invalid"),
			code:        "invalid_state",
			wantInKind:  planner.FailureDomainRejection,
			wantInAct:   planner.RecoveryReplan,
			wantRemKind: planner.FailureDomainRejection,
			wantRemAct:  planner.RecoveryReplan,
		},
		{
			row:         1,
			name:        "eligible declared error reaching default (custom type invalid_state wrapped)",
			err:         fmt.Errorf("wrapped step: %w", &customNamerError{name: "invalid_state", message: "custom invalid"}),
			code:        "execution_failed",
			wantInKind:  planner.FailureDomainRejection,
			wantInAct:   planner.RecoveryReplan,
			wantRemKind: planner.FailureDomainRejection,
			wantRemAct:  planner.RecoveryReplan,
		},
		{
			row:         2,
			name:        "eligible declared error with design Temporary()",
			err:         goa.PermanentError("temp_error", "temporarily unavailable"),
			code:        "temp_error",
			wantInKind:  planner.FailureUnavailable,
			wantInAct:   planner.RecoveryReplan,
			wantRemKind: planner.FailureUnavailable,
			wantRemAct:  planner.RecoveryReplan,
		},
		{
			row:         3,
			name:        "declared ServiceError named like reserved code (invalid_input)",
			err:         goa.PermanentError("invalid_input", "input is rejected"),
			code:        "invalid_input",
			wantInKind:  planner.FailureDomainRejection,
			wantInAct:   planner.RecoveryReplan,
			wantRemKind: planner.FailureDomainRejection,
			wantRemAct:  planner.RecoveryReplan,
		},
		{
			row:         3,
			name:        "declared ServiceError named like reserved code (rate_limited)",
			err:         goa.PermanentError("rate_limited", "slow down"),
			code:        "rate_limited",
			wantInKind:  planner.FailureDomainRejection,
			wantInAct:   planner.RecoveryReplan,
			wantRemKind: planner.FailureRateLimited,
			wantRemAct:  planner.RecoveryReplan,
		},
		{
			row:         4,
			name:        "declared custom type named like reserved code (invalid_input)",
			err:         &customNamerError{name: "invalid_input", message: "custom input error"},
			code:        "execution_failed",
			wantInKind:  planner.FailureDomainRejection,
			wantInAct:   planner.RecoveryReplan,
			wantRemKind: planner.FailureDomainRejection,
			wantRemAct:  planner.RecoveryReplan,
		},
		{
			row:         5,
			name:        "declared timeout by runtime flag (ServiceError.Timeout)",
			err:         goa.PermanentTimeoutError("invalid_state", "timed out"),
			code:        "timeout",
			wantInKind:  planner.FailureInternal,
			wantInAct:   planner.RecoveryFinish,
			wantRemKind: planner.FailureTimeout,
			wantRemAct:  planner.RecoveryFinish,
		},
		{
			row:         6,
			name:        "declared timeout by design Timeout() only (runtime flag unset)",
			err:         goa.PermanentError("timeout_by_design", "design timeout"),
			code:        "timeout_by_design",
			wantInKind:  planner.FailureInternal,
			wantInAct:   planner.RecoveryFinish,
			wantRemKind: planner.FailureInternal,
			wantRemAct:  planner.RecoveryFinish,
		},
		{
			row:         7,
			name:        "declared or undeclared error with field issues (MissingFieldError)",
			err:         goa.MissingFieldError("id", "req"),
			code:        "missing_field",
			wantInKind:  planner.FailureInternal,
			wantInAct:   planner.RecoveryFinish,
			wantRemKind: planner.FailureInvalidCall,
			wantRemAct:  planner.RecoveryCorrectCall,
		},
		{
			row:         8,
			name:        "merged error with field issues (fault + missing field)",
			err:         goa.MergeErrors(goa.Fault("server error"), goa.MissingFieldError("id", "req")),
			code:        "missing_field",
			wantInKind:  planner.FailureInternal,
			wantInAct:   planner.RecoveryFinish,
			wantRemKind: planner.FailureInvalidCall,
			wantRemAct:  planner.RecoveryCorrectCall,
		},
		{
			row:         9,
			name:        "merged error without field issues",
			err:         goa.MergeErrors(goa.PermanentError("invalid_state", "err1"), goa.PermanentError("invalid_state", "err2")),
			code:        "invalid_state",
			wantInKind:  planner.FailureInternal,
			wantInAct:   planner.RecoveryFinish,
			wantRemKind: planner.FailureInternal,
			wantRemAct:  planner.RecoveryFinish,
		},
		{
			row:         10,
			name:        "undeclared error (plain error)",
			err:         errors.New("unknown error"),
			code:        "execution_failed",
			wantInKind:  planner.FailureInternal,
			wantInAct:   planner.RecoveryFinish,
			wantRemKind: planner.FailureInternal,
			wantRemAct:  planner.RecoveryFinish,
		},
		{
			row:         10,
			name:        "undeclared error (undeclared ServiceError)",
			err:         goa.PermanentError("not_declared", "not in spec"),
			code:        "not_declared",
			wantInKind:  planner.FailureInternal,
			wantInAct:   planner.RecoveryFinish,
			wantRemKind: planner.FailureInternal,
			wantRemAct:  planner.RecoveryFinish,
		},
		{
			row:         10,
			name:        "runtime fault (ServiceError.Fault)",
			err:         goa.Fault("invalid_state: %s", "panic"),
			code:        "invalid_state",
			wantInKind:  planner.FailureInternal,
			wantInAct:   planner.RecoveryFinish,
			wantRemKind: planner.FailureInternal,
			wantRemAct:  planner.RecoveryFinish,
		},
		{
			row:         10,
			name:        "design fault (Fault() spec)",
			err:         goa.PermanentError("fault_by_design", "fault"),
			code:        "fault_by_design",
			wantInKind:  planner.FailureInternal,
			wantInAct:   planner.RecoveryFinish,
			wantRemKind: planner.FailureInternal,
			wantRemAct:  planner.RecoveryFinish,
		},
		{
			row:         10,
			name:        "multi-cause error join",
			err:         errors.Join(goa.PermanentError("invalid_state", "err1"), errors.New("err2")),
			code:        "execution_failed",
			wantInKind:  planner.FailureInternal,
			wantInAct:   planner.RecoveryFinish,
			wantRemKind: planner.FailureInternal,
			wantRemAct:  planner.RecoveryFinish,
		},
		{
			row:         11,
			name:        "ToolFailureProvider precedence",
			err:         &customProviderError{failure: providerFailure},
			code:        "execution_failed",
			wantInKind:  planner.FailureUnavailable,
			wantInAct:   planner.RecoveryReplan,
			wantRemKind: planner.FailureUnavailable,
			wantRemAct:  planner.RecoveryReplan,
		},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("Row%d_%s", tc.row, tc.name), func(t *testing.T) {
			// In-process path:
			// 1. Check ToolFailureProvider
			// 2. Check EligibleDeclaredToolFailure
			// 3. Fall back to internal finish
			var inFailure *planner.ToolFailure
			var provider planner.ToolFailureProvider
			if errors.As(tc.err, &provider) {
				inFailure = planner.CloneToolFailure(provider.ToolFailure("test_tool"))
			} else if eligible := toolregistry.EligibleDeclaredToolFailure(tc.err, declaredSpecs); eligible != nil {
				inFailure = eligible
			} else {
				inFailure = &planner.ToolFailure{
					Kind:  planner.FailureInternal,
					Error: planner.ToolErrorFromError(tc.err),
					Recovery: planner.RecoveryDirective{
						Action: planner.RecoveryFinish,
					},
				}
			}
			require.NotNil(t, inFailure)
			assert.Equal(t, tc.wantInKind, inFailure.Kind, "in-process kind mismatch")
			assert.Equal(t, tc.wantInAct, inFailure.Recovery.Action, "in-process action mismatch")

			// Remote path:
			msg := toolregistry.NewToolResultServiceErrorMessage(
				"token-1", "call-1", "test_tool", tc.code, tc.err, declaredSpecs...,
			)
			require.NotNil(t, msg.Error)
			require.NotNil(t, msg.Error.Failure)
			assert.Equal(t, tc.wantRemKind, msg.Error.Failure.Kind, "remote kind mismatch")
			assert.Equal(t, tc.wantRemAct, msg.Error.Failure.Recovery.Action, "remote action mismatch")
		})
	}
}
