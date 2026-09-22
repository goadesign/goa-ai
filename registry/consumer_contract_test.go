package registry

// The selected registration must change when consumer behavior changes, even
// when both versions accept exactly the same model arguments.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	toolcontract "goa.design/goa-ai/runtime/toolregistry/contract"
)

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
