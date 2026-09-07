package temporalerrors

// These tests cross the real SDK failure codec. Large native failures are not
// workflow arguments: local round trips prove exact bytes, not server acceptance.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	failurepb "go.temporal.io/api/failure/v1"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/protobuf/proto"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/errorevidence"
	"goa.design/goa-ai/runtime/agent/internal/outputcontract"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
)

func TestCurrentTemporalDiagnosticExactText(t *testing.T) {
	for _, size := range []int{0, 3071, 3072, 3073, 65_537, engine.MaxPayloadBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			message := strings.Repeat("x", size) + "é\n\"\\"
			for _, retryable := range []bool{false, true} {
				original := temporal.NewApplicationErrorWithOptions(message, "custom.type", temporal.ApplicationErrorOptions{
					NonRetryable: !retryable,
					Cause:        errors.New("not serialized as a cause"),
					Details:      []any{"custom details are not serialized"},
				})
				wrapped := Wrap(original)
				app := roundTripCurrentFailure(t, wrapped)
				assert.Equal(t, currentGenericApplicationType, app.Type())
				assert.Equal(t, original.Error(), app.Message())
				assert.Equal(t, !retryable, app.NonRetryable())
				var details currentGenericDetails
				require.NoError(t, app.Details(&details))
				assert.Equal(t, currentGenericDetails{OriginalType: "custom.type", Message: message, Retryable: retryable}, details)
				assert.Same(t, app, Wrap(app))
				outer := fmt.Errorf("caller context: %w", app)
				withContext := roundTripCurrentFailure(t, Wrap(outer))
				assert.Equal(t, outer.Error(), withContext.Message())
				var retained currentGenericDetails
				require.NoError(t, withContext.Details(&retained))
				assert.Equal(t, details, retained)
			}
		})
	}
}

func TestCurrentTemporalProviderExactFields(t *testing.T) {
	// Each identity exceeds its historical allocation. The complete details
	// also exceed both the historical 4 KiB allocation and the argument limit.
	long := strings.Repeat("é", engine.MaxPayloadBytes/2+1)
	for _, retryable := range []bool{false, true} {
		original := model.NewProviderError("provider/"+long, "operation/"+long, 503,
			model.ProviderErrorKindUnavailable, "code/"+long, "message/"+long, "request/"+long,
			retryable, errors.New("original SDK object is not serialized"))
		outer := fmt.Errorf("dependency failed: %w", original)
		app := roundTripCurrentFailure(t, Wrap(outer))
		assert.Equal(t, currentProviderApplicationType, app.Type())
		assert.Equal(t, outer.Error(), app.Message())
		assert.Equal(t, !retryable, app.NonRetryable())
		got, ok := Provider(app)
		require.True(t, ok)
		assert.Equal(t, original.Provider(), got.Provider())
		assert.Equal(t, original.Operation(), got.Operation())
		assert.Equal(t, original.HTTPStatus(), got.HTTPStatus())
		assert.Equal(t, original.Kind(), got.Kind())
		assert.Equal(t, original.Code(), got.Code())
		assert.Equal(t, original.Message(), got.Message())
		assert.Equal(t, original.RequestID(), got.RequestID())
		assert.Equal(t, original.Retryable(), got.Retryable())
		require.EqualError(t, got.Unwrap(), outer.Error())
		assert.Same(t, app, Wrap(app))
	}
}

func TestCurrentTemporalOutputCauseAndOrigin(t *testing.T) {
	message := strings.Repeat("validator detail\n", 1000)
	for _, origin := range []planner.OutputContractOrigin{planner.OutputContractOriginModel, planner.OutputContractOriginPlanner, planner.OutputContractOriginTool} {
		original := outputcontract.NewWithOrigin(errors.New(message), origin)
		app := roundTripCurrentFailure(t, Wrap(original))
		assert.Equal(t, currentOutputApplicationType, app.Type())
		assert.Equal(t, message, app.Message())
		assert.True(t, app.NonRetryable())
		assert.Equal(t, origin, OutputContractOrigin(app))
		assert.True(t, IsOutputContract(app))
		assert.Same(t, app, Wrap(app))
	}
	contract, err := model.NewRequestContract(&model.Request{})
	require.NoError(t, err)
	validation := contract.RejectProviderOutput(model.OutputValidationResponseShape, nil, errors.New(message))
	app := roundTripCurrentFailure(t, Wrap(outputcontract.NewWithOrigin(validation, outputcontract.OriginModel)))
	assert.Equal(t, message, app.Message())
}

func TestCurrentTemporalInvalidTextIsExplicit(t *testing.T) {
	invalid := string([]byte{0xff}) + strings.Repeat("x", 4096)
	app := roundTripCurrentFailure(t, Wrap(errors.New(invalid)))
	assert.Equal(t, errorevidence.DiagnosticMessage(invalid), app.Message())
	assert.Contains(t, app.Message(), "invalid_utf8")
	assert.NotContains(t, app.Message(), "size_limit")
	assert.NotContains(t, app.Message(), "�")
	var details currentGenericDetails
	require.NoError(t, app.Details(&details))
	assert.Equal(t, app.Message(), details.Message)
	provider := model.NewProviderError(invalid, invalid, 503, model.ProviderErrorKindUnavailable, invalid, invalid, invalid, true, nil)
	got, ok := Provider(roundTripCurrentFailure(t, Wrap(provider)))
	require.True(t, ok)
	assert.Equal(t, errorevidence.DiagnosticMessage(invalid), got.Provider())
	assert.Equal(t, errorevidence.DiagnosticMessage(invalid), got.Operation())
	assert.Equal(t, errorevidence.DiagnosticMessage(invalid), got.Message())
	assert.Equal(t, errorevidence.DiagnosticMessage(invalid), got.Code())
	assert.Equal(t, errorevidence.DiagnosticMessage(invalid), got.RequestID())
}

func TestCurrentTemporalClassificationAndHistoricalRewrap(t *testing.T) {
	require.NoError(t, Wrap(nil))
	assert.True(t, temporal.IsCanceledError(Wrap(fmt.Errorf("stopped: %w", context.Canceled))))
	assert.False(t, temporal.IsCanceledError(Wrap(errors.Join(context.Canceled, errors.New("independent failure")))))
	for _, native := range []error{
		errors.New("historical generic"),
		planner.NewOutputContractError(errors.New("historical rejection")),
		model.NewProviderError("provider", "complete", 503, model.ProviderErrorKindUnavailable, "code", "message", "id", true, nil),
	} {
		old := wrapHistorical(native)
		assert.Same(t, old, Wrap(old))
		outer := fmt.Errorf("historical context: %w", old)
		codec := temporal.GetDefaultFailureConverter()
		assert.True(t, proto.Equal(codec.ErrorToFailure(wrapHistorical(outer)), codec.ErrorToFailure(Wrap(outer))))
	}
	for _, forged := range []error{
		temporal.NewApplicationError("retryable output", currentOutputApplicationType, outputContractErrorDetails{Origin: string(planner.OutputContractOriginPlanner)}),
		temporal.NewNonRetryableApplicationError("bad origin", currentOutputApplicationType, nil, outputContractErrorDetails{Origin: "unknown"}),
		temporal.NewApplicationError("missing provider", currentProviderApplicationType, currentProviderDetails{Kind: string(model.ProviderErrorKindUnknown), Retryable: true}),
		temporal.NewApplicationError("bad retry setting", currentGenericApplicationType, currentGenericDetails{Retryable: false}),
		temporal.NewApplicationError("retryable invalid", currentInvalidApplicationType),
		temporal.NewNonRetryableApplicationError("invalid details", currentInvalidApplicationType, nil, "extra"),
		temporal.NewNonRetryableApplicationError("invalid cause", currentInvalidApplicationType, errors.New("extra")),
	} {
		app := roundTripCurrentFailure(t, Wrap(forged))
		assert.Equal(t, currentInvalidApplicationType, app.Type())
		assert.True(t, app.NonRetryable())
		assert.Same(t, app, Wrap(app))
	}
}

func TestCurrentTemporalMalformedGraphsKeepClassification(t *testing.T) {
	var typedNil *typedNilError
	cycle := &singleCycleError{}
	cycle.next = cycle
	children := make([]error, maxClassificationChildren+1)
	for index := range children {
		children[index] = context.Canceled
	}
	for _, original := range []error{typedNil, cycle, &manyChildrenError{children: children}} {
		assert.False(t, CancellationOnly(original))
		app := roundTripCurrentFailure(t, Wrap(original))
		assert.Equal(t, currentInvalidApplicationType, app.Type())
		assert.True(t, app.NonRetryable())
	}
	app := roundTripCurrentFailure(t, Wrap(outputcontract.NewWithOrigin(&panickingError{}, outputcontract.OriginPlanner)))
	assert.True(t, IsOutputContract(app))
	assert.Equal(t, "error message unavailable", app.Message())
	provider := model.NewProviderError("provider", "complete", 503, model.ProviderErrorKindUnavailable,
		"code", "provider owns this error", "request", true, planner.NewOutputContractError(errors.New("nested output")))
	app = roundTripCurrentFailure(t, Wrap(provider))
	assert.Equal(t, currentProviderApplicationType, app.Type())
	assert.False(t, app.NonRetryable())
	assert.False(t, IsOutputContract(app))
	app = roundTripCurrentFailure(t, Wrap(planner.NewOutputContractError(provider)))
	assert.Equal(t, currentOutputApplicationType, app.Type())
	assert.True(t, app.NonRetryable())
	assert.Equal(t, provider.Error(), app.Message())
	for _, bad := range []*model.ProviderError{
		model.NewProviderError("provider", "complete", 700, model.ProviderErrorKindUnknown, "", "", "", true, nil),
		model.NewProviderError("provider", "complete", 0, model.ProviderErrorKind("invalid"), "", "", "", true, nil),
	} {
		assert.Equal(t, currentInvalidApplicationType, roundTripCurrentFailure(t, Wrap(bad)).Type())
	}
}

func TestHistoricalTerminalFailureKeepsExactStoredBytes(t *testing.T) {
	stored := &failurepb.Failure{
		Message: "historical terminal diagnostic",
		Source:  "GoSDK",
		FailureInfo: &failurepb.Failure_ApplicationFailureInfo{ApplicationFailureInfo: &failurepb.ApplicationFailureInfo{
			Type:         "goa_ai.output_contract_error",
			NonRetryable: true,
			Details: &commonpb.Payloads{Payloads: []*commonpb.Payload{{
				Metadata: map[string][]byte{"encoding": []byte("json/plain")},
				Data:     []byte(`{"Origin":"planner"}`),
			}}},
		}},
	}
	before, err := proto.Marshal(stored)
	require.NoError(t, err)
	codec := temporal.GetDefaultFailureConverter()
	restored := codec.FailureToError(stored)
	assert.Same(t, restored, Wrap(restored))
	assert.Equal(t, planner.OutputContractOriginPlanner, OutputContractOrigin(restored))
	after, err := proto.Marshal(codec.ErrorToFailure(Wrap(restored)))
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

// roundTripCurrentFailure invokes the SDK codec and protobuf wire encoding.
// No server is contacted and no assertion here claims server acceptance.
func roundTripCurrentFailure(t *testing.T, err error) *temporal.ApplicationError {
	t.Helper()
	codec := temporal.GetDefaultFailureConverter()
	failure := codec.ErrorToFailure(err)
	assert.Nil(t, failure.Cause)
	encoded, encodeErr := proto.Marshal(failure)
	require.NoError(t, encodeErr)
	require.NoError(t, proto.Unmarshal(encoded, failure))
	var app *temporal.ApplicationError
	require.ErrorAs(t, codec.FailureToError(failure), &app)
	return app
}
