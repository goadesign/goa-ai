// These tests compare saved identities with explicit admission inputs, rather
// than re-encoding saved contracts through today's generated Go fields.
package admission

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

func TestSavedDeclarationPreservesOriginalContractBytes(t *testing.T) {
	for _, contract := range []string{
		`{"Kind":"service","Title":"Read records"}`,
		`{"Kind":"service","Title":"Read records","RequiresUI":false,"TextOnly":null}`,
		`{ "Title" : "Read \u0072ecords", "Kind" : "service" }`,
		`{"Kind":"service","Title":"Read records","RequiresUI":true,"TextOnly":{"Description":"Ordinary messages"}}`,
		`{"Kind":"service","Meta":{"Audience":["one"],"audience":["two"]}}`,
	} {
		t.Run(contract, func(t *testing.T) {
			raw := []byte(`{"Name":"records","Tools":[{"Name":"records.read","ConsumerContract":` + contract + `}]}`)
			toolset, fingerprint, err := SavedToolsetFingerprint(raw)
			require.NoError(t, err)
			assert.Equal(t, "records", toolset.Name)
			assert.Equal(t, SchemaFingerprint(Schema{Name: "records", Tools: []ToolSchema{{
				Name: "records.read", ConsumerContract: []byte(contract),
			}}}), fingerprint)
			_, contracts, err := parseSavedDeclaration(raw)
			require.NoError(t, err)
			require.Len(t, contracts, 1)
			assert.Equal(t, contract, string(contracts[0]))
		})
	}
}

func TestSavedDeclarationPreservesFieldAndElementUnions(t *testing.T) {
	for _, test := range []struct {
		name    string
		segment string
		kind    genregistry.ToolFieldSegmentKind
	}{
		{name: "field", segment: `{ "value" : "record", "type" : "field" }`, kind: genregistry.ToolFieldSegmentKindField},
		{name: "element", segment: `{ "value" : {}, "type" : "element" }`, kind: genregistry.ToolFieldSegmentKindElement},
	} {
		t.Run(test.name, func(t *testing.T) {
			contract := `{"Kind":"service","Payload":{"Fields":[{"Path":[{"Segment":` + test.segment + `}]}]}}`
			raw := []byte(`{"Name":"records","Tools":[{"Name":"records.read","ConsumerContract":` + contract + `}]}`)
			toolset, fingerprint, err := SavedToolsetFingerprint(raw)
			require.NoError(t, err)
			segment := toolset.Tools[0].ConsumerContract.Payload.Fields[0].Path[0].Segment
			assert.Equal(t, test.kind, segment.Kind())
			field, hasField := segment.AsField()
			element, hasElement := segment.AsElement()
			if test.kind == genregistry.ToolFieldSegmentKindField {
				assert.True(t, hasField)
				assert.Equal(t, genregistry.ToolFieldSegmentBranchField("record"), field)
				assert.False(t, hasElement)
			} else {
				assert.False(t, hasField)
				assert.True(t, hasElement)
				assert.Equal(t, &genregistry.ToolCollectionElement{}, element)
			}
			assert.Equal(t, SchemaFingerprint(Schema{Name: "records", Tools: []ToolSchema{{
				Name: "records.read", ConsumerContract: []byte(contract),
			}}}), fingerprint)
			_, contracts, err := parseSavedDeclaration(raw)
			require.NoError(t, err)
			require.Len(t, contracts, 1)
			assert.Equal(t, contract, string(contracts[0]))
		})
	}
}

func TestSavedDeclarationAlignsIndependentToolContracts(t *testing.T) {
	first := `{"Title":"Second tool","Kind":"service"}`
	second := `{"Kind":"service","Title":"First tool"}`
	raw := []byte(`{"Name":"records","Tools":[{"Name":"records.z","ConsumerContract":` + first + `},{"Name":"records.a","ConsumerContract":` + second + `}]}`)
	_, got, err := SavedToolsetFingerprint(raw)
	require.NoError(t, err)
	assert.Equal(t, SchemaFingerprint(Schema{Name: "records", Tools: []ToolSchema{
		{Name: "records.z", ConsumerContract: []byte(first)},
		{Name: "records.a", ConsumerContract: []byte(second)},
	}}), got)
}

func TestSavedDeclarationMissingAndNullContractMatchNil(t *testing.T) {
	current, err := ToolsetFingerprint(&genregistry.Toolset{
		Name: "records", Tools: []*genregistry.ToolSchema{{Name: "records.read"}},
	})
	require.NoError(t, err)
	for _, member := range []string{"", `,"ConsumerContract":null`, `,"ConsumerContract": null `} {
		toolset, fingerprint, err := SavedToolsetFingerprint([]byte(`{"Name":"records","Tools":[{"Name":"records.read"` + member + `}]}`))
		require.NoError(t, err)
		assert.Nil(t, toolset.Tools[0].ConsumerContract)
		assert.Equal(t, current, fingerprint)
	}
}

func TestSavedDeclarationContractSpansStayAligned(t *testing.T) {
	first := "{ \n\"Title\" : \"Read \\u0072ecords ☃ }\", \"Kind\":\"service\" }"
	last := `{"Kind":"service","Meta":{"Audience":["one"],"audience":["two"]}}`
	raw := []byte(" \n" + `{"Tools":[` +
		`{"ConsumerContract" : ` + first + ` , "Name":"records.first"},` +
		`{"Name":"records.missing"},` +
		`{"Name":"records.null","ConsumerContract": null },` +
		`{"Consumer\u0043ontract": ` + last + `,"Name":"records.last"}` +
		`],"Name":"records"}` + "\n ")
	toolset, contracts, err := parseSavedDeclaration(raw)
	require.NoError(t, err)
	require.Len(t, contracts, 4)
	assert.Equal(t, first, string(contracts[0]))
	assert.Nil(t, contracts[1])
	assert.Nil(t, contracts[2])
	assert.Equal(t, last, string(contracts[3]))
	assert.Equal(t, "Read records ☃ }", toolset.Tools[0].ConsumerContract.Title)
	for _, index := range []int{0, 3} {
		start := bytes.Index(raw, contracts[index])
		require.NotEqual(t, -1, start)
		// The original input owns these bytes; comparing their addresses proves
		// that capturing a contract did not allocate and copy its JSON again.
		assert.Same(t, &raw[start], &contracts[index][0])
	}
	_, got, err := SavedToolsetFingerprint(raw)
	require.NoError(t, err)
	assert.Equal(t, SchemaFingerprint(Schema{Name: "records", Tools: []ToolSchema{
		{Name: "records.first", ConsumerContract: []byte(first)},
		{Name: "records.missing"},
		{Name: "records.null"},
		{Name: "records.last", ConsumerContract: []byte(last)},
	}}), got)
}

func TestSavedDeclarationPreservesGeneratedByteSliceEncodings(t *testing.T) {
	for _, encoded := range []string{`"e30="`, `[123,125]`} {
		t.Run(encoded, func(t *testing.T) {
			contract := `{"Kind":"service","Payload":{"ExampleJSON":` + encoded + `}}`
			raw := []byte(`{"Name":"records","Tools":[{"Name":"records.read","PayloadSchema":` +
				encoded + `,"ConsumerContract":` + contract + `}]}`)
			toolset, fingerprint, err := SavedToolsetFingerprint(raw)
			require.NoError(t, err)
			assert.Equal(t, []byte(`{}`), toolset.Tools[0].PayloadSchema)
			assert.Equal(t, []byte(`{}`), toolset.Tools[0].ConsumerContract.Payload.ExampleJSON)
			assert.Equal(t, SchemaFingerprint(Schema{Name: "records", Tools: []ToolSchema{{
				Name: "records.read", PayloadSchema: []byte(`{}`), ConsumerContract: []byte(contract),
			}}}), fingerprint)
		})
	}
}

func TestSavedDeclarationRejectsAmbiguousAndInvalidJSON(t *testing.T) {
	for _, raw := range []string{
		`{"Name":"records","Name":"other"}`,
		`{"Name":"records","name":"other"}`,
		`{"name":"records"}`,
		`{"Name":"records","Unknown":true}`,
		`{"Name":"records"}{}`,
		`{"Name":"records"} garbage`,
		`{"Name":false}`,
		`{"Tools":{}}`,
		`{"Tools":[null]}`,
		`{"Tools":[{"ConsumerContract":false}]}`,
		`{"Tools":[{"ConsumerContract":{"Kind":"service","Kind":"agent"}}]}`,
		`{"Tools":[{"ConsumerContract":{"Title":"one","title":"two"}}]}`,
		`{"Tools":[{"ConsumerContract":{"RequiresUI":"false"}}]}`,
		`{"Tools":[{"ConsumerContract":{"Meta":{"key":[],"key":[]}}}]}`,
		`{"Name":"records","Na\u006de":"other"}`,
		`{"Tools":[{"ConsumerContract":{"Meta":{"key":[],"k\u0065y":[]}}}]}`,
		`{"Tools":[{"ConsumerContract":{},"Consumer\u0043ontract":null}]}`,
		`{"Tools":[{"PayloadSchema":[256]}]}`,
		`{"Tools":[{"ConsumerContract":{"Payload":{"Fields":[{"Path":[{"Segment":{"value":{"unknown":true},"type":"element"}}]}]}}}]}`,
		`{"Tools":[{"ConsumerContract":{"Payload":{"Fields":[{"Path":[{"Segment":{"value":{},"type":"element","value":{}}}]}]}}}]}`,
		`{"Tools":[{"ConsumerContract":{"Payload":{"Fields":[{"Path":[{"Segment":{"value":{},"Type":"element"}}]}]}}}]}`,
		`{"Tools":[{"ConsumerContract":{"Payload":{"Fields":[{"Path":[{"Segment":{"type":"field","Type":"element","value":"name"}}]}]}}}]}`,
	} {
		t.Run(raw, func(t *testing.T) {
			_, _, err := SavedToolsetFingerprint([]byte(raw))
			assert.Error(t, err)
		})
	}
}

func TestCurrentDeclarationFingerprintRetainsGeneratedEncoding(t *testing.T) {
	contract := &genregistry.ConsumerContract{Kind: "service", Title: "Read records"}
	toolset := &genregistry.Toolset{Name: "records", Tools: []*genregistry.ToolSchema{{Name: "records.read", ConsumerContract: contract}}}
	for _, update := range []func(){
		func() {},
		func() { contract.RequiresUI = true },
		func() { contract.TextOnly = &genregistry.TextOnlyToolContract{Description: "Ordinary messages"} },
	} {
		update()
		body, err := json.Marshal(contract)
		require.NoError(t, err)
		got, err := ToolsetFingerprint(toolset)
		require.NoError(t, err)
		assert.Equal(t, SchemaFingerprint(Schema{Name: "records", Tools: []ToolSchema{{Name: "records.read", ConsumerContract: body}}}), got)
		raw, err := json.Marshal(toolset)
		require.NoError(t, err)
		_, saved, err := SavedToolsetFingerprint(raw)
		require.NoError(t, err)
		assert.Equal(t, got, saved)
		assert.Contains(t, string(body), `"RequiresUI":`)
		assert.Contains(t, string(body), `"TextOnly":`)
	}
	body, err := json.Marshal(toolset)
	require.NoError(t, err)
	_, original, err := SavedToolsetFingerprint(body)
	require.NoError(t, err)
	for _, changed := range []string{
		strings.Replace(string(body), `"Title":"Read records"`, `"Title":"Changed"`, 1),
		strings.Replace(string(body), `"Kind":"service"`, `"Kind" : "service"`, 1),
		strings.Replace(string(body), `"RequiresUI":true`, `"RequiresUI":false`, 1),
		strings.Replace(string(body), `"Description":"Ordinary messages"`, `"Description":"Changed messages"`, 1),
	} {
		_, fingerprint, err := SavedToolsetFingerprint([]byte(changed))
		require.NoError(t, err)
		assert.NotEqual(t, original, fingerprint)
	}
}

// Selected branch metadata must survive the same save-and-read path used by
// registrations, including nested path segments and original contract bytes.
func TestSavedDeclarationPreservesUnionSelections(t *testing.T) {
	for _, test := range []struct {
		name      string
		selection genregistry.ToolUnionSelection
	}{
		{name: "tagged", selection: genregistry.NewToolUnionSelectionTagged(&genregistry.ToolTaggedUnionBranch{
			Discriminator: []*genregistry.ToolFieldPathSegment{{Segment: genregistry.NewToolFieldSegmentField("type")}},
			Value:         "record",
		})},
		{name: "untagged", selection: genregistry.NewToolUnionSelectionUntagged(&genregistry.ToolUntaggedUnionBranch{
			Path:     []*genregistry.ToolFieldPathSegment{{Segment: genregistry.NewToolFieldSegmentElement(&genregistry.ToolCollectionElement{})}},
			JSONKind: "object", Index: 0,
		})},
	} {
		t.Run(test.name, func(t *testing.T) {
			toolset := &genregistry.Toolset{Name: "records", Tools: []*genregistry.ToolSchema{{
				Name: "records.read", ConsumerContract: &genregistry.ConsumerContract{
					Kind: "service", Payload: &genregistry.ToolTypeMetadata{Fields: []*genregistry.ToolFieldMetadata{{
						Branches: []*genregistry.ToolUnionBranch{{Selection: test.selection}},
					}}},
				},
			}}}
			raw, err := json.Marshal(toolset)
			require.NoError(t, err)
			want, err := ToolsetFingerprint(toolset)
			require.NoError(t, err)
			decoded, got, err := SavedToolsetFingerprint(raw)
			require.NoError(t, err)
			assert.Equal(t, want, got)
			assert.Equal(t, toolset, decoded)
			for _, invalid := range []string{
				strings.Replace(string(raw), `"type":`, `"Type":`, 1),
				strings.Replace(string(raw), `"value":`, `"value":{},"value":`, 1),
				strings.Replace(string(raw), `"value":{`, `"value":{"unknown":true,`, 1),
			} {
				_, _, err := SavedToolsetFingerprint([]byte(invalid))
				assert.Error(t, err)
			}
		})
	}
}
