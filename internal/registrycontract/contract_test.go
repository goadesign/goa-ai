package registrycontract

// These tests cover the contract consumed after registry transport validation:
// codecs preserve exact values, metadata stays attached, and server-only data
// cannot change kind or audience after a call has been selected.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestCompilePreservesPortableContract(t *testing.T) {
	declaration := testDeclaration()
	spec, err := Compile(declaration)
	require.NoError(t, err)
	value, err := spec.Payload.Codec.FromJSON([]byte(`{"value":9007199254740993}`))
	require.NoError(t, err)
	require.Equal(t, json.Number("9007199254740993"), value.(map[string]any)["value"])
	_, err = spec.Payload.Codec.FromJSON([]byte(`{"value":"wrong type"}`))
	require.Error(t, err)
	assert.Equal(t, declaration.ConsumerContract.Confirmation.PromptTemplate, spec.Confirmation.PromptTemplate)
	assert.Equal(t, "records.continue", spec.Bounds.Paging.ContinueTool.String())
	assert.Equal(t, "cursor", spec.Bounds.Paging.CursorField)
	assert.Equal(t, []string{"facility"}, spec.Meta["scope"])
	assert.Equal(t, "Report the returned value.", spec.ResultReminder)
	assert.Equal(t, []tools.FieldPathSegment{
		tools.FixedField("items"), tools.DynamicField{}, tools.FixedField("*"),
	}, spec.Payload.Fields[0].Path)
	assert.Equal(t, "text", spec.Payload.Fields[0].Branches[0].Value)
	assert.Equal(t, []tools.FieldPathSegment{tools.FixedField("choice"), tools.FixedField("type")},
		spec.Payload.Fields[0].Branches[0].Discriminator)

	// After resolution the source response can be released or changed without
	// altering the selected tool's declaration or private server-data validators.
	declaration.ConsumerContract.Search.Terms["records"] = 200
	declaration.ConsumerContract.Meta["scope"][0] = "mutated"
	declaration.ConsumerContract.Confirmation.PromptTemplate = "changed"
	declaration.ConsumerContract.ServerData[0].Audience = "internal"
	declaration.PayloadSchema[0] = '!'
	assert.Equal(t, 1, spec.Search.Terms["records"])
	assert.Equal(t, []string{"facility"}, spec.Meta["scope"])
	assert.Equal(t, "Confirm {{ json .value }}", spec.Confirmation.PromptTemplate)
	_, err = spec.Payload.Codec.FromJSON([]byte(`{"value":1}`))
	require.NoError(t, err)
	_, err = spec.CanonicalizeServerData(rawjson.Message(`[{"kind":"record","audience":"timeline","data":{"value":9007199254740993}}]`))
	require.NoError(t, err)
}

func TestCompileServerDataRejectsContractChanges(t *testing.T) {
	spec, err := Compile(testDeclaration())
	require.NoError(t, err)
	for _, data := range []string{
		`[{"kind":"unknown","audience":"timeline","data":{"value":1}}]`,
		`[{"kind":"record","audience":"internal","data":{"value":1}}]`,
		`[{"kind":"record","audience":"timeline","data":{"value":"invalid"}}]`,
		`[{"kind":"record","audience":"timeline","data":{"value":1}},{"kind":"record","audience":"timeline","data":{"value":2}}]`,
	} {
		_, err := spec.CanonicalizeServerData(rawjson.Message(data))
		require.Error(t, err)
	}
	spec.ServerData[0].Audience = tools.AudienceInternal
	spec.ServerData[0].Type.Codec.FromJSON = nil
	canonical, err := spec.CanonicalizeServerData(rawjson.Message(`[{"kind":"record","audience":"timeline","data":{"value":9007199254740993}}]`))
	require.NoError(t, err)
	assert.Contains(t, string(canonical), "9007199254740993")
}

func TestCompileRejectsUnavailableExecutionSemantics(t *testing.T) {
	for _, kind := range []string{"", "agent", "control"} {
		declaration := testDeclaration()
		if kind == "" {
			declaration.ConsumerContract = nil
		} else {
			declaration.ConsumerContract.Kind = kind
		}
		_, err := Compile(declaration)
		require.Error(t, err)
	}
	declaration := testDeclaration()
	declaration.ConsumerContract.Search.Length++
	_, err := Compile(declaration)
	require.ErrorContains(t, err, "search document")
}

func testDeclaration() *genregistry.ToolSchema {
	document := []byte(`{"type":"object","properties":{"value":{"type":"integer"}},"required":["value"],"additionalProperties":false}`)
	reminder, continuation := "Report the returned value.", "records.continue"
	return &genregistry.ToolSchema{
		Name:                   "records.change",
		PayloadSchema:          document,
		ExecutionPayloadSchema: document,
		ResultSchema:           document,
		ConsumerContract: &genregistry.ConsumerContract{
			Kind: "service", Title: "Change record",
			Search: &genregistry.ToolSearchDocument{Length: 1, Terms: map[string]int{"records": 1}},
			Meta:   map[string][]string{"scope": {"facility"}},
			Payload: &genregistry.ToolTypeMetadata{
				SchemaWithoutRootExample: document,
				Fields: []*genregistry.ToolFieldMetadata{{
					Path: []*genregistry.ToolFieldPathSegment{
						{Segment: genregistry.NewToolFieldSegmentField("items")},
						{Segment: genregistry.NewToolFieldSegmentElement(&genregistry.ToolCollectionElement{})},
						{Segment: genregistry.NewToolFieldSegmentField("*")},
					},
					Branches: []*genregistry.ToolUnionBranch{{
						Discriminator: []*genregistry.ToolFieldPathSegment{
							{Segment: genregistry.NewToolFieldSegmentField("choice")},
							{Segment: genregistry.NewToolFieldSegmentField("type")},
						},
						Value: "text",
					}},
				}},
			},
			Result: &genregistry.ToolTypeMetadata{SchemaWithoutRootExample: document},
			Bounds: &genregistry.ToolBounds{Paging: &genregistry.ToolPaging{
				ContinueTool: &continuation, CursorField: "cursor", NextCursorField: "next_cursor",
			}},
			Confirmation: &genregistry.ToolConfirmation{
				PromptTemplate:       "Confirm {{ json .value }}",
				DeniedResultTemplate: `{"value":0}`,
			},
			ServerData: []*genregistry.ToolServerData{{
				Kind: "record", Audience: "timeline", Schema: document,
				Type: &genregistry.ToolTypeMetadata{SchemaWithoutRootExample: document},
			}},
			ResultReminder: &reminder,
		},
	}
}
