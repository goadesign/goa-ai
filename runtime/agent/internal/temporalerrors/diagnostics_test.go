package temporalerrors

// Diagnostic text and execution classification have separate transport
// contracts. These tests verify both through Temporal's actual failure codec.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/protobuf/proto"

	"goa.design/goa-ai/runtime/agent/internal/errorevidence"
	"goa.design/goa-ai/runtime/agent/model"
)

func TestTemporalDiagnosticTextRoundTrip(t *testing.T) {
	for _, original := range []error{
		errors.New(""),
		fmt.Errorf("read operation: %w", errors.New("field[7] is invalid")),
		errors.Join(errors.New("first failure"), errors.New("second failure")),
		errors.Join(context.Canceled, errors.New("independent failure")),
		temporal.NewNonRetryableApplicationError("custom failure", "custom.type", errors.New("original cause"), "custom details"),
		errors.New(strings.Repeat("é", maxTemporalErrorMessageBytes/2)),
		errors.New(strings.Repeat("é", maxTemporalErrorMessageBytes/2) + "x"),
	} {
		wrapped := Wrap(original)
		var app *temporal.ApplicationError
		require.ErrorAs(t, wrapped, &app)
		assert.Equal(t, errorevidence.BoundedMessage(original.Error()), app.Message())
		assert.True(t, utf8.ValidString(app.Message()))
		assert.LessOrEqual(t, len(app.Message()), maxTemporalErrorMessageBytes)
		converter := temporal.GetDefaultFailureConverter()
		encoded := converter.ErrorToFailure(wrapped)
		assert.Less(t, proto.Size(encoded), maxEncodedTemporalFailureBytes)
		restored := converter.FailureToError(encoded)
		assert.Same(t, restored, Wrap(restored))
		var decoded *temporal.ApplicationError
		require.ErrorAs(t, restored, &decoded)
		assert.Equal(t, app.Message(), decoded.Message())
		assert.Equal(t, app.NonRetryable(), decoded.NonRetryable())
		var details genericErrorDetails
		require.NoError(t, decoded.Details(&details))
		assert.Equal(t, genericDiagnosticVersion, details.Version)
		bytes, err := json.Marshal(details)
		require.NoError(t, err)
		assert.LessOrEqual(t, len(bytes), maxTemporalDetailsJSONBytes)
	}
}

func TestTemporalProviderDiagnosticPreservesOuterContext(t *testing.T) {
	for _, retryable := range []bool{false, true} {
		provider := model.NewProviderError("provider", "complete", 503, model.ProviderErrorKindUnavailable,
			"unavailable", "try later", "request-1", retryable, errors.New("typed SDK detail"))
		original := fmt.Errorf("planner dependency: %w", provider)
		wrapped := Wrap(original)
		converter := temporal.GetDefaultFailureConverter()
		restored := converter.FailureToError(converter.ErrorToFailure(wrapped))
		got, ok := Provider(restored)
		require.True(t, ok)
		assert.Equal(t, provider.Provider(), got.Provider())
		assert.Equal(t, provider.Operation(), got.Operation())
		assert.Equal(t, provider.HTTPStatus(), got.HTTPStatus())
		assert.Equal(t, provider.Kind(), got.Kind())
		assert.Equal(t, provider.Code(), got.Code())
		assert.Equal(t, provider.Message(), got.Message())
		assert.Equal(t, provider.RequestID(), got.RequestID())
		assert.Equal(t, retryable, got.Retryable())
		require.EqualError(t, got.Unwrap(), original.Error())
		assert.NotSame(t, provider.Unwrap(), got.Unwrap())
		assert.Same(t, restored, Wrap(restored))
	}
}

func TestTemporalDiagnosticVersionsKeepLegacyInvariants(t *testing.T) {
	legacyGeneric := genericErrorDetails{
		Version:   genericErrorDetailsVersion,
		Message:   boundedText{Value: "old detail"},
		Retryable: true,
	}
	legacy := temporal.NewApplicationError("operation failed", genericErrorApplicationType, legacyGeneric)
	assert.Same(t, legacy, Wrap(legacy))
	var legacyApp *temporal.ApplicationError
	require.ErrorAs(t, legacy, &legacyApp)
	assert.Equal(t, "operation failed", legacyApp.Message())
	forged := temporal.NewApplicationError("new diagnostic with old version", genericErrorApplicationType, legacyGeneric)
	var rejected *temporal.ApplicationError
	require.ErrorAs(t, Wrap(forged), &rejected)
	assert.Equal(t, invalidReservedApplicationType, rejected.Type())

	legacyProvider := validProviderDetails()
	legacyProvider.Version = providerErrorDetailsVersion
	legacyProvider.Retryable = true
	oldProvider := temporal.NewApplicationError(providerErrorMessage(legacyProvider), providerErrorApplicationType, legacyProvider)
	assert.Same(t, oldProvider, Wrap(oldProvider))
	provider, ok := Provider(oldProvider)
	require.True(t, ok)
	require.NoError(t, provider.Unwrap())
	forgedProvider := temporal.NewApplicationError("new diagnostic with old version", providerErrorApplicationType, legacyProvider)
	require.ErrorAs(t, Wrap(forgedProvider), &rejected)
	assert.Equal(t, invalidReservedApplicationType, rejected.Type())
	legacyGeneric.Version = "goa_ai.generic_error.unknown"
	require.ErrorAs(t, Wrap(temporal.NewApplicationError("unknown", genericErrorApplicationType, legacyGeneric)), &rejected)
	assert.Equal(t, invalidReservedApplicationType, rejected.Type())
}

func TestInvalidUTF8TemporalDiagnosticOmission(t *testing.T) {
	original := errors.New(string([]byte{0xff}))
	failure := temporal.GetDefaultFailureConverter().ErrorToFailure(Wrap(original))
	encoded, err := proto.Marshal(failure)
	require.NoError(t, err)
	require.NoError(t, proto.Unmarshal(encoded, failure))
	assert.Contains(t, failure.Message, "invalid_utf8")
	assert.NotContains(t, failure.Message, "�")
	digest, _ := errorevidence.FingerprintText(original.Error())
	assert.Contains(t, failure.Message, digest)
	restored := temporal.GetDefaultFailureConverter().FailureToError(failure)
	assert.Same(t, restored, Wrap(restored))
	// Existing structured detail text is not an exact-byte transport contract.
	// This assertion deliberately concerns only the new outer diagnostic.
}

func TestTemporalGenericDiagnosticPreservesNewOuterContext(t *testing.T) {
	for _, retryable := range []bool{false, true} {
		inner := wrapGeneric(errors.New("detail"), "custom.type", retryable)
		assert.Same(t, inner, Wrap(inner))
		outer := fmt.Errorf("child context: %w", inner)
		var app *temporal.ApplicationError
		require.ErrorAs(t, Wrap(outer), &app)
		assert.Equal(t, outer.Error(), app.Message())
		assert.Equal(t, !retryable, app.NonRetryable())
		var details genericErrorDetails
		require.NoError(t, app.Details(&details))
		assert.Equal(t, "custom.type", details.OriginalType.Value)
		assert.Equal(t, "detail", details.Message.Value)
		assert.Equal(t, retryable, details.Retryable)
		legacy := temporal.NewApplicationError("operation failed", genericErrorApplicationType,
			genericErrorDetails{Version: genericErrorDetailsVersion, Message: boundedText{Value: "old detail"}, Retryable: true})
		assert.Same(t, legacy, Wrap(fmt.Errorf("historical context: %w", legacy)))
	}
}

func TestTemporalCurrentDiagnosticRejectsForgedInvalidUTF8(t *testing.T) {
	provider := validProviderDetails()
	provider.Version = providerDiagnosticVersion
	provider.Retryable = true
	generic := genericErrorDetails{Version: genericDiagnosticVersion, Message: boundedText{Value: "detail"}, Retryable: true}
	for _, test := range []struct {
		typeName string
		details  any
	}{
		{providerErrorApplicationType, provider},
		{genericErrorApplicationType, generic},
	} {
		valid := temporal.NewApplicationError("diagnostic", test.typeName, test.details)
		assert.Same(t, valid, Wrap(valid))
		forged := temporal.NewApplicationError(string([]byte{0xff}), test.typeName, test.details)
		var rejected *temporal.ApplicationError
		require.ErrorAs(t, Wrap(forged), &rejected)
		assert.Equal(t, invalidReservedApplicationType, rejected.Type())
		assert.True(t, rejected.NonRetryable())
		assert.True(t, utf8.ValidString(rejected.Message()))
		assert.LessOrEqual(t, len(rejected.Message()), maxTemporalErrorMessageBytes)
		_, err := proto.Marshal(temporal.GetDefaultFailureConverter().ErrorToFailure(rejected))
		require.NoError(t, err)
	}
}
