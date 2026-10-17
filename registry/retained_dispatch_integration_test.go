//go:build integration

// These tests exercise admission reads after a provider claims a retained
// request. A later retry publication must not invalidate that execution owner,
// and malformed stored dispatch references must still be rejected.
package registry

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
)

func TestRetainedDispatchAdmission(t *testing.T) {
	for _, tc := range []struct {
		name    string
		retry   bool
		invalid string
	}{
		{name: "latest publication"},
		{name: "earlier publication", retry: true},
		{name: "missing publication marker", retry: true, invalid: "missing"},
		{name: "noncanonical publication", retry: true, invalid: "01-0"},
		{name: "future publication", retry: true, invalid: "18446744073709551615-0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			rdb := getRedis(t)
			reg, err := New(ctx, Config{Redis: rdb, Name: "retained-dispatch-" + uuid.NewString()})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, reg.Close(context.Background())) })
			svc := reg.Service()
			svc.healthTracker = newMockHealthTracker()
			const toolset = "retained-dispatch"
			provider := validRegisterPayloadForSchemaAdmission(toolset)
			registration, err := svc.Register(ctx, provider)
			require.NoError(t, err)
			payload := transitionCallPayload(toolset, "retained-request")
			call, err := svc.CallTool(ctx, payload)
			require.NoError(t, err)
			store := svc.callAdmissions.(*callAdmissionStore)
			firstID := retainedPublicationEventID(t, ctx, rdb, store, call.ToolUseID)
			latestID := firstID
			retry := &genregistry.RetryToolPayload{
				Toolset:                   payload.Toolset,
				Tool:                      payload.Tool,
				PayloadJSON:               payload.PayloadJSON,
				Meta:                      payload.Meta,
				WireProtocolVersion:       toolregistry.WireProtocolVersion,
				ExpectedRegistrationToken: registration.RegistrationToken,
			}

			// Publish a retry while the first request remains eligible to execute.
			if tc.retry {
				require.NoError(t, svc.ReportToolCallOverload(ctx, &genregistry.ProviderToolCallClaimPayload{
					Toolset:                   toolset,
					ProviderID:                provider.ProviderID,
					ProviderIncarnationID:     provider.ProviderIncarnationID,
					ProviderRegistrationToken: registration.RegistrationToken,
					CallRegistrationToken:     registration.RegistrationToken,
					ToolUseID:                 call.ToolUseID,
					RequestEventID:            firstID,
				}))
				retried, retryErr := svc.RetryTool(ctx, retry)
				require.NoError(t, retryErr)
				assert.Equal(t, call, retried)
				latestID = retainedPublicationEventID(t, ctx, rdb, store, call.ToolUseID)
				require.NotEqual(t, firstID, latestID)
			}
			claim := &genregistry.ClaimToolCallPayload{
				Toolset:                   toolset,
				ProviderID:                provider.ProviderID,
				ProviderIncarnationID:     provider.ProviderIncarnationID,
				ProviderRegistrationToken: registration.RegistrationToken,
				CallRegistrationToken:     registration.RegistrationToken,
				ToolUseID:                 call.ToolUseID,
				RequestEventID:            firstID,
				ClaimOperationID:          uuid.NewString(),
			}
			claimed, err := svc.ClaimToolCall(ctx, claim)
			require.NoError(t, err)
			require.Equal(t, string(callClaimExecute), claimed.Disposition)

			// Corrupt only the stored reference under test; a forged marker must not
			// make a noncanonical or future event an admissible execution owner.
			if tc.invalid != "" {
				key := store.callKey(call.ToolUseID)
				if tc.invalid == "missing" {
					require.NoError(t, rdb.HDel(ctx, key, "claim:"+firstID).Err())
				} else {
					require.NoError(t, rdb.HSet(ctx, key,
						"dispatch_request_event_id", tc.invalid, "claim:"+tc.invalid, "1").Err())
				}
				_, err = svc.CallTool(ctx, payload)
				require.ErrorContains(t, err, "CALLDECISIONINVALID dispatch state")
				return
			}

			// Exact callers keep the same identity and deadlines after ownership is
			// granted, even when the owner claimed an earlier published request.
			replayed, err := svc.CallTool(ctx, payload)
			require.NoError(t, err)
			assert.Equal(t, call, replayed)
			if tc.retry {
				replayed, err = svc.RetryTool(ctx, retry)
				require.NoError(t, err)
				assert.Equal(t, call, replayed)
			}
			duplicate := *claim
			duplicate.RequestEventID = latestID
			duplicate.ClaimOperationID = uuid.NewString()
			repeated, err := svc.ClaimToolCall(ctx, &duplicate)
			require.NoError(t, err)
			assert.Equal(t, string(callClaimClaimed), repeated.Disposition)

			result := toolregistry.NewToolResultMessage(registration.RegistrationToken,
				call.ToolUseID, json.RawMessage(`{"value":"finished"}`))
			resultJSON, err := json.Marshal(result)
			require.NoError(t, err)
			completion, err := svc.CompleteToolCall(ctx, &genregistry.CompleteToolCallPayload{
				Toolset:                   toolset,
				ProviderID:                provider.ProviderID,
				ProviderIncarnationID:     provider.ProviderIncarnationID,
				ProviderRegistrationToken: registration.RegistrationToken,
				RegistrationToken:         registration.RegistrationToken,
				ToolUseID:                 call.ToolUseID,
				RequestEventID:            firstID,
				ResultJSON:                resultJSON,
			})
			require.NoError(t, err)
			assert.True(t, completion.Accepted)
			replayed, err = svc.CallTool(ctx, payload)
			require.NoError(t, err)
			assert.Equal(t, call, replayed)
			repeated, err = svc.ClaimToolCall(ctx, &duplicate)
			require.NoError(t, err)
			assert.Equal(t, string(callClaimTerminal), repeated.Disposition)
		})
	}
}
