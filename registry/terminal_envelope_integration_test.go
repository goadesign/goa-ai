//go:build integration

// These tests submit exact JSON through generated gRPC and retain it in Redis.
// Replay must validate the stored envelope without rewriting its original bytes.
package registry

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
	goa "goa.design/goa/v3/pkg"
)

func TestTerminalEnvelopeThroughGeneratedTransportAndRedis(t *testing.T) {
	for _, registrySettles := range []bool{false, true} {
		name := "provider terminal"
		if registrySettles {
			name = "registry outcome unknown"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			rdb := getRedis(t)
			reg, err := New(ctx, Config{Redis: rdb, Name: t.Name()})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, reg.Close(context.Background())) })
			service := reg.Service()
			service.healthTracker = newMockHealthTracker()
			client, _ := startServiceAndClients(t, service)
			provider := validRegisterPayloadForSchemaAdmission("terminal-envelope")
			registration, err := client.Register(ctx, provider)
			require.NoError(t, err)
			callPayload := transitionCallPayload(provider.Name, t.Name())
			call, err := client.CallTool(ctx, callPayload)
			require.NoError(t, err)
			store := service.callAdmissions.(*callAdmissionStore)
			eventID := retainedPublicationEventID(t, ctx, rdb, store, call.ToolUseID)
			claim, err := client.ClaimToolCall(ctx, &genregistry.ClaimToolCallPayload{
				Toolset:                   provider.Name,
				ProviderID:                provider.ProviderID,
				ProviderIncarnationID:     provider.ProviderIncarnationID,
				ProviderRegistrationToken: registration.RegistrationToken,
				CallRegistrationToken:     call.RegistrationToken,
				ToolUseID:                 call.ToolUseID,
				RequestEventID:            eventID,
				ClaimOperationID:          uuid.NewString(),
			})
			require.NoError(t, err)
			require.Equal(t, "execute", claim.Disposition)
			payload := &genregistry.CompleteToolCallPayload{
				Toolset:                   provider.Name,
				ProviderID:                provider.ProviderID,
				ProviderIncarnationID:     provider.ProviderIncarnationID,
				RegistrationToken:         call.RegistrationToken,
				ProviderRegistrationToken: registration.RegistrationToken,
				ToolUseID:                 call.ToolUseID,
				RequestEventID:            eventID,
			}
			callKey := store.callKey(call.ToolUseID)
			for _, extra := range []string{
				`,"extra":true`,
				`,"server_data":[{"kind":"record","audience":"timeline","data":{},"extra":true}]`,
				`,"retry":{"reason":"provider_overloaded","retry_after_ms":250}`,
				`,"tool_use_id":"another-call"`,
				`,"registration_token":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"`,
			} {
				payload.ResultJSON = []byte(fmt.Sprintf(
					`{"registration_token":%q,"tool_use_id":%q%s}`,
					call.RegistrationToken, call.ToolUseID, extra,
				))
				result, err := client.CompleteToolCall(ctx, payload)
				require.Error(t, err)
				assert.Nil(t, result)
				var named *goa.ServiceError
				require.ErrorAs(t, err, &named)
				assert.Equal(t, "validation_error", named.Name)
				terminal, err := rdb.HGet(ctx, callKey, "terminal").Result()
				require.NoError(t, err)
				assert.Equal(t, "0", terminal)
			}

			if registrySettles {
				require.NoError(t, client.ReleaseProvider(ctx, &genregistry.ReleaseProviderPayload{
					Name:                      provider.Name,
					ProviderID:                provider.ProviderID,
					ProviderIncarnationID:     provider.ProviderIncarnationID,
					ExpectedRegistrationToken: registration.RegistrationToken,
				}))
			} else {
				payload.ResultJSON = []byte(fmt.Sprintf(
					" \n{ \"tool_use_id\":%q, \"registration_token\":%q,\n \"result_json\": {\"value\":9007199254740993} }\t\n",
					call.ToolUseID, call.RegistrationToken,
				))
				result, err := client.CompleteToolCall(ctx, payload)
				require.NoError(t, err)
				require.True(t, result.Accepted)
				repeated, err := client.CompleteToolCall(ctx, payload)
				require.NoError(t, err)
				assert.Equal(t, result, repeated)
				changed := *payload
				changed.ResultJSON = append([]byte(" "), payload.ResultJSON...)
				rejected, err := client.CompleteToolCall(ctx, &changed)
				require.Error(t, err)
				assert.Nil(t, rejected, "even a whitespace-only byte change conflicts")
			}

			retained, err := rdb.HGet(ctx, callKey, "terminal_payload").Bytes()
			require.NoError(t, err)
			decoded, err := toolregistry.DecodeToolResultMessage(retained)
			require.NoError(t, err)
			require.NoError(t, toolregistry.ValidateToolResultMessage(decoded))
			if registrySettles {
				require.NotNil(t, decoded.Error)
				assert.Equal(t, toolregistry.ToolErrorCodeOutcomeUnknown, decoded.Error.Code)
			} else {
				assert.Equal(t, payload.ResultJSON, retained)
				assert.Equal(t, `{"value":9007199254740993}`, string(decoded.Result))
			}
			streamKey := pulseStreamKeyPrefix + toolregistry.ResultStreamID(call.ToolUseID)
			oldEventID, err := rdb.HGet(ctx, callKey, "terminal_event_id").Result()
			require.NoError(t, err)
			deleted, err := rdb.XDel(ctx, streamKey, oldEventID).Result()
			require.NoError(t, err)
			require.EqualValues(t, 1, deleted)

			replayed, err := client.CallTool(ctx, callPayload)
			require.NoError(t, err)
			assert.Equal(t, call, replayed)
			newEventID, err := rdb.HGet(ctx, callKey, "terminal_event_id").Result()
			require.NoError(t, err)
			assert.NotEqual(t, oldEventID, newEventID)
			events, err := rdb.XRange(ctx, streamKey, newEventID, newEventID).Result()
			require.NoError(t, err)
			require.Len(t, events, 1)
			assert.Equal(t, string(retained), events[0].Values["p"])
			afterReplay, err := rdb.HGet(ctx, callKey, "terminal_payload").Bytes()
			require.NoError(t, err)
			assert.Equal(t, retained, afterReplay)
		})
	}
}
