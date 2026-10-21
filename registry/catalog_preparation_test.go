// These tests check that prepared catalog declarations preserve complete data
// while registration inputs and each returned result remain independently owned.
package registry

import (
	"encoding/json"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genrecords "goa.design/goa-ai/internal/testpresentation/gen/records/toolsets/records"
	genregistryserver "goa.design/goa-ai/registry/gen/grpc/registry/server"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	toolcontract "goa.design/goa-ai/runtime/toolregistry/contract"
	"google.golang.org/protobuf/proto"
)

func TestPreparedCatalogDeclarationOwnsRegistrationInputAndReadResults(t *testing.T) {
	t.Parallel()
	input := completePreparedCatalogToolset()
	expected, err := json.Marshal(input)
	require.NoError(t, err)
	prepared, err := newCatalogToolset(input, "synthetic", newSchemaValidator())
	require.NoError(t, err)
	mutatePreparedCatalogResult(input)
	for _, timestamp := range []string{"first registration time", "current registration time"} {
		result := prepared.copyDeclaration(timestamp)
		assert.Equal(t, timestamp, result.RegisteredAt)
		result.RegisteredAt = ""
		encoded, err := json.Marshal(result)
		require.NoError(t, err)
		assert.JSONEq(t, string(expected), string(encoded))
		mutatePreparedCatalogResult(result)
	}
}

func TestPreparedCatalogDeclarationPreservesGeneratedContracts(t *testing.T) {
	t.Parallel()
	native := testAgentToolset("revision/1")
	for _, test := range []struct {
		name    string
		toolset *genregistry.Toolset
	}{
		{name: "service", toolset: genrecords.Toolset()},
		{name: "native", toolset: &genregistry.Toolset{Name: native.Name, Tools: native.Tools}},
		{
			name: "empty optional collections",
			toolset: &genregistry.Toolset{
				Name: "schema-only",
				Tags: []string{},
				Tools: []*genregistry.ToolSchema{{
					Name:                   "schema.read",
					Tags:                   []string{},
					PayloadSchema:          []byte(`{"type":"object"}`),
					ExecutionPayloadSchema: []byte(`{"type":"object"}`),
					ResultSchema:           []byte(`{"type":"object"}`),
				}},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			test.toolset.RegisteredAt = "2026-01-01T00:00:00Z"
			expected, err := json.Marshal(test.toolset)
			require.NoError(t, err)
			expectedSize := proto.Size(genregistryserver.NewProtoGetToolsetResponse(test.toolset))
			prepared, err := newCatalogToolset(test.toolset, "synthetic", newSchemaValidator())
			require.NoError(t, err)
			actual := prepared.copyDeclaration(test.toolset.RegisteredAt)
			encoded, err := json.Marshal(actual)
			require.NoError(t, err)
			assert.Equal(t, expected, encoded)
			assert.Equal(t, expectedSize, proto.Size(genregistryserver.NewProtoGetToolsetResponse(actual)))
		})
	}
}

func TestPreparedCatalogDeclarationConcurrentCopies(t *testing.T) {
	t.Parallel()
	input := completePreparedCatalogToolset()
	prepared, err := newCatalogToolset(input, "synthetic", newSchemaValidator())
	require.NoError(t, err)
	const readers = 8
	results := make(chan *genregistry.Toolset, readers)
	var group sync.WaitGroup
	for range readers {
		group.Go(func() {
			result := prepared.copyDeclaration("current")
			mutatePreparedCatalogResult(result)
			results <- result
		})
	}
	group.Wait()
	close(results)
	assert.Len(t, results, readers)
	actual := prepared.copyDeclaration("")
	assert.Equal(t, input, actual)
}

func TestPreparedCatalogColdReadRejectsMalformedSavedDefinition(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(string) string
	}{
		{name: "unknown root member", change: func(raw string) string {
			return strings.Replace(raw, `{`, `{"Unknown":true,`, 1)
		}},
		{name: "unknown contract member", change: func(raw string) string {
			return strings.Replace(raw, `"Title":"original"`, `"Title":"original","Unknown":true`, 1)
		}},
		{name: "trailing object", change: func(raw string) string {
			return raw + `{}`
		}},
		{name: "trailing invalid bytes", change: func(raw string) string {
			return raw + `broken`
		}},
		{name: "duplicate member", change: func(raw string) string {
			return strings.Replace(raw, `"Name":"tools"`, `"Name":"tools","Name":"other"`, 1)
		}},
		{name: "invalid JSON", change: func(raw string) string {
			return raw[:len(raw)-1]
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, store, clock := testDefinitionCatalog(t)
			key := toolsetCatalogKey("tools")
			raw := store.definitions[key]
			changed := test.change(raw)
			require.NotEqual(t, raw, changed)
			store.definitions[key] = changed
			cold := newToolsetCatalog(store, clock)
			_, err := cold.ActiveRegistration(t.Context(), "tools")
			require.Error(t, err)
			assert.Empty(t, cold.definitions)
		})
	}
}

// TestPreparedCatalogDeclarationPreservesSavedValues compares both preparation
// paths with the exact generated service value represented by saved JSON.
func TestPreparedCatalogDeclarationPreservesSavedValues(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(*genregistry.Toolset)
	}{
		{name: "integers above int32", change: func(input *genregistry.Toolset) {
			contract := input.Tools[0].ConsumerContract
			contract.Search.Length = 1 << 31
			contract.Search.Terms["lookup"] = math.MaxInt
			contract.Payload.Fields[0].Branches = []*genregistry.ToolUnionBranch{{
				Selection: genregistry.NewToolUnionSelectionUntagged(&genregistry.ToolUntaggedUnionBranch{
					JSONKind: "object", Index: math.MaxInt,
				}),
			}}
		}},
		{name: "empty optional collections", change: func(input *genregistry.Toolset) {
			input.Tags = []string{}
			tool := input.Tools[0]
			tool.Tags = []string{}
			tool.SidecarSchema = []byte{}
			contract := tool.ConsumerContract
			contract.RequiredLabels = []string{}
			contract.Meta = map[string][]string{"empty": {}, "null": nil}
			contract.ServerData = []*genregistry.ToolServerData{}
			contract.Payload.Fields = []*genregistry.ToolFieldMetadata{}
			contract.Payload.ExampleJSON = []byte{}
		}},
		{name: "empty optional maps and paths", change: func(input *genregistry.Toolset) {
			contract := input.Tools[0].ConsumerContract
			contract.Meta = map[string][]string{}
			field := contract.Payload.Fields[0]
			field.Path = []*genregistry.ToolFieldPathSegment{}
			field.Branches = []*genregistry.ToolUnionBranch{}
			field.DiscriminatorValues = []string{}
		}},
		{name: "complete metadata and closed unions", change: func(input *genregistry.Toolset) {
			*input = *completePreparedCatalogToolset()
		}},
		{name: "nil optional values", change: func(input *genregistry.Toolset) {
			input.Description, input.Version, input.Tags = nil, nil, nil
			input.Tools[0].Description = nil
			input.Tools[0].Tags = nil
			input.Tools[0].ConsumerContract = nil
		}},
		{name: "saved string decoding", change: func(input *genregistry.Toolset) {
			input.Description = new("description" + string([]byte{0xff}))
			input.Tools[0].ConsumerContract.Meta = map[string][]string{
				string([]byte{0xff}): {"value" + string([]byte{0xfe})},
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := testDefinitionToolset()
			test.change(input)
			fingerprint, err := toolcontract.Fingerprint(input)
			require.NoError(t, err)
			prepared, err := newCatalogToolset(input, fingerprint, newSchemaValidator())
			require.NoError(t, err)
			var expected *genregistry.Toolset
			require.NoError(t, json.Unmarshal([]byte(prepared.raw), &expected))
			expectedJSON, err := json.Marshal(expected)
			require.NoError(t, err)
			expectedFingerprint, err := toolcontract.Fingerprint(expected)
			require.NoError(t, err)
			clock := newTestTimeSource(time.Unix(0, 0))
			cold := newToolsetCatalog(newTestCatalogMap(clock), clock)
			checked, err := cold.validatedDefinition(t.Context(), input.Name, prepared.raw, false)
			require.NoError(t, err)
			for _, source := range []*catalogToolset{prepared, {catalogDefinition: checked, raw: prepared.raw}} {
				actual := source.copyDeclaration("")
				assert.Equal(t, expected, actual)
				actualJSON, err := json.Marshal(actual)
				require.NoError(t, err)
				assert.Equal(t, expectedJSON, actualJSON) //nolint:testifylint // Saved JSON bytes determine the fingerprint, so changed encoding must fail.
				actualFingerprint, err := toolcontract.Fingerprint(actual)
				require.NoError(t, err)
				assert.Equal(t, expectedFingerprint, actualFingerprint)
			}
		})
	}
}

func TestPreparedCatalogNativeTargetOwnership(t *testing.T) {
	t.Parallel()
	native := testAgentToolset("revision/1")
	input := &genregistry.Toolset{Name: native.Name, Tools: native.Tools}
	expected, err := json.Marshal(input)
	require.NoError(t, err)
	prepared, err := newCatalogToolset(input, "synthetic", newSchemaValidator())
	require.NoError(t, err)
	for _, target := range []*genregistry.Toolset{input, prepared.copyDeclaration("")} {
		agent := target.Tools[0].ConsumerContract.Agent
		agent.Executor, agent.Configuration, agent.Workflow, agent.Queue = "changed", "changed", "changed", "changed"
	}
	actual, err := json.Marshal(prepared.copyDeclaration(""))
	require.NoError(t, err)
	assert.Equal(t, string(expected), string(actual))
}

// completePreparedCatalogToolset includes optional metadata, every closed union
// branch and nil collection members accepted by the saved service types.
func completePreparedCatalogToolset() *genregistry.Toolset {
	input := testDefinitionToolset()
	tool := input.Tools[0]
	tool.Description = new("Independent operation")
	tool.ResultSchema = []byte(`{ "type": "object", "properties": {"count": {"type": "number"}} }`)
	tool.SidecarSchema = []byte(`{ "type": "object" }`)
	contract := tool.ConsumerContract
	contract.RequiredLabels = []string{"scope"}
	contract.Bounds = &genregistry.ToolBounds{Paging: &genregistry.ToolPaging{
		ContinueTool: new("tools.next"), SourceTool: new("tools.lookup"),
		RetainedFields: []string{"input"}, CursorField: "cursor", NextCursorField: "next",
	}}
	contract.Confirmation = &genregistry.ToolConfirmation{
		Title: new("Confirm"), PromptTemplate: "Continue?", DeniedResultTemplate: `{}`,
	}
	contract.ResultReminder = new("Read the result")
	contract.RequiresUI, contract.ReplanOnTimeout = true, true
	contract.Payload = completePreparedTypeMetadata()
	contract.Result = completePreparedTypeMetadata()
	contract.ServerData = []*genregistry.ToolServerData{nil, {
		Kind: "details", Audience: "internal", Description: new("Details"),
		Schema: []byte(`{ "type": "object" }`), Type: completePreparedTypeMetadata(), NativeImage: true,
	}}
	contract.TextOnly = &genregistry.TextOnlyToolContract{
		Description:   "Read without controls",
		Search:        &genregistry.ToolSearchDocument{Length: 2, Terms: map[string]int{"read": 2}},
		PayloadSchema: []byte(`{ "type": "object" }`), Payload: completePreparedTypeMetadata(),
		ResultReminder: new("Plain result"), ExecutionSchema: []byte(`{ "type": "object" }`),
	}
	return input
}

// completePreparedTypeMetadata uses distinct raw bytes and mutable branch paths
// so the ownership checks can change every nested declaration field.
func completePreparedTypeMetadata() *genregistry.ToolTypeMetadata {
	return &genregistry.ToolTypeMetadata{
		Name: new("Input"), SchemaWithoutRootExample: []byte(`{ "type": "object" }`),
		ExampleJSON: []byte{0, 0xff, 0x80},
		Fields: []*genregistry.ToolFieldMetadata{{
			Path: []*genregistry.ToolFieldPathSegment{
				{Segment: genregistry.NewToolFieldSegmentField("input")},
				{Segment: genregistry.NewToolFieldSegmentElement(new(genregistry.ToolCollectionElement))}, nil,
			},
			JSONType: new("object"), Description: new("Field details"), DiscriminatorValues: []string{"record"},
			Branches: []*genregistry.ToolUnionBranch{nil, {
				Selection: genregistry.NewToolUnionSelectionTagged(&genregistry.ToolTaggedUnionBranch{
					Discriminator: []*genregistry.ToolFieldPathSegment{{Segment: genregistry.NewToolFieldSegmentField("kind")}},
					Value:         "record",
				}),
			}, {
				Selection: genregistry.NewToolUnionSelectionUntagged(&genregistry.ToolUntaggedUnionBranch{
					Path:     []*genregistry.ToolFieldPathSegment{{Segment: genregistry.NewToolFieldSegmentField("value")}},
					JSONKind: "object", Index: 1,
				}),
			}},
		}, nil},
	}
}

// mutatePreparedCatalogResult changes every populated mutable declaration field
// so a later unchanged result proves that the retained value was not shared.
func mutatePreparedCatalogResult(result *genregistry.Toolset) {
	mutateDefinitionResult(result)
	tool := result.Tools[0]
	*tool.Description = "changed description"
	tool.ResultSchema[0], tool.SidecarSchema[0] = '!', '!'
	contract := tool.ConsumerContract
	contract.RequiredLabels[0] = "changed"
	paging := contract.Bounds.Paging
	*paging.ContinueTool, *paging.SourceTool = "changed", "changed"
	paging.RetainedFields[0] = "changed"
	paging.CursorField, paging.NextCursorField = "changed", "changed"
	*contract.Confirmation.Title = "changed"
	contract.Confirmation.PromptTemplate, contract.Confirmation.DeniedResultTemplate = "changed", "changed"
	*contract.ResultReminder = "changed"
	contract.RequiresUI, contract.ReplanOnTimeout = false, false
	mutatePreparedTypeMetadata(contract.Payload)
	mutatePreparedTypeMetadata(contract.Result)
	data := contract.ServerData[1]
	data.Kind, data.Audience, *data.Description = "changed", "changed", "changed"
	data.Schema[0], data.NativeImage = '!', false
	mutatePreparedTypeMetadata(data.Type)
	textOnly := contract.TextOnly
	textOnly.Description, *textOnly.ResultReminder = "changed", "changed"
	textOnly.Search.Length, textOnly.Search.Terms["read"] = 99, 99
	textOnly.PayloadSchema[0], textOnly.ExecutionSchema[0] = '!', '!'
	mutatePreparedTypeMetadata(textOnly.Payload)
}

// mutatePreparedTypeMetadata changes schemas, optional pointers and each branch
// so accidental sharing is visible in the next prepared-declaration result.
func mutatePreparedTypeMetadata(metadata *genregistry.ToolTypeMetadata) {
	*metadata.Name = "Changed"
	metadata.SchemaWithoutRootExample[0], metadata.ExampleJSON[0] = '!', '!'
	field := metadata.Fields[0]
	*field.JSONType, *field.Description = "string", "Changed"
	field.Path[0].Segment.SetField("changed")
	field.Path[1].Segment.SetField("changed collection")
	field.DiscriminatorValues[0] = "changed"
	tagged, _ := field.Branches[1].Selection.AsTagged()
	tagged.Discriminator[0].Segment.SetField("changed")
	tagged.Value = "changed"
	untagged, _ := field.Branches[2].Selection.AsUntagged()
	untagged.Path[0].Segment.SetField("changed")
	untagged.JSONKind, untagged.Index = "string", 99
}
