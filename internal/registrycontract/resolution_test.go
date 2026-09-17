package registrycontract

// These tests restore selected definitions without a registry connection. They
// cover the durable data needed after a provider changes or history is compacted.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

const continuationToolName = "records.continue"

func TestSelectionRetainsOnlyRequiredContracts(t *testing.T) {
	source := testDeclaration()
	target := testDeclaration()
	target.Name = continuationToolName
	target.ConsumerContract.Bounds.Paging.SourceTool = &source.Name
	unrelated := testDeclaration()
	unrelated.Name = "records.unrelated"
	unrelated.ConsumerContract.Bounds = nil
	registration := testResolution(source, target, unrelated)
	catalog, err := Resolve(registration)
	require.NoError(t, err)
	binding, err := catalog.Select("company", tools.Ident(source.Name))
	require.NoError(t, err)
	assert.NotContains(t, string(binding.Resolution), "unrelated")
	assert.Contains(t, string(binding.Resolution), continuationToolName)

	registration.RegistrationToken = strings.Repeat("b", 64)
	source.PayloadSchema = []byte(`{"type":"string"}`)
	restored, err := Read(binding)
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("a", 64), restored.Registered.RegistrationToken)
	assert.Len(t, restored.Specs, 2)
	_, err = restored.Specs["records.change"].Payload.Codec.FromJSON([]byte(`{"value":9007199254740993}`))
	require.NoError(t, err)

	next, err := restored.Select("company", continuationToolName)
	require.NoError(t, err)
	continued, err := Read(next)
	require.NoError(t, err)
	assert.Len(t, continued.Specs, 2)
}

func TestResolutionRejectsBrokenPagination(t *testing.T) {
	_, err := Resolve(testResolution(testDeclaration()))
	require.ErrorContains(t, err, "no matching continuation")
	source := testDeclaration()
	target := testDeclaration()
	target.Name = continuationToolName
	wrong := "records.wrong"
	target.ConsumerContract.Bounds.Paging.SourceTool = &wrong
	_, err = Resolve(testResolution(source, target))
	require.Error(t, err)
}

func TestReadRejectsMalformedSavedBindings(t *testing.T) {
	for _, document := range []string{
		`null`,
		`{}`,
		`{"unknown":true}`,
		`{"registrationToken":"bad","toolset":{"name":"records","registeredAt":"2026-09-17T00:00:00Z"}}`,
		`{"toolset":{"tools":[null]}}`,
		`{"toolset":{"tools":[{"consumerContract":{"payload":{"fields":[null]}}}]}}`,
		`{} {}`,
	} {
		t.Run(document, func(t *testing.T) {
			_, err := Read(&tools.RegistryBinding{Registry: "company", Resolution: rawjson.Message(document)})
			require.Error(t, err)
		})
	}
	_, err := Read(&tools.RegistryBinding{Resolution: rawjson.Message(`{}`)})
	require.ErrorContains(t, err, "registry is required")
}

func testResolution(declarations ...*genregistry.ToolSchema) *genregistry.ResolvedToolset {
	return &genregistry.ResolvedToolset{
		RegistrationToken: strings.Repeat("a", 64),
		Toolset: &genregistry.Toolset{
			Name: "provider.records", RegisteredAt: "2026-09-17T00:00:00Z", Tools: declarations,
		},
	}
}
