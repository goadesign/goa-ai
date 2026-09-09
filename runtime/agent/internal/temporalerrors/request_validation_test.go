package temporalerrors

// Request rejection tests cross the SDK's real failure converter. They prove
// exact diagnostic preservation and terminal classification, not acceptance by
// an external Temporal server with its own failure-size limits.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/protobuf/proto"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
)

func TestRequestValidationTemporalRoundTrip(t *testing.T) {
	for _, size := range []int{0, 3072, 3073, 65_537} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			cause := errors.New(strings.Repeat("x", size) + "é\n\"\\")
			native := model.NewRequestValidationError(cause)
			assert.True(t, IsRequestValidation(native))
			outer := fmt.Errorf("prepare model request: %w", native)
			assert.True(t, IsRequestValidation(outer))
			app := roundTripCurrentFailure(t, Wrap(outer))
			assert.Equal(t, requestValidationApplicationType, app.Type())
			assert.Equal(t, outer.Error(), app.Message())
			assert.True(t, app.NonRetryable())
			assert.False(t, app.HasDetails())
			require.NoError(t, app.Unwrap())
			assert.True(t, IsRequestValidation(app))
			assert.Same(t, app, Wrap(app))
			_, provider := Provider(app)
			assert.False(t, provider)
			assert.False(t, IsOutputContract(app))
			wrapped := fmt.Errorf("child failed: %w", app)
			restored := roundTripCurrentFailure(t, Wrap(wrapped))
			assert.Equal(t, wrapped.Error(), restored.Message())
			assert.True(t, IsRequestValidation(restored))
			assert.True(t, restored.NonRetryable())
		})
	}
}

func TestRequestValidationRejectsMalformedSavedFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{"retryable", temporal.NewApplicationError("rejected", requestValidationApplicationType)},
		{"details", temporal.NewNonRetryableApplicationError("rejected", requestValidationApplicationType, nil, "unexpected")},
		{"cause", temporal.NewNonRetryableApplicationError("rejected", requestValidationApplicationType, errors.New("unexpected"))},
		{"invalid UTF-8", temporal.NewNonRetryableApplicationError(string([]byte{0xff}), requestValidationApplicationType, nil)},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.False(t, IsRequestValidation(test.err))
			app := roundTripCurrentFailure(t, Wrap(test.err))
			assert.Equal(t, currentInvalidApplicationType, app.Type())
			assert.True(t, app.NonRetryable())
			assert.Same(t, app, Wrap(app))
		})
	}
}

func TestRequestValidationPreservesOtherOwners(t *testing.T) {
	rejected := model.NewRequestValidationError(errors.New("invalid request"))
	provider := model.NewProviderError("provider", "complete", 400,
		model.ProviderErrorKindInvalidRequest, "invalid_request", "provider diagnostic", "request-id", false, rejected)
	output := planner.NewOutputContractError(rejected)
	for _, original := range []error{provider, output} {
		assert.False(t, IsRequestValidation(original))
		app := roundTripCurrentFailure(t, Wrap(original))
		assert.False(t, IsRequestValidation(app))
		assert.True(t, app.NonRetryable())
	}
	restoredProvider, ok := Provider(roundTripCurrentFailure(t, Wrap(provider)))
	require.True(t, ok)
	assert.Equal(t, provider.Message(), restoredProvider.Message())
	assert.Equal(t, 400, restoredProvider.HTTPStatus())
	assert.True(t, IsOutputContract(roundTripCurrentFailure(t, Wrap(output))))
	// The deliberately constructed outer owner wins; causes do not change it.
	assert.True(t, IsRequestValidation(roundTripCurrentFailure(t, Wrap(model.NewRequestValidationError(provider)))))
	assert.True(t, temporal.IsCanceledError(Wrap(context.Canceled)))
	joined := roundTripCurrentFailure(t, Wrap(errors.Join(context.Canceled, rejected)))
	assert.True(t, IsRequestValidation(joined))
	assert.True(t, joined.NonRetryable())
	unknown := roundTripCurrentFailure(t, Wrap(errors.New("transport failed")))
	assert.Equal(t, currentGenericApplicationType, unknown.Type())
	assert.False(t, unknown.NonRetryable())
	custom := temporal.NewApplicationErrorWithOptions("custom owns retryability", "custom",
		temporal.ApplicationErrorOptions{Cause: rejected})
	assert.False(t, IsRequestValidation(custom))
	customSaved := roundTripCurrentFailure(t, Wrap(custom))
	assert.Equal(t, currentGenericApplicationType, customSaved.Type())
	assert.False(t, customSaved.NonRetryable())
	// Wrap grants only a direct custom Temporal error the retry override;
	// an ordinary outer wrapper follows its recognized request-error cause.
	wrappedCustom := fmt.Errorf("outer context: %w", custom)
	assert.True(t, IsRequestValidation(wrappedCustom))
	assert.True(t, IsRequestValidation(roundTripCurrentFailure(t, Wrap(wrappedCustom))))
}

func TestRequestValidationDoesNotReinterpretHistoricalProviderFailures(t *testing.T) {
	provider := model.NewProviderError("provider", "complete", 400,
		model.ProviderErrorKindInvalidRequest, "invalid_request", "historical rejection", "request-id", false, nil)
	codec := temporal.GetDefaultFailureConverter()
	for _, saved := range []error{wrapHistorical(provider), Wrap(provider)} {
		before := codec.ErrorToFailure(saved)
		restored := codec.FailureToError(before)
		assert.False(t, IsRequestValidation(restored))
		assert.True(t, proto.Equal(before, codec.ErrorToFailure(Wrap(restored))))
		got, ok := Provider(restored)
		require.True(t, ok)
		assert.Equal(t, provider.Kind(), got.Kind())
		assert.False(t, got.Retryable())
	}
}
