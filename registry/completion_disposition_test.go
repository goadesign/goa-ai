package registry

// Completion tests preserve the distinction between accepted provider bytes,
// deadline settlement, and a failed storage operation at the service boundary.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
	goa "goa.design/goa/v3/pkg"
)

type (
	// completionAdmissions records the exact submitted bytes and supplies the
	// storage outcome without invoking unrelated admission operations.
	completionAdmissions struct {
		callAdmissionRepository
		accepted bool
		err      error
		payload  []byte
	}
)

func TestCompleteToolCallReportsStoreAcceptance(t *testing.T) {
	t.Parallel()

	for _, accepted := range []bool{true, false} {
		name := "deadline settled"
		if accepted {
			name = "provider bytes accepted"
		}
		t.Run(name, func(t *testing.T) {
			store := &completionAdmissions{accepted: accepted}
			service := &Service{callAdmissions: store}
			payload := completionDispositionPayload(t)

			result, err := service.CompleteToolCall(t.Context(), payload)

			require.NoError(t, err)
			assert.Equal(t, accepted, result.Accepted)
			assert.Equal(t, payload.ResultJSON, store.payload)
		})
	}
}

func TestCompleteToolCallPreservesStorageErrors(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		err  error
		code string
	}{
		{name: "conflicting terminal", err: errCallTerminalConflict, code: "validation_error"},
		{name: "lost authority", err: errToolsetNotFound, code: "service_unavailable"},
		{name: "storage failure", err: errors.New("storage unavailable"), code: "service_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &Service{callAdmissions: &completionAdmissions{err: test.err}}

			result, err := service.CompleteToolCall(t.Context(), completionDispositionPayload(t))

			require.Error(t, err)
			assert.Nil(t, result)
			var named *goa.ServiceError
			require.ErrorAs(t, err, &named)
			assert.Equal(t, test.code, named.Name)
		})
	}
}

// Complete records the body that the service would submit to Redis.
func (s *completionAdmissions) Complete(
	_ context.Context,
	_, _, _, _, _, _, _ string,
	payload []byte,
) (bool, error) {
	s.payload = payload
	return s.accepted, s.err
}

// completionDispositionPayload supplies a structurally valid terminal so
// service tests reach the storage result without bypassing envelope validation.
func completionDispositionPayload(t *testing.T) *genregistry.CompleteToolCallPayload {
	t.Helper()
	result := toolregistry.NewToolResultMessage(
		testActiveRegistrationToken, "completion-call", json.RawMessage(`{"value":7}`),
	)
	body, err := json.Marshal(result)
	require.NoError(t, err)
	return &genregistry.CompleteToolCallPayload{
		Toolset:                   "completion-toolset",
		ProviderID:                "completion-provider",
		ProviderIncarnationID:     "11111111-1111-4111-8111-111111111111",
		RegistrationToken:         result.RegistrationToken,
		ProviderRegistrationToken: result.RegistrationToken,
		ToolUseID:                 result.ToolUseID,
		RequestEventID:            "1-0",
		ResultJSON:                body,
	}
}
