package temporalerrors

// These tests preserve the complete native timeout through Temporal's real
// failure converter and keep independent or previously saved failures opaque.

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	failurepb "go.temporal.io/api/failure/v1"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/protobuf/proto"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
)

func TestNativeTimeoutRetainsCompleteSDKFailure(t *testing.T) {
	codec := temporal.GetDefaultFailureConverter()
	activity := &failurepb.Failure{
		Message: "activity timed out",
		FailureInfo: &failurepb.Failure_ActivityFailureInfo{ActivityFailureInfo: &failurepb.ActivityFailureInfo{
			ScheduledEventId: 12, StartedEventId: 13,
			ActivityType: &commonpb.ActivityType{Name: "lookup.resume"},
			ActivityId:   "resume-2", Identity: "lookup-worker",
			RetryState: enumspb.RETRY_STATE_MAXIMUM_ATTEMPTS_REACHED,
		}},
		Cause: &failurepb.Failure{
			Message: "activity StartToClose timeout",
			FailureInfo: &failurepb.Failure_TimeoutFailureInfo{TimeoutFailureInfo: &failurepb.TimeoutFailureInfo{
				TimeoutType: enumspb.TIMEOUT_TYPE_START_TO_CLOSE,
			}},
		},
	}
	child := &failurepb.Failure{
		Message: "child workflow failed",
		FailureInfo: &failurepb.Failure_ChildWorkflowExecutionFailureInfo{ChildWorkflowExecutionFailureInfo: &failurepb.ChildWorkflowExecutionFailureInfo{
			Namespace: "test", WorkflowExecution: &commonpb.WorkflowExecution{WorkflowId: "lookup", RunId: "attempt-1"},
			WorkflowType: &commonpb.WorkflowType{Name: "lookup.workflow"},
		}},
		Cause: activity,
	}
	for _, saved := range []*failurepb.Failure{activity.Cause, activity, child} {
		err := codec.FailureToError(saved)
		require.True(t, IsNativeTimeout(err))
		assert.Same(t, err, Wrap(err))
		encoded := codec.ErrorToFailure(Wrap(err))
		assert.True(t, proto.Equal(saved, encoded))
		restored := codec.FailureToError(encoded)
		require.EqualError(t, restored, err.Error())
		assert.True(t, IsNativeTimeout(restored))
	}
}

func TestNativeTimeoutDoesNotOverrideAnotherFailure(t *testing.T) {
	timeout := temporal.NewTimeoutError(enumspb.TIMEOUT_TYPE_START_TO_CLOSE, nil)
	custom := temporal.NewApplicationErrorWithCause("write outcome unknown", "custom", timeout)
	provider := model.NewProviderError("provider", "complete", 503, model.ProviderErrorKindUnavailable, "unavailable", "exact reason", "request", true, timeout)
	for _, original := range []error{
		errors.Join(timeout, errors.New("independent failure")),
		errors.Join(errors.New("independent failure"), timeout),
		custom,
		fmt.Errorf("child context: %w", custom),
		fmt.Errorf("unclassified context: %w", timeout),
		temporal.NewTimeoutError(enumspb.TIMEOUT_TYPE_START_TO_CLOSE, errors.Join(timeout, errors.New("independent previous failure"))),
		temporal.NewTimeoutError(enumspb.TIMEOUT_TYPE_START_TO_CLOSE, custom),
		planner.NewOutputContractError(timeout),
		model.NewRequestValidationError(timeout),
		provider,
	} {
		t.Run(original.Error(), func(t *testing.T) {
			assert.False(t, IsNativeTimeout(original))
			saved := Wrap(original)
			assert.False(t, IsNativeTimeout(saved))
			var app *temporal.ApplicationError
			require.ErrorAs(t, saved, &app)
			assert.NotEmpty(t, app.Message())
		})
	}
	assert.True(t, IsOutputContract(Wrap(planner.NewOutputContractError(timeout))))
	assert.True(t, IsRequestValidation(Wrap(model.NewRequestValidationError(timeout))))
	got, ok := Provider(Wrap(provider))
	require.True(t, ok)
	assert.Equal(t, provider.Message(), got.Message())
}

func TestSavedGenericTimeoutIsNotReinterpreted(t *testing.T) {
	// Freeze the already-written wire details rather than constructing them
	// through today's Wrap, which now recognizes native timeouts.
	saved := &failurepb.Failure{
		Message: "activity StartToClose timeout",
		FailureInfo: &failurepb.Failure_ApplicationFailureInfo{ApplicationFailureInfo: &failurepb.ApplicationFailureInfo{
			Type: "goa_ai.generic_error.v3",
			Details: &commonpb.Payloads{Payloads: []*commonpb.Payload{{
				Metadata: map[string][]byte{"encoding": []byte("json/plain")},
				Data:     []byte(`{"OriginalType":"","Message":"activity StartToClose timeout","Retryable":true}`),
			}}},
		}},
	}
	codec := temporal.GetDefaultFailureConverter()
	restored := codec.FailureToError(saved)
	assert.False(t, IsNativeTimeout(restored))
	assert.Same(t, restored, Wrap(restored))
	assert.True(t, proto.Equal(saved, codec.ErrorToFailure(Wrap(restored))))
}
