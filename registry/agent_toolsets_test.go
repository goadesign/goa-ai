package registry

// Native registrations must survive registry restarts and concurrent updates
// without acquiring service-provider leases or changing accepted declarations.

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

func TestAgentToolsetRegistrationReplacementAndRestart(t *testing.T) {
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	store := newTestCatalogMap(clock)
	service := &Service{catalog: newToolsetCatalog(store, clock), validator: newSchemaValidator()}
	first, err := service.RegisterAgentToolset(t.Context(), testAgentToolset("revision/1"))
	require.NoError(t, err)
	same, err := service.RegisterAgentToolset(t.Context(), testAgentToolset("revision/1"))
	require.NoError(t, err)
	assert.Equal(t, first, same)
	_, err = service.RegisterAgentToolset(t.Context(), testAgentToolset("revision/2"))
	require.Error(t, err)

	next := testAgentToolset("revision/2")
	second, err := service.ReplaceAgentToolset(t.Context(), &genregistry.ReplaceAgentToolsetPayload{
		Name: next.Name, Tools: next.Tools, ExpectedRegistrationToken: first.RegistrationToken,
	})
	require.NoError(t, err)
	assert.NotEqual(t, first.RegistrationToken, second.RegistrationToken)
	assert.Equal(t, "revision/1", first.Toolset.Tools[0].ConsumerContract.Agent.Configuration)
	_, err = service.ReplaceAgentToolset(t.Context(), &genregistry.ReplaceAgentToolsetPayload{
		Name: next.Name, Tools: next.Tools, ExpectedRegistrationToken: first.RegistrationToken,
	})
	require.Error(t, err)

	restarted := newToolsetCatalog(store, clock)
	require.NoError(t, restarted.validatePersistedEntries(t.Context()))
	current, err := restarted.GetToolset(t.Context(), next.Name)
	require.NoError(t, err)
	assert.Equal(t, "revision/2", current.Tools[0].ConsumerContract.Agent.Configuration)
	_, _, err = restarted.HealthIdentity(t.Context(), next.Name)
	require.ErrorIs(t, err, errToolsetNotFound)
	_, err = restarted.Register(t.Context(), testCatalogDefinition(t, current),
		testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
	require.ErrorIs(t, err, errAdmissionBlocked)

	require.NoError(t, restarted.Retire(t.Context(), next.Name, second.RegistrationToken))
	listed, err := restarted.ListToolsets(t.Context(), nil)
	require.NoError(t, err)
	assert.Empty(t, listed)
	require.NoError(t, restarted.validatePersistedEntries(t.Context()))
	retired, err := store.Retired(t.Context(), second.RegistrationToken)
	require.NoError(t, err)
	assert.False(t, retired)
	_, err = restarted.RegisterAgent(t.Context(), testCatalogDefinition(t, second.Toolset), second.RegistrationToken)
	require.NoError(t, err)
}

func TestAgentToolsetConcurrentReplacementHasOneWinner(t *testing.T) {
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	service := &Service{catalog: newToolsetCatalog(newTestCatalogMap(clock), clock), validator: newSchemaValidator()}
	first, err := service.RegisterAgentToolset(t.Context(), testAgentToolset("revision/1"))
	require.NoError(t, err)
	errors := make(chan error, 2)
	var wg sync.WaitGroup
	for _, revision := range []string{"revision/2", "revision/3"} {
		wg.Go(func() {
			next := testAgentToolset(revision)
			_, err := service.ReplaceAgentToolset(t.Context(), &genregistry.ReplaceAgentToolsetPayload{
				Name: next.Name, Tools: next.Tools, ExpectedRegistrationToken: first.RegistrationToken,
			})
			errors <- err
		})
	}
	wg.Wait()
	close(errors)
	successes := 0
	for err := range errors {
		if err == nil {
			successes++
		}
	}
	assert.Equal(t, 1, successes)
}

func TestAgentToolsetRejectsServiceToolsAndPreservesServiceEncoding(t *testing.T) {
	declaration := testAgentToolset("revision/1")
	contract := declaration.Tools[0].ConsumerContract
	contract.Kind = "service"
	require.Error(t, validateAgentTools(declaration.Tools))
	contract.Agent = nil
	encoded, err := json.Marshal(contract)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), `"Agent"`)
	require.Error(t, validateAgentTools(declaration.Tools))
}

func testAgentToolset(revision string) *genregistry.AgentToolsetDeclaration {
	schema := []byte(`{"type":"object","properties":{"value":{"type":"integer"}},"required":["value"],"additionalProperties":false}`)
	return &genregistry.AgentToolsetDeclaration{
		Name: "installation",
		Tools: []*genregistry.ToolSchema{{
			Name: "installation.check", PayloadSchema: schema, ExecutionPayloadSchema: schema, ResultSchema: schema,
			ConsumerContract: &genregistry.ConsumerContract{
				Kind: "agent", Title: "Check installation",
				Agent:   &genregistry.AgentToolTarget{Executor: "generic.agent", Configuration: revision},
				Search:  &genregistry.ToolSearchDocument{Length: 1, Terms: map[string]int{"installation": 1}},
				Payload: &genregistry.ToolTypeMetadata{SchemaWithoutRootExample: schema},
				Result:  &genregistry.ToolTypeMetadata{SchemaWithoutRootExample: schema},
			},
		}},
	}
}
