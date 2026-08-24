package temporalerrors

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/protobuf/proto"

	"goa.design/goa-ai/runtime/agent/internal/outputcontract"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
)

type (
	singleCycleError struct {
		next error
	}

	joinedCycleError struct {
		children []error
	}

	typedNilError struct{}

	manyChildrenError struct {
		children []error
	}

	panickingError struct{}
)

func TestWrapPreservesCancellation(t *testing.T) {
	tests := []error{
		context.Canceled,
		fmt.Errorf("activity stopped: %w", context.Canceled),
		temporal.NewCanceledError("activity stopped"),
		errors.Join(context.Canceled, fmt.Errorf("nested: %w", context.Canceled)),
	}
	for _, err := range tests {
		require.True(t, temporal.IsCanceledError(Wrap(err)))
	}
}

func (*singleCycleError) Error() string {
	return "single cycle"
}

func (e *singleCycleError) Unwrap() error {
	return e.next
}

func (*joinedCycleError) Error() string {
	return "joined cycle"
}

func (e *joinedCycleError) Unwrap() []error {
	return e.children
}

func (*typedNilError) Error() string {
	return "typed nil"
}

func (*manyChildrenError) Error() string {
	return "many children"
}

func (e *manyChildrenError) Unwrap() []error {
	return e.children
}

func (*panickingError) Error() string {
	panic("broken error")
}

func TestErrorGraphsRejectTypedNilExcessiveFanoutAndPanickingText(t *testing.T) {
	var typedNil *typedNilError
	require.False(t, CancellationOnly(typedNil))
	require.NotPanics(t, func() {
		wrapped := Wrap(typedNil)
		var appErr *temporal.ApplicationError
		require.ErrorAs(t, wrapped, &appErr)
		require.Equal(t, invalidReservedApplicationType, appErr.Type())
	})

	children := make([]error, maxClassificationChildren+1)
	for i := range children {
		children[i] = context.Canceled
	}
	fanout := &manyChildrenError{children: children}
	require.False(t, CancellationOnly(fanout))
	wrapped := Wrap(fanout)
	var appErr *temporal.ApplicationError
	require.ErrorAs(t, wrapped, &appErr)
	require.Equal(t, invalidReservedApplicationType, appErr.Type())

	require.NotPanics(t, func() {
		wrapped := Wrap(outputcontract.NewWithOrigin(&panickingError{}, outputcontract.OriginPlanner))
		require.True(t, IsOutputContract(wrapped))
	})
}

func TestCancellationOnlyBoundsAndClassifiesCompleteErrorGraph(t *testing.T) {
	single := &singleCycleError{}
	single.next = single

	require.True(t, CancellationOnly(fmt.Errorf("wrapped: %w", context.Canceled)))
	require.True(t, CancellationOnly(errors.Join(context.Canceled, temporal.NewCanceledError("ignored"))))
	require.False(t, CancellationOnly(errors.Join(context.Canceled, errors.New("failed"))))
	require.False(t, CancellationOnly(single))
}

func TestWrapReclassifiesNestedOutputContractError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		stored error
	}{
		{
			name:   "native cause remains available",
			stored: Wrap(planner.NewOutputContractError(errors.New("invalid reply"))),
		},
		{
			name: "only Temporal type remains available",
			stored: temporal.NewNonRetryableApplicationError(
				"invalid reply",
				outputContractErrorApplicationType,
				nil,
				outputContractErrorDetails{Origin: string(planner.OutputContractOriginPlanner)},
			),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			nested := fmt.Errorf("child workflow failed: %w", test.stored)
			got := Wrap(nested)

			require.NotSame(t, nested, got)
			var appErr *temporal.ApplicationError
			require.ErrorAs(t, got, &appErr)
			require.Equal(t, outputContractErrorApplicationType, appErr.Type())
			require.True(t, appErr.NonRetryable())
		})
	}
}

func TestWrapPreservesToolOutputContractOrigin(t *testing.T) {
	wrapped := Wrap(outputcontract.NewWithOrigin(errors.New("tool result is too large"), outputcontract.OriginTool))

	require.True(t, IsOutputContract(wrapped))
	require.Equal(t, planner.OutputContractOriginTool, OutputContractOrigin(wrapped))
}

func TestOutputContractApplicationTypeIsNeutralWithoutLegacyDecoding(t *testing.T) {
	t.Parallel()

	wrapped := Wrap(planner.NewOutputContractError(errors.New("invalid reply")))
	var appErr *temporal.ApplicationError
	require.ErrorAs(t, wrapped, &appErr)
	require.Equal(t, "goa_ai.output_contract_error", appErr.Type())

	legacy := temporal.NewNonRetryableApplicationError(
		"invalid reply",
		"goa_ai.planner_output_error",
		nil,
		outputContractErrorDetails{Origin: string(planner.OutputContractOriginPlanner)},
	)
	reclassified := Wrap(legacy)
	require.ErrorAs(t, reclassified, &appErr)
	require.Equal(t, genericErrorApplicationType, appErr.Type())
	require.True(t, appErr.NonRetryable())
	require.False(t, IsOutputContract(reclassified))
}

func TestWrapReclassifiesNestedProviderError(t *testing.T) {
	t.Parallel()

	for _, retryable := range []bool{false, true} {
		t.Run(fmt.Sprintf("retryable=%t", retryable), func(t *testing.T) {
			t.Parallel()

			providerErr := model.NewProviderError(
				"anthropic",
				"complete",
				503,
				model.ProviderErrorKindUnavailable,
				"service_unavailable",
				"provider unavailable",
				"request-1",
				retryable,
				nil,
			)
			stored := Wrap(providerErr)
			nested := fmt.Errorf("activity failed: %w", stored)

			got := Wrap(nested)

			require.NotSame(t, nested, got)
			var appErr *temporal.ApplicationError
			require.ErrorAs(t, got, &appErr)
			require.Equal(t, providerErrorApplicationType, appErr.Type())
			require.Equal(t, !retryable, appErr.NonRetryable())
		})
	}
}

func TestProviderTemporalEnvelopePreservesSmallFieldsWithoutCause(t *testing.T) {
	cause := errors.New("raw upstream cause")
	providerErr := model.NewProviderError(
		"anthropic",
		"complete",
		503,
		model.ProviderErrorKindUnavailable,
		"service_unavailable",
		"provider unavailable",
		"request-1",
		true,
		cause,
	)

	wrapped := Wrap(providerErr)

	var appErr *temporal.ApplicationError
	require.ErrorAs(t, wrapped, &appErr)
	require.NoError(t, appErr.Unwrap())
	require.LessOrEqual(t, len(appErr.Message()), maxTemporalErrorMessageBytes)
	got, ok := Provider(wrapped)
	require.True(t, ok)
	require.Equal(t, "anthropic", got.Provider())
	require.Equal(t, "complete", got.Operation())
	require.Equal(t, 503, got.HTTPStatus())
	require.Equal(t, model.ProviderErrorKindUnavailable, got.Kind())
	require.Equal(t, "service_unavailable", got.Code())
	require.Equal(t, "provider unavailable", got.Message())
	require.Equal(t, "request-1", got.RequestID())
	require.True(t, got.Retryable())
	require.NoError(t, got.Unwrap())

	failure := temporal.GetDefaultFailureConverter().ErrorToFailure(wrapped)
	require.Nil(t, failure.Cause)
	require.NotContains(t, failure.String(), cause.Error())
	require.Less(t, proto.Size(failure), maxEncodedTemporalFailureBytes)
}

func TestProviderTemporalEnvelopeBoundsOversizedFieldsAndCause(t *testing.T) {
	const hugeBytes = 1 << 20
	huge := strings.Repeat("raw-upstream-secret-", hugeBytes/len("raw-upstream-secret-"))
	tests := []struct {
		name      string
		provider  string
		operation string
		code      string
		message   string
		requestID string
		cause     error
		check     func(*testing.T, *model.ProviderError)
	}{
		{
			name:     "provider",
			provider: huge,
			check: func(t *testing.T, got *model.ProviderError) {
				requireBoundedEvidence(t, got.Provider(), "provider", huge)
			},
		},
		{
			name:      "operation",
			operation: huge,
			check: func(t *testing.T, got *model.ProviderError) {
				requireBoundedEvidence(t, got.Operation(), "operation", huge)
			},
		},
		{
			name: "code",
			code: huge,
			check: func(t *testing.T, got *model.ProviderError) {
				requireBoundedEvidence(t, got.Code(), "code", huge)
			},
		},
		{
			name:    "message",
			message: huge,
			check: func(t *testing.T, got *model.ProviderError) {
				requireBoundedEvidence(t, got.Message(), "message", huge)
			},
		},
		{
			name:      "request id",
			requestID: huge,
			check: func(t *testing.T, got *model.ProviderError) {
				requireBoundedEvidence(t, got.RequestID(), "request_id", huge)
			},
		},
		{
			name:  "cause",
			cause: errors.New(huge),
			check: func(t *testing.T, got *model.ProviderError) {
				require.NoError(t, got.Unwrap())
				require.Equal(t, "provider unavailable", got.Message())
			},
		},
		{
			name:      "combined",
			provider:  huge,
			operation: huge,
			code:      huge,
			message:   huge,
			requestID: huge,
			cause:     errors.New(huge),
			check: func(t *testing.T, got *model.ProviderError) {
				requireBoundedEvidence(t, got.Provider(), "provider", huge)
				requireBoundedEvidence(t, got.Operation(), "operation", huge)
				requireBoundedEvidence(t, got.Code(), "code", huge)
				requireBoundedEvidence(t, got.Message(), "message", huge)
				requireBoundedEvidence(t, got.RequestID(), "request_id", huge)
				require.NoError(t, got.Unwrap())
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := test.provider
			if provider == "" {
				provider = "anthropic"
			}
			operation := test.operation
			if operation == "" {
				operation = "complete"
			}
			code := test.code
			if code == "" {
				code = "service_unavailable"
			}
			message := test.message
			if message == "" {
				message = "provider unavailable"
			}
			requestID := test.requestID
			if requestID == "" {
				requestID = "request-1"
			}
			wrapped := Wrap(model.NewProviderError(
				provider,
				operation,
				503,
				model.ProviderErrorKindUnavailable,
				code,
				message,
				requestID,
				true,
				test.cause,
			))

			var appErr *temporal.ApplicationError
			require.ErrorAs(t, wrapped, &appErr)
			require.NoError(t, appErr.Unwrap())
			require.LessOrEqual(t, len(appErr.Message()), maxTemporalErrorMessageBytes)
			got, ok := Provider(wrapped)
			require.True(t, ok)
			require.Equal(t, 503, got.HTTPStatus())
			require.Equal(t, model.ProviderErrorKindUnavailable, got.Kind())
			require.True(t, got.Retryable())
			test.check(t, got)

			failure := temporal.GetDefaultFailureConverter().ErrorToFailure(wrapped)
			require.Nil(t, failure.Cause)
			require.NotContains(t, failure.String(), "raw-upstream-secret-")
			require.Less(t, proto.Size(failure), maxEncodedTemporalFailureBytes)
		})
	}
}

func TestOutputAndInvalidTemporalEnvelopesBoundMessagesWithoutCause(t *testing.T) {
	huge := strings.Repeat("raw-output-secret-", 1<<16)
	output := Wrap(planner.NewOutputContractError(errors.New(huge)))
	var outputApp *temporal.ApplicationError
	require.ErrorAs(t, output, &outputApp)
	require.NoError(t, outputApp.Unwrap())
	require.LessOrEqual(t, len(outputApp.Message()), maxTemporalErrorMessageBytes)
	require.Contains(t, outputApp.Message(), "sha256=")
	require.NotContains(t, outputApp.Message(), "raw-output-secret-")
	outputFailure := temporal.GetDefaultFailureConverter().ErrorToFailure(output)
	require.Nil(t, outputFailure.Cause)
	require.Less(t, proto.Size(outputFailure), maxEncodedTemporalFailureBytes)

	malformed := temporal.NewNonRetryableApplicationError(
		huge,
		providerErrorApplicationType,
		errors.New(huge),
		"not provider details",
	)
	invalid := Wrap(malformed)
	var invalidApp *temporal.ApplicationError
	require.ErrorAs(t, invalid, &invalidApp)
	require.Equal(t, invalidReservedApplicationType, invalidApp.Type())
	require.NoError(t, invalidApp.Unwrap())
	require.LessOrEqual(t, len(invalidApp.Message()), maxTemporalErrorMessageBytes)
	require.NotContains(t, invalidApp.Message(), "raw-output-secret-")
	invalidFailure := temporal.GetDefaultFailureConverter().ErrorToFailure(invalid)
	require.Nil(t, invalidFailure.Cause)
	require.Less(t, proto.Size(invalidFailure), maxEncodedTemporalFailureBytes)
}

func TestWrapFindsOutputContractAfterAnotherJoinedApplicationError(t *testing.T) {
	t.Parallel()

	ordinary := temporal.NewApplicationError("first failure", "ordinary")
	outputContract := temporal.NewNonRetryableApplicationError(
		"invalid reply",
		outputContractErrorApplicationType,
		nil,
		outputContractErrorDetails{Origin: string(planner.OutputContractOriginPlanner)},
	)

	got := Wrap(errors.Join(ordinary, outputContract))

	var appErr *temporal.ApplicationError
	require.ErrorAs(t, got, &appErr)
	require.Equal(t, outputContractErrorApplicationType, appErr.Type())
	require.True(t, appErr.NonRetryable())
}

func TestProviderFindsFailureAfterAnotherJoinedApplicationError(t *testing.T) {
	t.Parallel()

	ordinary := temporal.NewApplicationError("first failure", "ordinary")
	provider := Wrap(model.NewProviderError(
		"anthropic",
		"complete",
		503,
		model.ProviderErrorKindUnavailable,
		"service_unavailable",
		"provider unavailable",
		"request-1",
		false,
		nil,
	))

	got, ok := Provider(errors.Join(ordinary, provider))

	require.True(t, ok)
	require.Equal(t, "anthropic", got.Provider())
	require.False(t, got.Retryable())
}

func TestWrapClassifiesOuterProviderBeforeWrappedOutput(t *testing.T) {
	t.Parallel()

	outputErr := outputcontract.NewWithOrigin(
		errors.New("invalid reply"),
		outputcontract.OriginModel,
	)
	providerErr := model.NewProviderError(
		"anthropic",
		"complete",
		503,
		model.ProviderErrorKindUnavailable,
		"service_unavailable",
		"provider unavailable",
		"request-1",
		true,
		outputErr,
	)

	got := Wrap(providerErr)

	var appErr *temporal.ApplicationError
	require.ErrorAs(t, got, &appErr)
	require.Equal(t, providerErrorApplicationType, appErr.Type())
	require.False(t, appErr.NonRetryable())
	require.False(t, IsOutputContract(got))
	classified, ok := Provider(got)
	require.True(t, ok)
	require.Equal(t, "anthropic", classified.Provider())
}

func TestWrapRejectsMalformedReservedEnvelopes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
	}{
		{
			name: "output missing details",
			err: temporal.NewNonRetryableApplicationError(
				"invalid reply",
				outputContractErrorApplicationType,
				nil,
			),
		},
		{
			name: "output invalid origin",
			err: temporal.NewNonRetryableApplicationError(
				"invalid reply",
				outputContractErrorApplicationType,
				nil,
				outputContractErrorDetails{Origin: "worker"},
			),
		},
		{
			name: "output retryable",
			err: temporal.NewApplicationErrorWithCause(
				"invalid reply",
				outputContractErrorApplicationType,
				nil,
				outputContractErrorDetails{Origin: string(planner.OutputContractOriginModel)},
			),
		},
		{
			name: "output has cause",
			err: temporal.NewNonRetryableApplicationError(
				"invalid reply",
				outputContractErrorApplicationType,
				errors.New("raw output cause"),
				outputContractErrorDetails{Origin: string(planner.OutputContractOriginModel)},
			),
		},
		{
			name: "provider malformed details",
			err: temporal.NewNonRetryableApplicationError(
				"provider failed",
				providerErrorApplicationType,
				nil,
				"not provider details",
			),
		},
		{
			name: "provider invalid kind",
			err: temporal.NewNonRetryableApplicationError(
				"provider failed",
				providerErrorApplicationType,
				nil,
				providerErrorDetails{
					Version:  providerErrorDetailsVersion,
					Provider: boundedText{Value: "anthropic"},
					Kind:     "temporary",
				},
			),
		},
		{
			name: "provider retryability conflict",
			err: temporal.NewNonRetryableApplicationError(
				"provider failed",
				providerErrorApplicationType,
				nil,
				providerErrorDetails{
					Version:   providerErrorDetailsVersion,
					Provider:  boundedText{Value: "anthropic"},
					Kind:      string(model.ProviderErrorKindUnavailable),
					Retryable: true,
				},
			),
		},
		{
			name: "provider has cause",
			err: func() error {
				details := validProviderDetails()
				return temporal.NewNonRetryableApplicationError(
					providerErrorMessage(details),
					providerErrorApplicationType,
					errors.New("raw provider cause"),
					details,
				)
			}(),
		},
		{
			name: "provider invalid status",
			err: temporal.NewNonRetryableApplicationError(
				"provider failed",
				providerErrorApplicationType,
				nil,
				func() providerErrorDetails {
					details := validProviderDetails()
					details.HTTPStatus = 700
					return details
				}(),
			),
		},
		{
			name: "provider forged oversized evidence",
			err: temporal.NewNonRetryableApplicationError(
				"provider failed",
				providerErrorApplicationType,
				nil,
				func() providerErrorDetails {
					details := validProviderDetails()
					details.Message = boundedText{
						Value:         "forged",
						SHA256:        strings.Repeat("0", 64),
						OriginalBytes: maxProviderMessageBytes + 1,
					}
					return details
				}(),
			),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var got error
			require.NotPanics(t, func() {
				got = Wrap(test.err)
			})

			var appErr *temporal.ApplicationError
			require.ErrorAs(t, got, &appErr)
			require.Equal(t, invalidReservedApplicationType, appErr.Type())
			require.True(t, appErr.NonRetryable())
			require.False(t, IsOutputContract(got))
			_, ok := Provider(got)
			require.False(t, ok)
		})
	}
}

func TestWrapBoundsHugeGenericError(t *testing.T) {
	huge := strings.Repeat("generic-secret-", 1<<16)

	wrapped := Wrap(errors.New(huge))

	var appErr *temporal.ApplicationError
	require.ErrorAs(t, wrapped, &appErr)
	require.Equal(t, genericErrorApplicationType, appErr.Type())
	require.False(t, appErr.NonRetryable())
	require.Equal(t, "operation failed", appErr.Message())
	require.NoError(t, appErr.Unwrap())
	var details genericErrorDetails
	require.NoError(t, appErr.Details(&details))
	require.True(t, details.Retryable)
	requireBoundedEvidence(t, details.Message.Value, "message", huge)
	failure := temporal.GetDefaultFailureConverter().ErrorToFailure(wrapped)
	require.Nil(t, failure.Cause)
	require.NotContains(t, failure.String(), "generic-secret-")
	require.Less(t, proto.Size(failure), maxEncodedTemporalFailureBytes)
}

func TestWrapBoundsCustomApplicationErrorAndPreservesRetryability(t *testing.T) {
	huge := strings.Repeat("application-secret-", 1<<16)
	tests := []struct {
		name         string
		err          error
		nonRetryable bool
	}{
		{
			name: "retryable",
			err: temporal.NewApplicationErrorWithCause(
				huge,
				huge,
				errors.New(huge),
				huge,
			),
		},
		{
			name: "nonretryable",
			err: temporal.NewNonRetryableApplicationError(
				huge,
				huge,
				errors.New(huge),
				huge,
			),
			nonRetryable: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wrapped := Wrap(test.err)
			var appErr *temporal.ApplicationError
			require.ErrorAs(t, wrapped, &appErr)
			require.Equal(t, genericErrorApplicationType, appErr.Type())
			require.Equal(t, test.nonRetryable, appErr.NonRetryable())
			require.NoError(t, appErr.Unwrap())
			var details genericErrorDetails
			require.NoError(t, appErr.Details(&details))
			require.Equal(t, !test.nonRetryable, details.Retryable)
			requireBoundedEvidence(t, details.OriginalType.Value, "application_type", huge)
			requireBoundedEvidence(t, details.Message.Value, "message", huge)
			failure := temporal.GetDefaultFailureConverter().ErrorToFailure(wrapped)
			require.Nil(t, failure.Cause)
			require.NotContains(t, failure.String(), "application-secret-")
			require.Less(t, proto.Size(failure), maxEncodedTemporalFailureBytes)
		})
	}
}

func TestWrapRejectsCyclicErrorGraphs(t *testing.T) {
	single := &singleCycleError{}
	single.next = single
	joined := &joinedCycleError{}
	joined.children = []error{errors.New("ordinary"), joined}

	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "single unwrap cycle", err: single},
		{name: "joined cycle", err: joined},
	} {
		t.Run(test.name, func(t *testing.T) {
			var wrapped error
			require.NotPanics(t, func() {
				wrapped = Wrap(test.err)
			})
			var appErr *temporal.ApplicationError
			require.ErrorAs(t, wrapped, &appErr)
			require.Equal(t, invalidReservedApplicationType, appErr.Type())
			require.True(t, appErr.NonRetryable())
			require.NoError(t, appErr.Unwrap())
			failure := temporal.GetDefaultFailureConverter().ErrorToFailure(wrapped)
			require.Nil(t, failure.Cause)
			require.Less(t, proto.Size(failure), maxEncodedTemporalFailureBytes)
		})
	}
}

const maxEncodedTemporalFailureBytes = 8 * 1024

func validProviderDetails() providerErrorDetails {
	return providerErrorDetails{
		Version:    providerErrorDetailsVersion,
		Provider:   boundedText{Value: "anthropic"},
		Operation:  boundedText{Value: "complete"},
		HTTPStatus: 503,
		Kind:       string(model.ProviderErrorKindUnavailable),
		Code:       boundedText{Value: "service_unavailable"},
		Message:    boundedText{Value: "provider unavailable"},
		RequestID:  boundedText{Value: "request-1"},
	}
}

func requireBoundedEvidence(t *testing.T, got, field, original string) {
	t.Helper()
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(original)))
	require.Equal(t, boundedTextReplacement(field, sum, len(original)), got)
}
