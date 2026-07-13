package model

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestMetadataCodecRoundTrip(t *testing.T) {
	metadata := map[string]any{
		"provider_item":   `{"id":"msg_1"}`,
		"reasoning_items": []string{`{"id":"rs_1"}`, `{"id":"rs_2"}`},
		"nested": map[string]any{
			"enabled": true,
			"values":  []any{"first", json.Number("42")},
		},
		"sequence": json.Number("9007199254740993"),
	}

	encoded, err := MarshalMetadata(metadata)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"sequence":9007199254740993`)

	decoded, err := UnmarshalMetadata(encoded)
	require.NoError(t, err)
	providerItem, ok := decoded["provider_item"].(string)
	require.True(t, ok)
	require.JSONEq(t, `{"id":"msg_1"}`, providerItem)
	require.Equal(t, []any{`{"id":"rs_1"}`, `{"id":"rs_2"}`}, decoded["reasoning_items"])
	require.Equal(t, json.Number("9007199254740993"), decoded["sequence"])
	require.Equal(t, map[string]any{
		"enabled": true,
		"values":  []any{"first", json.Number("42")},
	}, decoded["nested"])
}

func TestMetadataCodecCanonicalAbsence(t *testing.T) {
	for _, metadata := range []map[string]any{nil, {}} {
		encoded, err := MarshalMetadata(metadata)
		require.NoError(t, err)
		require.Nil(t, encoded)
	}

	for _, encoded := range []rawjson.Message{nil, rawjson.Message(`{}`)} {
		decoded, err := UnmarshalMetadata(encoded)
		require.NoError(t, err)
		require.Nil(t, decoded)
	}
}

func TestMarshalMetadataRejectsNonJSONValues(t *testing.T) {
	_, err := MarshalMetadata(map[string]any{"invalid": make(chan int)})
	require.ErrorContains(t, err, "model: marshal message metadata")
}

func TestUnmarshalMetadataRejectsInvalidJSON(t *testing.T) {
	tests := []struct {
		name    string
		data    rawjson.Message
		wantErr string
	}{
		{name: "empty", data: rawjson.Message{}, wantErr: "message metadata is empty"},
		{name: "truncated", data: rawjson.Message(`{"a"`), wantErr: "unmarshal message metadata"},
		{name: "trailing", data: rawjson.Message(`{} {}`), wantErr: "rawjson: trailing data"},
		{name: "null", data: rawjson.Message(`null`), wantErr: "must be a JSON object"},
		{name: "array", data: rawjson.Message(`[]`), wantErr: "must be a JSON object"},
		{name: "string", data: rawjson.Message(`"value"`), wantErr: "must be a JSON object"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := UnmarshalMetadata(test.data)
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestPartMarshalJSONIncludesKind(t *testing.T) {
	cases := []struct {
		name string
		part Part
		kind string
	}{
		{
			name: "thinking",
			part: ThinkingPart{
				Text:      "think",
				Signature: "sig",
				Index:     1,
				Final:     true,
			},
			kind: "thinking",
		},
		{name: "text", part: TextPart{Text: "hello"}, kind: "text"},
		{name: "image", part: ImagePart{Format: ImageFormatPNG, Bytes: []byte{0x01}}, kind: "image"},
		{name: "document", part: DocumentPart{Name: "doc", Format: DocumentFormatTXT, Text: "hello"}, kind: "document"},
		{name: "citations", part: CitationsPart{Text: "supported", Citations: []Citation{{Title: "t"}}}, kind: "citations"},
		{name: "tool_use", part: ToolUsePart{ID: "call-1", Name: "search", Input: rawjson.Message(`{"q":"golang"}`)}, kind: "tool_use"},
		{name: "tool_result", part: ToolResultPart{ToolUseID: "tu", Content: map[string]any{"hits": 1}}, kind: "tool_result"},
		{name: "cache_checkpoint", part: CacheCheckpointPart{}, kind: "cache_checkpoint"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(Message{
				Role:  ConversationRoleUser,
				Parts: []Part{tt.part},
			})
			require.NoError(t, err)
			var obj map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &obj))

			var parts []json.RawMessage
			require.NoError(t, json.Unmarshal(obj["parts"], &parts))
			require.Len(t, parts, 1)

			var partObj map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(parts[0], &partObj))

			var kind string
			require.NoError(t, json.Unmarshal(partObj["kind"], &kind))
			require.Equal(t, tt.kind, kind)
		})
	}
}

func TestDecodeMessagePartHonorsKind(t *testing.T) {
	const payload = `{"kind":"tool_use","id":"call-1","name":"search","input":{"z":9007199254740993,"q":"old"}}`
	part, err := decodeMessagePart([]byte(payload))
	require.NoError(t, err)

	tu, ok := part.(ToolUsePart)
	require.True(t, ok)
	require.Equal(t, "search", tu.Name)
	require.Equal(t, `{"z":9007199254740993,"q":"old"}`, string(tu.Input)) //nolint:testifylint // Exact bytes are the contract.
}

func TestMessageJSONRejectsUnknownFields(t *testing.T) {
	var message Message
	err := json.Unmarshal([]byte(`{"role":"user","parts":[],"unknown":true}`), &message)
	require.ErrorContains(t, err, `unknown field "unknown"`)

	_, err = decodeMessagePart([]byte(`{"kind":"text","text":"hello","unknown":true}`))
	require.ErrorContains(t, err, `unknown field "unknown"`)
}

func TestMessageJSONRejectsObsoleteFieldCasing(t *testing.T) {
	var message Message
	err := json.Unmarshal([]byte(`{"Role":"user","Parts":[]}`), &message)
	require.ErrorContains(t, err, `unknown field "Parts"`)

	_, err = decodeMessagePart([]byte(`{"Kind":"text","Text":"hello"}`))
	require.EqualError(t, err, "message part requires kind")
}

func TestMessageJSONRejectsNull(t *testing.T) {
	var message Message
	require.EqualError(t, json.Unmarshal([]byte(`null`), &message), "expected JSON object")
}

func TestMessageUnmarshalPreservesInterfaceValuedNumbers(t *testing.T) {
	const payload = `{
		"role":"user",
		"parts":[{
			"kind":"tool_result",
			"tool_use_id":"call-1",
			"content":{"reading":9007199254740993}
		}],
		"meta":{"sequence":9007199254740995}
	}`

	var message Message
	require.NoError(t, json.Unmarshal([]byte(payload), &message))
	result := message.Parts[0].(ToolResultPart)
	require.Equal(t, json.Number("9007199254740993"), result.Content.(map[string]any)["reading"])
	require.Equal(t, json.Number("9007199254740995"), message.Meta["sequence"])
}

func TestToolInputContractRoundTrip(t *testing.T) {
	orig := ToolInputFromSpec(toolInputSpecFixture())

	contract := orig.Contract()
	got, err := ToolInputFromContract("reports.complete", contract)
	require.NoError(t, err)

	require.Equal(t, orig.JSONSchema(), got.JSONSchema())
	require.Equal(t, orig.SchemaWithoutRootExample(), got.SchemaWithoutRootExample())
	require.Equal(t, orig.ExampleJSON(), got.ExampleJSON())
}

func TestToolInputContractRequiresSchemaWithoutRootExample(t *testing.T) {
	_, err := ToolInputFromContract("reports.complete", ToolInputContract{
		Schema:      rawjson.Message(`{"type":"object"}`),
		ExampleJSON: rawjson.Message(`{"summary":"Done"}`),
	})
	require.ErrorContains(t, err, "example JSON requires schema without root example")
}

func TestThinkingPartRoundTripPreservesSignature(t *testing.T) {
	orig := ThinkingPart{
		Text:      "let me think",
		Signature: "signed-by-provider",
		Redacted:  []byte{0x01, 0x02},
		Index:     3,
		Final:     true,
	}

	raw, err := json.Marshal(struct {
		Kind string `json:"kind"`
		ThinkingPart
	}{Kind: "thinking", ThinkingPart: orig})
	require.NoError(t, err)

	part, err := decodeMessagePart(raw)
	require.NoError(t, err)

	got, ok := part.(ThinkingPart)
	require.True(t, ok)
	require.Equal(t, orig.Text, got.Text)
	require.Equal(t, orig.Signature, got.Signature)
	require.Equal(t, orig.Index, got.Index)
	require.Equal(t, orig.Final, got.Final)
	require.Equal(t, orig.Redacted, got.Redacted)
}

func TestToolUsePartRoundTripPreservesThoughtSignature(t *testing.T) {
	orig := Message{
		Role: ConversationRoleAssistant,
		Parts: []Part{
			ToolUsePart{
				ID:               "tu1",
				Name:             "search",
				Input:            rawjson.Message(`{"q":"golang"}`),
				ThoughtSignature: "opaque-provider-signature",
			},
		},
	}

	raw, err := json.Marshal(orig)
	require.NoError(t, err)

	var got Message
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Len(t, got.Parts, 1)

	tu, ok := got.Parts[0].(ToolUsePart)
	require.True(t, ok)
	require.Equal(t, "search", tu.Name)
	require.Equal(t, "opaque-provider-signature", tu.ThoughtSignature)
	require.JSONEq(t, `{"q":"golang"}`, string(tu.Input))
}

func TestToolUsePartRejectsLegacyArgsField(t *testing.T) {
	_, err := decodeMessagePart([]byte(`{"kind":"tool_use","name":"search","args":{"q":"old"}}`))
	require.ErrorContains(t, err, `unknown field "args"`)
}

func TestToolUsePartRejectsMissingCanonicalFields(t *testing.T) {
	_, err := decodeMessagePart([]byte(`{"kind":"tool_use","name":"search","input":{"q":"old"}}`))
	require.ErrorContains(t, err, "requires id and name")
}

func TestToolUsePartRejectsNonObjectInput(t *testing.T) {
	for _, input := range []string{"null", `[]`, `"value"`} {
		t.Run(input, func(t *testing.T) {
			_, err := decodeMessagePart([]byte(
				`{"kind":"tool_use","id":"call-1","name":"lookup","input":` + input + `}`,
			))
			require.ErrorContains(t, err, "requires input to be a JSON object")
		})
	}
}

func TestToolUsePartMarshalRejectsNonObjectInput(t *testing.T) {
	_, err := json.Marshal(Message{
		Role: ConversationRoleAssistant,
		Parts: []Part{ToolUsePart{
			ID:    "call-1",
			Name:  "lookup",
			Input: rawjson.Message(`null`),
		}},
	})
	require.ErrorContains(t, err, "requires input to be a JSON object")
}

func TestCacheCheckpointPartRoundTrip(t *testing.T) {
	orig := CacheCheckpointPart{}

	raw, err := json.Marshal(Message{
		Role:  ConversationRoleUser,
		Parts: []Part{orig},
	})
	require.NoError(t, err)

	var obj map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &obj))

	var parts []json.RawMessage
	require.NoError(t, json.Unmarshal(obj["parts"], &parts))
	require.Len(t, parts, 1)

	// Verify it emits a kind discriminator, not an empty object.
	require.JSONEq(t, `{"kind":"cache_checkpoint"}`, string(parts[0]))

	part, err := decodeMessagePart(parts[0])
	require.NoError(t, err)

	_, ok := part.(CacheCheckpointPart)
	require.True(t, ok, "expected CacheCheckpointPart, got %T", part)
}

func TestDecodeEmptyObjectReturnsError(t *testing.T) {
	_, err := decodeMessagePart([]byte(`{}`))
	require.EqualError(t, err, "message part requires kind")
}

func TestDocumentPartDecodeRejectsInvalidSources(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{
			name:    "missing_source",
			payload: `{"kind":"document","name":"doc"}`,
		},
		{
			name:    "multiple_sources",
			payload: `{"kind":"document","name":"doc","text":"a","uri":"s3://b/doc.pdf"}`,
		},
		{
			name:    "empty_chunk",
			payload: `{"kind":"document","name":"doc","chunks":[""]}`,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeMessagePart([]byte(tt.payload))
			require.Error(t, err)
		})
	}
}

func TestRequestJSONRoundTripPreservesPromptRefs(t *testing.T) {
	original := &Request{
		PromptRefs: []prompt.PromptRef{
			{
				ID:      "planner.system",
				Version: "v1",
			},
			{
				ID:      "planner.tool",
				Version: "v3",
			},
		},
	}

	raw, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded Request
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, original.PromptRefs, decoded.PromptRefs)
}

func toolInputSpecFixture() tools.TypeSpec {
	return tools.TypeSpec{
		Name:                     "ReportsCompletePayload",
		Schema:                   tools.RawJSON(`{"type":"object","example":{"summary":"Done"}}`),
		SchemaWithoutRootExample: tools.RawJSON(`{"type":"object"}`),
		ExampleJSON:              tools.RawJSON(`{"summary":"Done"}`),
	}
}
