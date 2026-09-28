// These tests keep native completion and retained-record validation on the same
// decoder without changing their identity, retry, or original-byte contracts.
package registry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/toolregistry"
	goa "goa.design/goa/v3/pkg"
)

func TestCompleteToolCallRejectsInvalidTerminalEnvelopeBeforeStorage(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		extra string
	}{
		{name: "unknown root", extra: `,"extra":true`},
		{name: "unknown server item", extra: `,"server_data":[{"kind":"record","audience":"timeline","data":{},"extra":true}]`},
		{name: "unknown recovery", extra: `,"error":{"failure":{"recovery":{"extra":true}}}`},
		{name: "retry", extra: `,"retry":{"reason":"provider_overloaded","retry_after_ms":250}`},
		{name: "wrong tool use", extra: `,"tool_use_id":"another-call"`},
		{name: "wrong token", extra: `,"registration_token":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &completionAdmissions{accepted: true}
			service := &Service{callAdmissions: store}
			payload := completionDispositionPayload(t)
			payload.ResultJSON = []byte(fmt.Sprintf(
				`{"registration_token":%q,"tool_use_id":%q%s}`,
				payload.RegistrationToken, payload.ToolUseID, test.extra,
			))

			result, err := service.CompleteToolCall(t.Context(), payload)

			require.Error(t, err)
			assert.Nil(t, result)
			var named *goa.ServiceError
			require.ErrorAs(t, err, &named)
			assert.Equal(t, "validation_error", named.Name)
			assert.Nil(t, store.payload)
		})
	}
}

func TestTerminalEnvelopeKeepsSubmittedAndRetainedBytes(t *testing.T) {
	t.Parallel()

	for _, extra := range []string{
		"",
		`,"result_json": { "dynamic":[9007199254740993, {"unregistered":true}] }`,
		`,"error":{"code":"execution_failed","failure":{"kind":"internal","error":{"message":"failed","cause":{"message":"cause"}},"recovery":{"action":"finish"}}}`,
	} {
		store := &completionAdmissions{accepted: true}
		service := &Service{callAdmissions: store}
		payload := completionDispositionPayload(t)
		payload.ResultJSON = []byte(fmt.Sprintf(
			" \n{ \"registration_token\":%q,\n \"tool_use_id\":%q%s }\t\n",
			payload.RegistrationToken, payload.ToolUseID, extra,
		))
		original := bytes.Clone(payload.ResultJSON)

		result, err := service.CompleteToolCall(t.Context(), payload)
		require.NoError(t, err)
		assert.True(t, result.Accepted)
		assert.Equal(t, original, store.payload)
		sum := sha256.Sum256(original)
		require.NoError(t, validateTerminalPayload(
			true, store.payload, hex.EncodeToString(sum[:]), callTerminalCauseProvider,
			payload.RegistrationToken, payload.ToolUseID,
		))
		message, err := decodePersistedToolResult(store.payload, payload.RegistrationToken, payload.ToolUseID)
		require.NoError(t, err)
		assert.Equal(t, payload.ToolUseID, message.ToolUseID)
		assert.Equal(t, original, store.payload)
		assert.Equal(t, original, payload.ResultJSON)
	}
}

func TestPersistedTerminalEnvelopeRetainsIdentityAndRetryRestrictions(t *testing.T) {
	t.Parallel()

	payload := completionDispositionPayload(t)
	for _, test := range []struct {
		name  string
		extra string
	}{
		{name: "unknown root", extra: `,"extra":true`},
		{name: "unknown nested field", extra: `,"bounds":{"extra":true}`},
		{name: "wrong tool use", extra: `,"tool_use_id":"another-call"`},
		{name: "wrong token", extra: `,"registration_token":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"`},
		{name: "retry", extra: `,"retry":{"reason":"provider_overloaded","retry_after_ms":250}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"registration_token":%q,"tool_use_id":%q%s}`,
				payload.RegistrationToken, payload.ToolUseID, test.extra))
			sum := sha256.Sum256(body)
			err := validateTerminalPayload(true, body, hex.EncodeToString(sum[:]),
				callTerminalCauseProvider, payload.RegistrationToken, payload.ToolUseID)
			require.Error(t, err)
		})
	}
}

func TestRetainedRegistryOutcomeUnknownEnvelope(t *testing.T) {
	t.Parallel()

	body := []byte(fmt.Sprintf(
		`{"tool_use_id":"unknown-call","registration_token":%q,"error":{"code":"outcome_unknown","failure":{"kind":"internal","error":{"message":"provider outcome is unknown"},"recovery":{"action":"finish"}}}}`,
		testActiveRegistrationToken,
	))
	require.NoError(t, validateOutcomeUnknownPayload(body, testActiveRegistrationToken, "unknown-call"))
	for _, cause := range []string{"execution_deadline", "provider_lease_lost", "provider_lease_released"} {
		require.NoError(t, validateTerminalPayload(true, body, redisTerminalDigest(body),
			cause, testActiveRegistrationToken, "unknown-call"))
	}
	message, err := toolregistry.DecodeToolResultMessage(body)
	require.NoError(t, err)
	assert.Equal(t, toolregistry.ToolErrorCodeOutcomeUnknown, message.Error.Code)
}
