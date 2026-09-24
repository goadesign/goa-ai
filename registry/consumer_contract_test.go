package registry

// The selected registration must change when consumer behavior changes, even
// when both versions accept exactly the same model arguments.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	toolcontract "goa.design/goa-ai/runtime/toolregistry/contract"
)

func TestNativeImageMarkerPreservesUnmarkedContractEncoding(t *testing.T) {
	source := &genregistry.ToolServerData{
		Kind: "fixture.image.v1", Audience: "evidence",
		Schema: []byte(`{"type":"object"}`), Type: &genregistry.ToolTypeMetadata{},
	}
	// This is the previously persisted shape, before native image support.
	legacy := struct {
		Kind        string
		Audience    string
		Description *string
		Schema      []byte
		Type        *genregistry.ToolTypeMetadata
	}{source.Kind, source.Audience, source.Description, source.Schema, source.Type}
	want, err := json.Marshal(legacy)
	require.NoError(t, err)
	got, err := json.Marshal(source)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	toolset := testCatalogToolset("images", "Images", nil)
	toolset.Tools = validRegisterPayloadForSchemaAdmission("images").Tools
	toolset.Tools[0].ConsumerContract = &genregistry.ConsumerContract{ServerData: []*genregistry.ToolServerData{source}}
	unmarked, err := toolcontract.Fingerprint(toolset)
	require.NoError(t, err)
	source.NativeImage = true
	marked, err := toolcontract.Fingerprint(toolset)
	require.NoError(t, err)
	assert.NotEqual(t, unmarked, marked)
	source.NativeImage = false
	restored, err := toolcontract.Fingerprint(toolset)
	require.NoError(t, err)
	assert.Equal(t, unmarked, restored)
}

func TestSchemaFingerprintBindsConsumerContract(t *testing.T) {
	toolset := testCatalogToolset("records", "Records", nil)
	toolset.Tools = validRegisterPayloadForSchemaAdmission("records").Tools
	schemaOnly, err := toolcontract.Fingerprint(toolset)
	require.NoError(t, err)
	tool := toolset.Tools[0]
	tool.ConsumerContract = &genregistry.ConsumerContract{Kind: "service", Title: "Find records"}
	original, err := toolcontract.Fingerprint(toolset)
	require.NoError(t, err)
	assert.NotEqual(t, schemaOnly, original)
	tool.ConsumerContract.Confirmation = &genregistry.ToolConfirmation{
		PromptTemplate: "Confirm this call", DeniedResultTemplate: `{}`,
	}
	confirmed, err := toolcontract.Fingerprint(toolset)
	require.NoError(t, err)
	assert.NotEqual(t, original, confirmed)
	tool.ConsumerContract = nil
	unchanged, err := toolcontract.Fingerprint(toolset)
	require.NoError(t, err)
	assert.Equal(t, schemaOnly, unchanged, "schema-only registrations retain their existing identity")
}

func TestSchemaFingerprintRejectsUnselectedFieldPath(t *testing.T) {
	toolset := testCatalogToolset("records", "Records", nil)
	toolset.Tools = validRegisterPayloadForSchemaAdmission("records").Tools
	toolset.Tools[0].ConsumerContract = &genregistry.ConsumerContract{
		Payload: &genregistry.ToolTypeMetadata{Fields: []*genregistry.ToolFieldMetadata{{
			Path: []*genregistry.ToolFieldPathSegment{{}},
		}}},
	}
	_, err := toolcontract.Fingerprint(toolset)
	require.ErrorContains(t, err, "consumer contract")
}
