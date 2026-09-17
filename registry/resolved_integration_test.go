//go:build integration

// These tests use real Redis admission and publication to prove that a resolved
// call cannot transfer to a replacement registration, including during overload.
package registry

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
	goa "goa.design/goa/v3/pkg"
)

type (
	// replacingStreamManager replaces the registration immediately before the
	// real atomic publication, after service validation has already succeeded.
	replacingStreamManager struct {
		StreamManager
		replace func()
	}
)

func TestResolvedCallRejectsReplacementBeforePublication(t *testing.T) {
	for _, duringPublication := range []bool{false, true} {
		t.Run(fmt.Sprintf("during-publication=%t", duringPublication), func(t *testing.T) {
			rdb := getRedis(t)
			ctx := t.Context()
			name := fmt.Sprintf("resolved-replace-%d", time.Now().UnixNano())
			reg, err := New(ctx, Config{Redis: rdb, Name: name})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, reg.Close(context.Background())) })
			svc := reg.Service()
			svc.healthTracker = newMockHealthTracker()
			provider := validRegisterPayloadForSchemaAdmission(name)
			_, err = svc.Register(ctx, provider)
			require.NoError(t, err)
			resolved, err := svc.ResolveToolset(ctx, &genregistry.GetToolsetPayload{Name: name})
			require.NoError(t, err)
			call := resolvedCallPayload(name, resolved.RegistrationToken, "replacement")
			replace := func() {
				replaceResolvedProvider(t, svc, provider, resolved.RegistrationToken)
			}
			if duringPublication {
				svc.streamManager = &replacingStreamManager{StreamManager: svc.streamManager, replace: replace}
			} else {
				replace()
			}
			for range 2 {
				_, err = svc.CallResolvedTool(ctx, call)
				var serviceErr *goa.ServiceError
				require.ErrorAs(t, err, &serviceErr)
				assert.Equal(t, "call_not_admitted", serviceErr.Name)
				assert.Contains(t, serviceErr.Message, "resolved registration")
			}
			publications, err := rdb.XLen(ctx, pulseStreamKeyPrefix+toolregistry.ToolsetStreamID(name)).Result()
			require.NoError(t, err)
			assert.Zero(t, publications)
		})
	}
}

func TestResolvedCallPreservesIdentityAndPublishedResult(t *testing.T) {
	rdb := getRedis(t)
	ctx := t.Context()
	name := fmt.Sprintf("resolved-identity-%d", time.Now().UnixNano())
	reg, err := New(ctx, Config{Redis: rdb, Name: name})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reg.Close(context.Background())) })
	svc := reg.Service()
	svc.healthTracker = newMockHealthTracker()
	provider := validRegisterPayloadForSchemaAdmission(name)
	registered, err := svc.Register(ctx, provider)
	require.NoError(t, err)
	call := resolvedCallPayload(name, registered.RegistrationToken, "identity")
	admitted, err := svc.CallResolvedTool(ctx, call)
	require.NoError(t, err)
	replayed, err := svc.CallResolvedTool(ctx, call)
	require.NoError(t, err)
	assert.Equal(t, admitted, replayed)

	replacement := replaceResolvedProvider(t, svc, provider, registered.RegistrationToken)
	replayed, err = svc.CallResolvedTool(ctx, call)
	require.NoError(t, err)
	assert.Equal(t, admitted, replayed)
	requireReadableResultStream(t, ctx, rdb, replayed)

	changed := *call
	changed.ExpectedRegistrationToken = replacement.RegistrationToken
	_, err = svc.CallResolvedTool(ctx, &changed)
	var serviceErr *goa.ServiceError
	require.ErrorAs(t, err, &serviceErr)
	assert.Equal(t, "validation_error", serviceErr.Name)

	_, err = svc.CallTool(ctx, transitionCallPayload(name, "identity"))
	require.ErrorAs(t, err, &serviceErr)
	assert.Equal(t, "validation_error", serviceErr.Name)
	publications, err := rdb.XLen(ctx, pulseStreamKeyPrefix+toolregistry.ToolsetStreamID(name)).Result()
	require.NoError(t, err)
	assert.EqualValues(t, 1, publications)
}

func TestResolvedCallResumesOverloadOnlyOnOriginalRegistration(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(fmt.Sprintf("replacement=%t", replace), func(t *testing.T) {
			rdb := getRedis(t)
			ctx := t.Context()
			name := fmt.Sprintf("resolved-overload-%d", time.Now().UnixNano())
			reg, err := New(ctx, Config{Redis: rdb, Name: name})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, reg.Close(context.Background())) })
			svc := reg.Service()
			svc.healthTracker = newMockHealthTracker()
			provider := validRegisterPayloadForSchemaAdmission(name)
			registered, err := svc.Register(ctx, provider)
			require.NoError(t, err)
			call := resolvedCallPayload(name, registered.RegistrationToken, "overload")
			admitted, err := svc.CallResolvedTool(ctx, call)
			require.NoError(t, err)
			store := svc.callAdmissions.(*callAdmissionStore)
			eventID := retainedPublicationEventID(t, ctx, rdb, store, admitted.ToolUseID)
			require.NoError(t, svc.ReportToolCallOverload(ctx, &genregistry.ProviderToolCallClaimPayload{
				Toolset:                   name,
				ProviderID:                provider.ProviderID,
				ProviderIncarnationID:     provider.ProviderIncarnationID,
				ProviderRegistrationToken: registered.RegistrationToken,
				CallRegistrationToken:     registered.RegistrationToken,
				ToolUseID:                 admitted.ToolUseID,
				RequestEventID:            eventID,
			}))
			if replace {
				replaceResolvedProvider(t, svc, provider, registered.RegistrationToken)
			}
			resumed, err := svc.CallResolvedTool(ctx, call)
			if replace {
				var serviceErr *goa.ServiceError
				require.ErrorAs(t, err, &serviceErr)
				assert.Equal(t, "admission_conflict", serviceErr.Name)
			} else {
				require.NoError(t, err)
				assert.Equal(t, admitted, resumed)
				repeated, err := svc.CallResolvedTool(ctx, call)
				require.NoError(t, err)
				assert.Equal(t, admitted, repeated)
			}
			publications, err := rdb.XLen(ctx, pulseStreamKeyPrefix+toolregistry.ToolsetStreamID(name)).Result()
			require.NoError(t, err)
			expected := int64(2)
			if replace {
				expected = 1
			}
			assert.Equal(t, expected, publications)
		})
	}
}

// resolvedCallPayload keeps the ordinary request identical so the test checks
// that only the operation and registration distinguish its immutable identity.
func resolvedCallPayload(toolset, token, callID string) *genregistry.CallResolvedToolPayload {
	call := transitionCallPayload(toolset, callID)
	return &genregistry.CallResolvedToolPayload{
		Toolset:                   call.Toolset,
		Tool:                      call.Tool,
		PayloadJSON:               call.PayloadJSON,
		Meta:                      call.Meta,
		WireProtocolVersion:       call.WireProtocolVersion,
		ExpectedRegistrationToken: token,
	}
}

// replaceResolvedProvider changes the registration while keeping the same
// argument schema, proving that schema compatibility cannot authorize transfer.
func replaceResolvedProvider(t *testing.T, svc *Service, provider *genregistry.RegisterPayload, token string) *genregistry.RegisterResult {
	t.Helper()
	ctx := t.Context()
	require.NoError(t, svc.ReleaseProvider(ctx, &genregistry.ReleaseProviderPayload{
		Name:                      provider.Name,
		ProviderID:                provider.ProviderID,
		ProviderIncarnationID:     provider.ProviderIncarnationID,
		ExpectedRegistrationToken: token,
	}))
	replacement := *provider
	replacement.ProviderID += "-replacement"
	replacement.ProviderIncarnationID = testIncarnationB
	replacement.AdmissionRevision = testAdmissionRevisionB
	result, err := svc.Register(ctx, &replacement)
	require.NoError(t, err)
	require.NotEqual(t, token, result.RegistrationToken)
	return result
}

func (m *replacingStreamManager) PublishAdmittedToolCall(ctx context.Context, toolset string, msg toolregistry.ToolCallMessage, admission callAdmission, overloadEventID string) error {
	if m.replace != nil {
		replace := m.replace
		m.replace = nil
		replace()
	}
	return m.StreamManager.PublishAdmittedToolCall(ctx, toolset, msg, admission, overloadEventID)
}
