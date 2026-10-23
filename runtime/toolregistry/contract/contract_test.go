package contract

// These tests cover the contract consumed after registry transport validation:
// codecs preserve exact values, metadata stays attached, and server-only data
// cannot change kind or audience after a call has been selected.

import (
	"encoding/json"
	"fmt"
	"strconv"
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
	assert.Equal(t, "text", spec.Payload.Fields[0].Branches[0].(tools.TaggedUnionBranch).Value)
	assert.Equal(t, []tools.FieldPathSegment{tools.FixedField("choice"), tools.FixedField("type")},
		spec.Payload.Fields[0].Branches[0].(tools.TaggedUnionBranch).Discriminator)

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

func TestCompileTimeoutPolicyRequiresAgent(t *testing.T) {
	for _, test := range []struct {
		name    string
		agent   bool
		marked  bool
		invalid bool
	}{
		{name: "marked agent", agent: true, marked: true},
		{name: "unmarked agent", agent: true},
		{name: "service default"},
		{name: "marked service", marked: true, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			declaration := testDeclaration()
			declaration.ConsumerContract.ReplanOnTimeout = test.marked
			if test.agent {
				declaration.ConsumerContract.Kind = agentKind
				declaration.ConsumerContract.Agent = &genregistry.AgentToolTarget{
					Executor: "reader.agent", Workflow: "reader.workflow",
					Queue: "reader.queue", Configuration: "revision/1",
				}
			}
			spec, err := Compile(declaration)
			if test.invalid {
				require.ErrorContains(t, err, "timeout recovery requires an agent tool")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.marked, spec.ReplanOnTimeout)
		})
	}
}

func TestCompilePreservesUntaggedUnionSelection(t *testing.T) {
	for _, root := range []bool{false, true} {
		declaration := testDeclaration()
		var path []*genregistry.ToolFieldPathSegment
		if !root {
			path = []*genregistry.ToolFieldPathSegment{
				{Segment: genregistry.NewToolFieldSegmentField("items")},
				{Segment: genregistry.NewToolFieldSegmentElement(&genregistry.ToolCollectionElement{})},
			}
		}
		declaration.ConsumerContract.Payload.Fields[0].Branches = []*genregistry.ToolUnionBranch{{
			Selection: genregistry.NewToolUnionSelectionUntagged(&genregistry.ToolUntaggedUnionBranch{
				Path: path, JSONKind: "object", Index: 2,
			}),
		}}
		spec, err := Compile(declaration)
		require.NoError(t, err)
		selection, selected := spec.Payload.Fields[0].Branches[0].(tools.UntaggedUnionBranch)
		require.True(t, selected)
		assert.Equal(t, "object", selection.JSONKind)
		assert.Equal(t, 2, selection.Index)
		if root {
			assert.Empty(t, selection.Path)
		} else {
			assert.Equal(t, []tools.FieldPathSegment{tools.FixedField("items"), tools.DynamicField{}}, selection.Path)
		}
	}
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
	for _, kind := range []string{"", agentKind, "control"} {
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
						Selection: genregistry.NewToolUnionSelectionTagged(&genregistry.ToolTaggedUnionBranch{
							Discriminator: []*genregistry.ToolFieldPathSegment{
								{Segment: genregistry.NewToolFieldSegmentField("choice")},
								{Segment: genregistry.NewToolFieldSegmentField("type")},
							},
							Value: "text",
						}),
					}},
				}},
			},
			Result: &genregistry.ToolTypeMetadata{SchemaWithoutRootExample: document},
			Bounds: &genregistry.ToolBounds{Paging: &genregistry.ToolPaging{
				ContinueTool: &continuation, CursorField: "cursor", NextCursorField: "next_cursor",
				RetainedFields: []string{},
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

func TestCompileValidatesRuntimeAuthoredDeclarations(t *testing.T) {
	for _, mutate := range []func(*genregistry.ToolSchema){
		func(d *genregistry.ToolSchema) { d.ConsumerContract.Search = nil },
		func(d *genregistry.ToolSchema) { d.ConsumerContract.Payload = nil },
		func(d *genregistry.ToolSchema) { d.ConsumerContract.Kind = "unknown" },
		func(d *genregistry.ToolSchema) {
			d.ConsumerContract.Kind = agentKind
			d.ConsumerContract.Agent = &genregistry.AgentToolTarget{Executor: "executor"}
		},
	} {
		declaration := testDeclaration()
		mutate(declaration)
		_, err := Compile(declaration)
		require.Error(t, err)
	}
	_, err := Compile(nil)
	require.Error(t, err)
}

// A generated Empty result has no result metadata or codec. Native execution
// must preserve the same zero-byte result contract used by service tools.
func TestCompileNativeEmptyResult(t *testing.T) {
	declaration := testDeclaration()
	declaration.ConsumerContract.Kind = agentKind
	declaration.ConsumerContract.Agent = &genregistry.AgentToolTarget{
		Executor: "records.agent", Configuration: "revision/1",
		Workflow: "records.workflow", Queue: "records.queue",
	}
	declaration.ConsumerContract.Result = nil
	declaration.ResultSchema = []byte(`{"type":"null"}`)
	spec, err := Compile(declaration)
	require.NoError(t, err)
	assert.Nil(t, spec.Result.Codec.FromJSON)
	assert.Nil(t, spec.Result.Codec.ToJSON)
	assert.Equal(t, "records.agent", spec.AgentID)
}

func TestCompileRejectsNullExecutableMetadata(t *testing.T) {
	for _, kind := range []string{"service", agentKind} {
		for _, location := range []struct {
			name string
			set  func(*genregistry.ToolSchema, *genregistry.ToolTypeMetadata)
		}{
			{"payload", func(d *genregistry.ToolSchema, m *genregistry.ToolTypeMetadata) { d.ConsumerContract.Payload = m }},
			{"result", func(d *genregistry.ToolSchema, m *genregistry.ToolTypeMetadata) { d.ConsumerContract.Result = m }},
			{"server_data[0]", func(d *genregistry.ToolSchema, m *genregistry.ToolTypeMetadata) {
				d.ConsumerContract.ServerData[0].Type = m
			}},
			{"text-only arguments", func(d *genregistry.ToolSchema, m *genregistry.ToolTypeMetadata) {
				d.ConsumerContract.TextOnly = &genregistry.TextOnlyToolContract{
					Description: "Read records", Search: d.ConsumerContract.Search,
					PayloadSchema: d.PayloadSchema, ExecutionSchema: d.ExecutionPayloadSchema, Payload: m,
				}
			}},
		} {
			for _, test := range []struct {
				name, want string
				mutate     func(*genregistry.ToolTypeMetadata)
			}{
				{"field", "fields[1]: field metadata is required", func(m *genregistry.ToolTypeMetadata) {
					m.Fields = append(m.Fields, nil)
				}},
				{"path", "fields[0].path: segment[3] is required", func(m *genregistry.ToolTypeMetadata) {
					m.Fields[0].Path = append(m.Fields[0].Path, nil)
				}},
				{"branch", "fields[0].branches[1]: union branch is required", func(m *genregistry.ToolTypeMetadata) {
					m.Fields[0].Branches = append(m.Fields[0].Branches, nil)
				}},
				{"discriminator", "fields[0].branches[0].discriminator: segment[2] is required", func(m *genregistry.ToolTypeMetadata) {
					branch, ok := m.Fields[0].Branches[0].Selection.AsTagged()
					if !ok {
						panic("test fixture requires a tagged branch")
					}
					branch.Discriminator = append(branch.Discriminator, nil)
				}},
				{"untagged path", "fields[0].branches[0].path: segment[1] is required", func(m *genregistry.ToolTypeMetadata) {
					m.Fields[0].Branches = []*genregistry.ToolUnionBranch{{
						Selection: genregistry.NewToolUnionSelectionUntagged(&genregistry.ToolUntaggedUnionBranch{
							JSONKind: "object", Index: 0,
							Path: []*genregistry.ToolFieldPathSegment{{Segment: genregistry.NewToolFieldSegmentField("items")}, nil},
						}),
					}}
				}},
			} {
				t.Run(kind+"/"+location.name+"/"+test.name, func(t *testing.T) {
					declaration := testExecutionDeclaration(kind)
					metadata := testDeclaration().ConsumerContract.Payload
					location.set(declaration, metadata)
					require.NoError(t, genregistry.ValidateToolSchemaValue(declaration))
					test.mutate(metadata)
					require.NoError(t, genregistry.ValidateToolSchemaValue(declaration))
					_, err := Compile(declaration)
					require.ErrorContains(t, err, location.name)
					assert.ErrorContains(t, err, test.want)
				})
			}
		}
		t.Run(kind+"/server data item", func(t *testing.T) {
			declaration := testExecutionDeclaration(kind)
			declaration.ConsumerContract.ServerData = append(declaration.ConsumerContract.ServerData, nil)
			require.NoError(t, genregistry.ValidateToolSchemaValue(declaration))
			_, err := Compile(declaration)
			assert.ErrorContains(t, err, "server_data[1]: item is required")
		})
	}
}

func TestCompileValidatesOriginalIntegers(t *testing.T) {
	for _, kind := range []string{"service", agentKind} {
		for _, value := range []int64{-4294967295, -1, 0, 1, 2147483647, 2147483648, 4294967297} {
			t.Run(fmt.Sprintf("%s/index/%d", kind, value), func(t *testing.T) {
				if strconv.IntSize < 64 && (value < -1<<31 || value >= 1<<31) {
					t.Skip("the original Go int cannot represent this value")
				}
				number := int(value)
				declaration := testExecutionDeclaration(kind)
				declaration.ConsumerContract.Payload.Fields[0].Branches = []*genregistry.ToolUnionBranch{{
					Selection: genregistry.NewToolUnionSelectionUntagged(&genregistry.ToolUntaggedUnionBranch{JSONKind: "object", Index: number}),
				}}
				spec, err := Compile(declaration)
				if number < 0 {
					assert.ErrorContains(t, err, "value.index")
					return
				}
				require.NoError(t, err)
				assert.Equal(t, number, spec.Payload.Fields[0].Branches[0].(tools.UntaggedUnionBranch).Index)
			})
			for _, textOnly := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/search/%d/text-only=%t", kind, value, textOnly), func(t *testing.T) {
					if strconv.IntSize < 64 && (value < -1<<31 || value >= 1<<31) {
						t.Skip("the original Go int cannot represent this value")
					}
					number := int(value)
					declaration := testExecutionDeclaration(kind)
					search := &genregistry.ToolSearchDocument{Length: number, Terms: map[string]int{"records": number}}
					if textOnly {
						declaration.ConsumerContract.TextOnly = &genregistry.TextOnlyToolContract{
							Description: "Read records", Search: search,
							PayloadSchema: declaration.PayloadSchema, ExecutionSchema: declaration.ExecutionPayloadSchema,
							Payload: declaration.ConsumerContract.Payload,
						}
					} else {
						declaration.ConsumerContract.Search = search
					}
					spec, err := Compile(declaration)
					if number < 1 {
						require.ErrorContains(t, err, "value.length")
						assert.ErrorContains(t, err, "value.terms[key]")
						return
					}
					require.NoError(t, err)
					actual := spec.Search
					if textOnly {
						actual = spec.TextOnly.Search
					}
					assert.Equal(t, number, actual.Length)
					assert.Equal(t, number, actual.Terms["records"])
				})
			}
		}
	}
}

func TestCompileRejectsInvalidOriginalText(t *testing.T) {
	invalid := string([]byte{0xff})
	for _, test := range []struct {
		name   string
		mutate func(*genregistry.ToolSchema)
	}{
		{"title", func(d *genregistry.ToolSchema) { d.ConsumerContract.Title = invalid }},
		{"annotation key", func(d *genregistry.ToolSchema) { d.ConsumerContract.Meta[invalid] = []string{"value"} }},
		{"path property", func(d *genregistry.ToolSchema) {
			d.ConsumerContract.Payload.Fields[0].Path[0].Segment = genregistry.NewToolFieldSegmentField(genregistry.ToolFieldSegmentBranchField(invalid))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			declaration := testDeclaration()
			test.mutate(declaration)
			_, err := Compile(declaration)
			assert.ErrorContains(t, err, "invalid UTF-8")
		})
	}
}

func TestCompilePreservesEmptyPathsAndSharedMetadata(t *testing.T) {
	for _, kind := range []string{"service", agentKind} {
		t.Run(kind, func(t *testing.T) {
			declaration := testExecutionDeclaration(kind)
			metadata := declaration.ConsumerContract.Payload
			metadata.Fields = append(metadata.Fields, &genregistry.ToolFieldMetadata{})
			metadata.Fields[0].Path = []*genregistry.ToolFieldPathSegment{
				{Segment: genregistry.NewToolFieldSegmentField("")},
				{Segment: genregistry.NewToolFieldSegmentField("a.b[*]")},
				{Segment: genregistry.NewToolFieldSegmentElement(&genregistry.ToolCollectionElement{})},
			}
			metadata.Fields[0].Branches = append(metadata.Fields[0].Branches, &genregistry.ToolUnionBranch{
				Selection: genregistry.NewToolUnionSelectionUntagged(&genregistry.ToolUntaggedUnionBranch{JSONKind: "object", Index: 0}),
			})
			declaration.ConsumerContract.Result = metadata
			declaration.ConsumerContract.ServerData[0].Type = metadata
			spec, err := Compile(declaration)
			require.NoError(t, err)
			assert.Equal(t, []tools.FieldPathSegment{tools.FixedField(""), tools.FixedField("a.b[*]"), tools.DynamicField{}}, spec.Payload.Fields[0].Path)
			assert.Len(t, spec.Payload.Fields[0].Branches, 2)
			assert.Empty(t, spec.Payload.Fields[0].Branches[1].(tools.UntaggedUnionBranch).Path)
			assert.Empty(t, spec.Payload.Fields[1].Path)
			assert.Equal(t, spec.Payload.Fields, spec.Result.Fields)
			assert.Equal(t, spec.Payload.Fields, spec.ServerData[0].Type.Fields)
			assert.NotNil(t, spec.Bounds.Paging.RetainedFields)
			assert.Empty(t, spec.Bounds.Paging.RetainedFields)

			declaration.ConsumerContract.Payload.Fields = nil
			declaration.ConsumerContract.Result = nil
			declaration.ConsumerContract.ServerData = nil
			declaration.ConsumerContract.Bounds = nil
			_, err = Compile(declaration)
			assert.NoError(t, err)
		})
	}
}

func TestCompileRequiresDeclaredPagingList(t *testing.T) {
	declaration := testDeclaration()
	declaration.ConsumerContract.Bounds.Paging.RetainedFields = nil
	_, err := Compile(declaration)
	assert.ErrorContains(t, err, "retained_fields")
}

// testExecutionDeclaration returns a complete service or Agent declaration so
// the same compiler checks exercise both supported execution kinds.
func testExecutionDeclaration(kind string) *genregistry.ToolSchema {
	declaration := testDeclaration()
	declaration.ConsumerContract.Kind = kind
	if kind == agentKind {
		declaration.ConsumerContract.Agent = &genregistry.AgentToolTarget{
			Executor: "records.agent", Configuration: "revision/1",
			Workflow: "records.workflow", Queue: "records.queue",
		}
	}
	return declaration
}
