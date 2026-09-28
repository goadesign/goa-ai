//go:build integration

package registry

// These tests submit terminals through generated gRPC and real Redis. They
// compare the returned acceptance bit with the exact retained terminal, including
// replay after a lost reply and both execution-deadline settlement paths.

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
	goa "goa.design/goa/v3/pkg"
)

type (
	// lostCompletionReply retains a successful owner decision but hides the
	// first response, forcing the client to repeat the exact terminal.
	lostCompletionReply struct {
		genregistry.Service
		attempts atomic.Int64
	}
)

func TestCompleteToolCallDispositionThroughGeneratedTransport(t *testing.T) {
	for _, test := range []struct {
		name           string
		expire         bool
		settleFirst    bool
		loseFirstReply bool
	}{
		{name: "new terminal and identical replay"},
		{name: "lost acceptance reply", loseFirstReply: true},
		{name: "deadline settled by completion", expire: true},
		{name: "deadline settled before completion", expire: true, settleFirst: true},
		{name: "lost deadline settlement reply", expire: true, loseFirstReply: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			rdb := getRedis(t)
			reg, err := New(ctx, Config{
				Redis: rdb, Name: t.Name(), ExecutionTimeout: 3 * time.Second,
			})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, reg.Close(context.Background())) })
			// The test chooses whether Complete or the existing scanner performs
			// settlement. No stored deadline or other production state is edited.
			reg.callSettlement.Close()
			service := reg.Service()
			service.healthTracker = newMockHealthTracker()
			var served genregistry.Service = service
			if test.loseFirstReply {
				served = &lostCompletionReply{Service: service}
			}
			client, _ := startServiceAndClients(t, served)
			provider := validRegisterPayloadForSchemaAdmission("completion-disposition")
			registration, err := client.Register(ctx, provider)
			require.NoError(t, err)
			call, err := client.CallTool(ctx, transitionCallPayload(provider.Name, t.Name()))
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
			message := toolregistry.NewToolResultMessage(
				call.RegistrationToken, call.ToolUseID, json.RawMessage(`{"value":"provider"}`),
			)
			body, err := json.Marshal(message)
			require.NoError(t, err)
			payload := &genregistry.CompleteToolCallPayload{
				Toolset:                   provider.Name,
				ProviderID:                provider.ProviderID,
				ProviderIncarnationID:     provider.ProviderIncarnationID,
				RegistrationToken:         call.RegistrationToken,
				ProviderRegistrationToken: registration.RegistrationToken,
				ToolUseID:                 call.ToolUseID,
				RequestEventID:            eventID,
				ResultJSON:                body,
			}
			if test.expire {
				deadline, err := time.Parse(time.RFC3339Nano, call.ExecutionDeadline)
				require.NoError(t, err)
				require.Eventually(t, func() bool {
					now, readErr := rdb.Time(ctx).Result()
					return readErr == nil && !now.Before(deadline)
				}, 10*time.Second, 10*time.Millisecond)
				if test.settleFirst {
					count, err := store.SettleLostClaims(ctx, 1)
					require.NoError(t, err)
					require.Equal(t, 1, count)
				}
			}
			if test.loseFirstReply {
				result, err := client.CompleteToolCall(ctx, payload)
				require.ErrorContains(t, err, "completion reply lost")
				assert.Nil(t, result)
			}
			result, err := client.CompleteToolCall(ctx, payload)
			require.NoError(t, err)
			assert.Equal(t, !test.expire, result.Accepted)
			callKey := store.callKey(call.ToolUseID)
			retained, err := rdb.HGet(ctx, callKey, "terminal_payload").Bytes()
			require.NoError(t, err)
			if test.expire {
				var terminal toolregistry.ToolResultMessage
				require.NoError(t, json.Unmarshal(retained, &terminal))
				require.NotNil(t, terminal.Error)
				assert.Equal(t, toolregistry.ToolErrorCodeOutcomeUnknown, terminal.Error.Code)
				assert.NotEqual(t, body, retained)
			} else {
				assert.Equal(t, body, retained)
			}
			streamKey := pulseStreamKeyPrefix + toolregistry.ResultStreamID(call.ToolUseID)
			length, err := rdb.XLen(ctx, streamKey).Result()
			require.NoError(t, err)
			replayed, err := client.CompleteToolCall(ctx, payload)
			require.NoError(t, err)
			assert.Equal(t, result, replayed)
			afterReplay, err := rdb.XLen(ctx, streamKey).Result()
			require.NoError(t, err)
			assert.Equal(t, length, afterReplay)

			// Identity is still required even after terminal state exists.
			wrongIdentity := *payload
			wrongIdentity.RequestEventID = "0-0"
			rejected, err := client.CompleteToolCall(ctx, &wrongIdentity)
			require.Error(t, err)
			assert.Nil(t, rejected)
			if !test.expire {
				conflicting := *payload
				message.Result = json.RawMessage(`{"value":"different"}`)
				conflicting.ResultJSON, err = json.Marshal(message)
				require.NoError(t, err)
				rejected, err = client.CompleteToolCall(ctx, &conflicting)
				require.Error(t, err)
				assert.Nil(t, rejected)
				var named *goa.ServiceError
				require.ErrorAs(t, err, &named)
				assert.Equal(t, "validation_error", named.Name)
			}
			unchanged, err := rdb.HGet(ctx, callKey, "terminal_payload").Bytes()
			require.NoError(t, err)
			assert.Equal(t, retained, unchanged)
		})
	}
}

// CompleteToolCall changes only response delivery after the real owner has
// decided; the next invocation still uses that owner's retained terminal.
func (s *lostCompletionReply) CompleteToolCall(ctx context.Context, p *genregistry.CompleteToolCallPayload) (*genregistry.CompleteToolCallResult, error) {
	result, err := s.Service.CompleteToolCall(ctx, p)
	if err != nil {
		return nil, err
	}
	if s.attempts.Add(1) == 1 {
		return nil, genregistry.MakeServiceUnavailable(errors.New("completion reply lost"))
	}
	return result, nil
}
