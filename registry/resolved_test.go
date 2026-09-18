// These tests verify that resolution reads one registration and that generated
// requests require the token before reaching the registry service.
package registry

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistrypb "goa.design/goa-ai/registry/gen/grpc/registry/pb"
	genregistryserver "goa.design/goa-ai/registry/gen/grpc/registry/server"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
)

func TestResolveToolsetReturnsOneRegistration(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	store := newTestCatalogMap()
	catalog := newToolsetCatalog(store, newTestTimeSource(time.Now()))
	original, err := catalog.Register(ctx, testCatalogDefinition(t, testCatalogToolset("tools", "original", nil)),
		testAdmissionRevisionA, "provider-a", testIncarnationA, time.Hour)
	require.NoError(t, err)
	replacementStore := newTestCatalogMap()
	replacementCatalog := newToolsetCatalog(replacementStore, newTestTimeSource(time.Now()))
	replacement, err := replacementCatalog.Register(ctx, testCatalogDefinition(t, testCatalogToolset("tools", "replacement", nil)),
		testAdmissionRevisionB, "provider-b", testIncarnationB, time.Hour)
	require.NoError(t, err)
	key := toolsetCatalogKey("tools")
	replacementRaw, exists := replacementStore.Get(key)
	require.True(t, exists)
	var replace sync.Once
	store.afterExactRead = func(readKey string) {
		require.Equal(t, key, readKey)
		replace.Do(func() {
			store.mu.Lock()
			store.content[key] = replacementRaw
			store.definitions[key] = replacementStore.definitions[key]
			store.mu.Unlock()
		})
	}
	svc := &Service{catalog: catalog}
	resolved, err := svc.ResolveToolset(ctx, &genregistry.GetToolsetPayload{Name: "tools"})
	require.NoError(t, err)
	assert.Equal(t, original.RegistrationToken, resolved.RegistrationToken)
	assert.Equal(t, "original", *resolved.Toolset.Description)

	resolved, err = svc.ResolveToolset(ctx, &genregistry.GetToolsetPayload{Name: "tools"})
	require.NoError(t, err)
	assert.Equal(t, replacement.RegistrationToken, resolved.RegistrationToken)
	assert.Equal(t, "replacement", *resolved.Toolset.Description)
}

func TestGeneratedCallResolvedToolRequiresExactRegistration(t *testing.T) {
	t.Parallel()

	toolset, tool := "tools", "tools.lookup"
	runID, sessionID, toolCallID := "run", "session", "call"
	version := int32(toolregistry.WireProtocolVersion)
	request := &genregistrypb.CallResolvedToolRequest{
		Toolset:             &toolset,
		Tool:                &tool,
		PayloadJson:         []byte(`{}`),
		WireProtocolVersion: &version,
		Meta: &genregistrypb.ToolCallMeta{
			RunId:      &runID,
			SessionId:  &sessionID,
			ToolCallId: &toolCallID,
		},
	}
	require.ErrorContains(t, genregistryserver.ValidateCallResolvedToolRequest(request), "expected_registration_token")
	token := "invalid"
	request.ExpectedRegistrationToken = &token
	require.Error(t, genregistryserver.ValidateCallResolvedToolRequest(request))
	token = testActiveRegistrationToken
	require.NoError(t, genregistryserver.ValidateCallResolvedToolRequest(request))
	request.WireProtocolVersion = nil
	require.ErrorContains(t, genregistryserver.ValidateCallResolvedToolRequest(request), "wire_protocol_version")
}
