//go:build integration

package registry

// Native Agent registrations use the same Redis conditional writes as service
// definitions, without provider leases or permanent service retirement records.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

func TestRedisNativeAgentReplacementAndRetirement(t *testing.T) {
	store := newRedisCatalogStore(testRedisClient, t.Name())
	clock := newRedisTimeSource(testRedisClient)
	firstNode := &Service{catalog: newToolsetCatalog(store, clock), validator: newSchemaValidator()}
	secondNode := &Service{catalog: newToolsetCatalog(store, clock), validator: newSchemaValidator()}
	first, err := firstNode.RegisterAgentToolset(t.Context(), testAgentToolset("revision/1"))
	require.NoError(t, err)
	resolved, err := secondNode.catalog.ActiveRegistration(t.Context(), first.Toolset.Name)
	require.NoError(t, err)
	assert.Equal(t, first.RegistrationToken, resolved.RegistrationToken)
	assert.Empty(t, resolved.ProviderLeases)

	next := testAgentToolset("revision/2")
	replacement, err := secondNode.ReplaceAgentToolset(t.Context(), &genregistry.ReplaceAgentToolsetPayload{
		Name: next.Name, Tools: next.Tools, ExpectedRegistrationToken: first.RegistrationToken,
	})
	require.NoError(t, err)
	current, err := firstNode.catalog.GetToolset(t.Context(), next.Name)
	require.NoError(t, err)
	assert.Equal(t, "revision/2", current.Tools[0].ConsumerContract.Agent.Configuration)
	assert.Equal(t, "revision/1", first.Toolset.Tools[0].ConsumerContract.Agent.Configuration)
	restarted := newToolsetCatalog(store, clock)
	require.NoError(t, restarted.validatePersistedEntries(t.Context()))
	require.NoError(t, restarted.Retire(t.Context(), next.Name, replacement.RegistrationToken))
	_, err = firstNode.catalog.GetToolset(t.Context(), next.Name)
	require.ErrorIs(t, err, errToolsetNotFound)
	require.NoError(t, newToolsetCatalog(store, clock).validatePersistedEntries(t.Context()))
}
