package hooks

// These tests verify the run outcome seen by application hosts for explicit
// model-request rejections, before and after workflow failure serialization.

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.temporal.io/sdk/temporal"

	"goa.design/goa-ai/runtime/agent/internal/temporalerrors"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/run"
)

func TestRequestValidationRunFailureHasNoProviderFacts(t *testing.T) {
	cause := errors.New(strings.Repeat("local validation detail\né", 1000))
	native := model.NewRequestValidationError(cause)
	wrapped := fmt.Errorf("prepare: %w", native)
	codec := temporal.GetDefaultFailureConverter()
	saved := codec.FailureToError(codec.ErrorToFailure(temporalerrors.Wrap(wrapped)))
	for _, err := range []error{native, wrapped, saved} {
		event := NewRunCompletedEvent("run-1", "service.agent", "session-1", "failed", run.PhaseFailed, nil, err, nil)
		assert.Equal(t, &run.Failure{
			Message: PublicErrorModelRequest, DebugMessage: err.Error(), Kind: ErrorKindModelRequest,
			Retryable: false,
		}, event.Failure)
		assert.Contains(t, event.Failure.DebugMessage, cause.Error())
	}
}

func TestRequestValidationRunFailureUsesApplicationMessage(t *testing.T) {
	original := PublicErrorModelRequest
	t.Cleanup(func() { PublicErrorModelRequest = original })
	PublicErrorModelRequest = "This request needs an application correction."
	failure := RunFailureFromError(model.NewRequestValidationError(errors.New("original diagnostic")))
	assert.Equal(t, PublicErrorModelRequest, failure.Message)
	assert.Equal(t, "original diagnostic", failure.DebugMessage)
	assert.False(t, failure.Retryable)
}

func TestRequestValidationRunFailurePreservesDirectCustomOwner(t *testing.T) {
	rejected := model.NewRequestValidationError(errors.New("local request diagnostic"))
	custom := temporal.NewApplicationErrorWithOptions("application owns retries", "custom",
		temporal.ApplicationErrorOptions{Cause: rejected})
	failure := RunFailureFromError(custom)
	assert.NotEqual(t, ErrorKindModelRequest, failure.Kind)
	assert.True(t, failure.Retryable)
	assert.Equal(t, custom.Error(), failure.DebugMessage)
	// Match the writer's existing direct-only custom retry override.
	wrapped := fmt.Errorf("outer context: %w", custom)
	failure = RunFailureFromError(wrapped)
	assert.Equal(t, ErrorKindModelRequest, failure.Kind)
	assert.False(t, failure.Retryable)
	assert.Equal(t, wrapped.Error(), failure.DebugMessage)
}
