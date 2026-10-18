// Schema-only inspection preserves descriptive fields without exposing old
// consumer metadata, while still rejecting corrupt saved data.
package registry

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

func TestHistoricalSchemaInspection(t *testing.T) {
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	store := newTestCatalogMap(clock)
	state, definition := historicalCatalogFixture(t, `{"Bounds":{"Paging":{"ReplayPayload":true}}}`, false)
	raw, err := marshalCatalogState(state)
	require.NoError(t, err)
	key := toolsetCatalogKey(state.Info.Name)
	store.content[key], store.definitions[key] = raw, definition
	service := &Service{catalog: newToolsetCatalog(store, clock)}
	payload := &genregistry.GetToolsetPayload{Name: state.Info.Name}
	inspected, err := service.GetToolsetSchemas(t.Context(), payload)
	require.NoError(t, err)
	original := testDefinitionToolset()
	assert.Equal(t, original.Name, inspected.Name)
	assert.Equal(t, original.Description, inspected.Description)
	assert.Equal(t, original.Version, inspected.Version)
	assert.Equal(t, original.Tags, inspected.Tags)
	assert.Equal(t, state.RegisteredAt, inspected.RegisteredAt)
	require.Len(t, inspected.Tools, 1)
	assert.Equal(t, original.Tools[0].Name, inspected.Tools[0].Name)
	assert.Equal(t, original.Tools[0].Tags, inspected.Tools[0].Tags)
	assert.Equal(t, original.Tools[0].PayloadSchema, inspected.Tools[0].PayloadSchema)
	assert.Equal(t, original.Tools[0].ResultSchema, inspected.Tools[0].ResultSchema)
	assert.Equal(t, original.Tools[0].SidecarSchema, inspected.Tools[0].SidecarSchema)
	assert.Equal(t, raw, store.content[key])
	assert.Equal(t, definition, store.definitions[key])
	store.definitions[key] = strings.Replace(definition, "true", "false", 1)
	_, err = service.GetToolsetSchemas(t.Context(), payload)
	requireServiceErrorName(t, err, "service_unavailable")
	store.definitions[key] = definition
	require.NoError(t, service.catalog.Retire(t.Context(), state.Info.Name, state.RegistrationToken))
	_, err = service.GetToolsetSchemas(t.Context(), payload)
	requireServiceErrorName(t, err, "not_found")
	_, err = service.GetToolsetSchemas(t.Context(), &genregistry.GetToolsetPayload{Name: "missing"})
	requireServiceErrorName(t, err, "not_found")
}

func TestSchemaInspectionKeepsCurrentAndNativeValidation(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "service", true: "native"}[native], func(t *testing.T) {
			clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
			store := newTestCatalogMap(clock)
			catalog := newToolsetCatalog(store, clock)
			toolset := testDefinitionToolset()
			if native {
				agent := testAgentToolset("revision/1")
				toolset = &genregistry.Toolset{Name: agent.Name, Tools: agent.Tools}
			}
			definition := testCatalogDefinition(t, toolset)
			if native {
				_, err := catalog.RegisterAgent(t.Context(), definition, "")
				require.NoError(t, err)
			} else {
				_, err := catalog.Register(t.Context(), definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
				require.NoError(t, err)
			}
			service := &Service{catalog: catalog}
			payload := &genregistry.GetToolsetPayload{Name: toolset.Name}
			result, err := service.GetToolsetSchemas(t.Context(), payload)
			require.NoError(t, err)
			assert.Equal(t, toolset.Name, result.Name)
			require.Len(t, result.Tools, len(toolset.Tools))
			assert.Equal(t, toolset.Tools[0].PayloadSchema, result.Tools[0].PayloadSchema)
			key := toolsetCatalogKey(toolset.Name)
			store.definitions[key] = strings.Replace(definition.raw, `"ConsumerContract":{`, `"ConsumerContract":{"Unknown":true,`, 1)
			require.NotEqual(t, definition.raw, store.definitions[key])
			_, err = service.GetToolsetSchemas(t.Context(), payload)
			requireServiceErrorName(t, err, "service_unavailable")
			assert.ErrorContains(t, err, "unknown field")
		})
	}
}
