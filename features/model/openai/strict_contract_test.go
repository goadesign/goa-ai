package openai

// These tests prove that unsupported strict schemas stop before transport and
// that supported provider arguments survive the real codec and validated model
// boundary. All contracts and responses are synthetic.

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestStrictContractRejectsBeforeTransport(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		want   string
	}{
		{
			name:   "optional nullable",
			schema: `{"type":"object","properties":{"label":{"type":["string","null"]}},"additionalProperties":false}`,
			want:   "optional and accepts null",
		},
		{
			name:   "reference derived optional null",
			schema: `{"type":"object","properties":{"label":{"$ref":"#/$defs/Label"}},"additionalProperties":false,"$defs":{"Label":{"anyOf":[{"type":"string"},{"type":"null"}]}}}`,
			want:   "optional and accepts null",
		},
		{
			name:   "optional null only",
			schema: `{"type":"object","properties":{"marker":{"const":null}},"additionalProperties":false}`,
			want:   "optional and accepts null",
		},
		{
			name:   "nullable array item field",
			schema: `{"type":"object","properties":{"rows":{"type":"array","items":{"type":"object","properties":{"label":{"type":["string","null"]}},"additionalProperties":false}}},"required":["rows"],"additionalProperties":false}`,
			want:   "optional and accepts null",
		},
		{
			name:   "implicitly open object",
			schema: `{"type":"object","properties":{"label":{"type":"string"}}}`,
			want:   "must explicitly declare additionalProperties:false",
		},
		{
			name:   "implicitly open referenced object",
			schema: `{"type":"object","properties":{"entry":{"$ref":"#/$defs/Entry"}},"required":["entry"],"additionalProperties":false,"$defs":{"Entry":{"type":"object","properties":{"label":{"type":"string"}}}}}`,
			want:   "must explicitly declare additionalProperties:false",
		},
		{
			name:   "explicitly open object",
			schema: `{"type":"object","additionalProperties":true}`,
			want:   "requires closed objects",
		},
		{
			name:   "pattern omission",
			schema: `{"type":"object","additionalProperties":false,"patternProperties":{"^entry$":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false}}}`,
			want:   "optional-member omission inside patternProperties",
		},
		{
			name:   "pattern forbids named member omission",
			schema: `{"type":"object","properties":{"label":{"type":"string"}},"patternProperties":{"^label$":{"type":"string"}},"additionalProperties":false}`,
			want:   "patternProperties that forbid null",
		},
		{
			name:   "referenced pattern omission",
			schema: `{"type":"object","additionalProperties":false,"patternProperties":{"^entry$":{"$ref":"#/$defs/Entry"}},"$defs":{"Entry":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false}}}`,
			want:   "optional-member omission inside patternProperties",
		},
		{
			name:   "direct reference cycle",
			schema: `{"type":"object","properties":{"head":{"$ref":"#/$defs/Node"}},"required":["head"],"additionalProperties":false,"$defs":{"Node":{"type":"object","properties":{"next":{"$ref":"#/$defs/Node"}},"additionalProperties":false}}}`,
			want:   "reference cycle",
		},
		{
			name:   "array reference cycle",
			schema: `{"type":"object","properties":{"head":{"$ref":"#/$defs/Node"}},"required":["head"],"additionalProperties":false,"$defs":{"Node":{"type":"object","properties":{"children":{"type":"array","items":{"$ref":"#/$defs/Node"}}},"required":["children"],"additionalProperties":false}}}`,
			want:   "reference cycle",
		},
		{
			name:   "mutual reference cycle",
			schema: `{"type":"object","properties":{"head":{"$ref":"#/$defs/First"}},"required":["head"],"additionalProperties":false,"$defs":{"First":{"type":"object","properties":{"next":{"$ref":"#/$defs/Second"}},"additionalProperties":false},"Second":{"type":"object","properties":{"next":{"$ref":"#/$defs/First"}},"additionalProperties":false}}}`,
			want:   "reference cycle",
		},
		{
			name:   "constant object without explicit closure",
			schema: `{"type":"object","properties":{"label":{"type":"string"}},"required":["label"],"const":{"label":"ready"}}`,
			want:   "does not infer concrete object closure",
		},
		{
			name:   "local properties with closed union branches",
			schema: `{"type":"object","properties":{"choice":{"type":"object","properties":{"kind":{"type":"string"}},"required":["kind"],"anyOf":[{"type":"object","properties":{"kind":{"const":"number"},"value":{"type":"integer"}},"required":["kind","value"],"additionalProperties":false},{"type":"object","properties":{"kind":{"const":"text"},"label":{"type":"string"}},"required":["kind","label"],"additionalProperties":false}]}},"required":["choice"],"additionalProperties":false}`,
			want:   "does not support mixed object definitions",
		},
		{
			name:   "local required with complete union branches",
			schema: `{"type":"object","properties":{"choice":{"type":"object","required":["kind"],"anyOf":[{"type":"object","properties":{"kind":{"const":"number"},"value":{"type":"integer"}},"required":["kind","value"],"additionalProperties":false},{"type":"object","properties":{"kind":{"const":"text"},"label":{"type":"string"}},"required":["kind","label"],"additionalProperties":false}]}},"required":["choice"],"additionalProperties":false}`,
			want:   "does not support mixed object definitions",
		},
		{
			name:   "reference with additional properties sibling",
			schema: `{"type":"object","properties":{"entry":{"$ref":"#/$defs/Entry","additionalProperties":false}},"required":["entry"],"additionalProperties":false,"$defs":{"Entry":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false}}}`,
			want:   "structural sibling",
		},
		{
			name:   "reference with redundant required sibling",
			schema: `{"type":"object","properties":{"entry":{"$ref":"#/$defs/Entry","required":["label"]}},"required":["entry"],"additionalProperties":false,"$defs":{"Entry":{"type":"object","properties":{"label":{"type":"string"}},"required":["label"],"additionalProperties":false}}}`,
			want:   "does not support reference structural siblings",
		},
		{
			name:   "reference with structural sibling",
			schema: `{"type":"object","properties":{"entry":{"$ref":"#/$defs/Entry","required":["label"]}},"required":["entry"],"additionalProperties":false,"$defs":{"Entry":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false}}}`,
			want:   "structural sibling",
		},
		{
			name:   "object const omitted",
			schema: `{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false,"const":{}}`,
			want:   "does not support this literal and omission composition",
		},
		{
			name:   "array const omitted",
			schema: `{"type":"object","properties":{"value":{"type":"array","items":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false},"const":[{}]}},"required":["value"],"additionalProperties":false}`,
			want:   "does not support this literal and omission composition",
		},
		{
			name:   "ref sibling const omitted",
			schema: `{"type":"object","properties":{"value":{"$ref":"#/$defs/Entry","const":{}}},"required":["value"],"additionalProperties":false,"$defs":{"Entry":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false}}}`,
			want:   "does not support this literal and omission composition",
		},
		{
			name:   "ref target const omitted",
			schema: `{"type":"object","properties":{"value":{"$ref":"#/$defs/Entry"}},"required":["value"],"additionalProperties":false,"$defs":{"Entry":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false,"const":{}}}}`,
			want:   "does not support this literal and omission composition",
		},
		{
			name:   "object const optional full",
			schema: `{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false,"const":{"label":"ready"}}`,
			want:   "does not support this literal and omission composition",
		},
		{
			name:   "array const optional full",
			schema: `{"type":"object","properties":{"value":{"type":"array","items":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false},"const":[{"label":"ready"}]}},"required":["value"],"additionalProperties":false}`,
			want:   "does not support this literal and omission composition",
		},
		{
			name:   "array const empty",
			schema: `{"type":"object","properties":{"value":{"type":"array","items":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false},"const":[]}},"required":["value"],"additionalProperties":false}`,
			want:   "does not support this literal and omission composition",
		},
		{
			name:   "ref sibling const optional full",
			schema: `{"type":"object","properties":{"value":{"$ref":"#/$defs/Entry","const":{"label":"ready"}}},"required":["value"],"additionalProperties":false,"$defs":{"Entry":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false}}}`,
			want:   "does not support this literal and omission composition",
		},
		{
			name:   "object enum omitted",
			schema: `{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false,"enum":[{}]}`,
			want:   "does not support this literal and omission composition",
		},
		{
			name:   "array enum omitted",
			schema: `{"type":"object","properties":{"value":{"type":"array","items":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false},"enum":[[{}]]}},"required":["value"],"additionalProperties":false}`,
			want:   "does not support this literal and omission composition",
		},
		{
			name:   "ref sibling enum omitted",
			schema: `{"type":"object","properties":{"value":{"$ref":"#/$defs/Entry","enum":[{}]}},"required":["value"],"additionalProperties":false,"$defs":{"Entry":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false}}}`,
			want:   "does not support this literal and omission composition",
		},
		{
			name:   "ref target enum omitted",
			schema: `{"type":"object","properties":{"value":{"$ref":"#/$defs/Entry"}},"required":["value"],"additionalProperties":false,"$defs":{"Entry":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false,"enum":[{}]}}}`,
			want:   "does not support this literal and omission composition",
		},
		{
			name:   "object enum optional full",
			schema: `{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false,"enum":[{"label":"ready"}]}`,
			want:   "does not support this literal and omission composition",
		},
		{
			name:   "array enum optional full",
			schema: `{"type":"object","properties":{"value":{"type":"array","items":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false},"enum":[[{"label":"ready"}]]}},"required":["value"],"additionalProperties":false}`,
			want:   "does not support this literal and omission composition",
		},
		{
			name:   "array enum empty",
			schema: `{"type":"object","properties":{"value":{"type":"array","items":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false},"enum":[[]]}},"required":["value"],"additionalProperties":false}`,
			want:   "does not support this literal and omission composition",
		},
		{
			name:   "ref sibling enum optional full",
			schema: `{"type":"object","properties":{"value":{"$ref":"#/$defs/Entry","enum":[{"label":"ready"}]}},"required":["value"],"additionalProperties":false,"$defs":{"Entry":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false}}}`,
			want:   "does not support this literal and omission composition",
		},
		{
			name:   "object enum mixed",
			schema: `{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false,"enum":[{},{"label":"ready"}]}`,
			want:   "does not support this literal and omission composition",
		},
		{
			name:   "literal on cached reference after plain branch",
			schema: `{"type":"object","properties":{"choice":{"anyOf":[{"type":"object","properties":{"kind":{"const":"plain"},"entry":{"$ref":"#/$defs/Entry"}},"required":["kind","entry"],"additionalProperties":false},{"type":"object","properties":{"kind":{"const":"literal"},"entry":{"$ref":"#/$defs/Entry","const":{}}},"required":["kind","entry"],"additionalProperties":false}]}},"required":["choice"],"additionalProperties":false,"$defs":{"Entry":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false}}}`,
			want:   "does not support this literal and omission composition",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			definition := &model.ToolDefinition{
				Name: "records.inspect", Description: "Inspect a record.",
				Input: mustOpenAIToolInput(rawjson.Message(tt.schema)),
			}
			_, _, err := encodeTools([]*model.ToolDefinition{definition}, "gpt-4o", false)
			require.ErrorContains(t, err, tt.want)
			// Complete-schema tools still carry the caller's original contract.
			_, _, err = encodeTools([]*model.ToolDefinition{definition}, "gpt-4o", true)
			require.NoError(t, err)
			for _, structured := range []bool{false, true} {
				t.Run(fmt.Sprintf("structured=%t", structured), func(t *testing.T) {
					transport := &mockTransport{}
					client, err := New(Options{DefaultModel: "gpt-4o", transport: transport})
					require.NoError(t, err)
					request := &model.Request{
						Messages: []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Inspect the record."}}}},
					}
					if structured {
						request.StructuredOutput = &model.StructuredOutput{Name: "record", Schema: []byte(tt.schema)}
					} else {
						request.Tools = []*model.ToolDefinition{definition}
					}
					_, err = client.Complete(t.Context(), request)
					require.ErrorContains(t, err, tt.want)
					_, err = client.Stream(t.Context(), request)
					require.ErrorContains(t, err, tt.want)
					assert.Empty(t, transport.completeRequests)
					assert.Empty(t, transport.streamRequests)
				})
			}
		})
	}
}

func TestStrictContractPreservesValues(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		wire   string
		want   string
	}{
		{
			name:   "false zero and empty values",
			schema: `{"type":"object","properties":{"flag":{"type":"boolean"},"count":{"type":"integer"},"label":{"type":"string"},"items":{"type":"array","items":{"type":"string"}},"config":{"type":"object","additionalProperties":false},"skip":{"type":"string"}},"additionalProperties":false}`,
			wire:   `{"flag":false,"count":0,"label":"","items":[],"config":{},"skip":null}`,
			want:   `{"flag":false,"count":0,"label":"","items":[],"config":{}}`,
		},
		{
			name:   "required meaningful nulls",
			schema: `{"type":"object","properties":{"label":{"type":["string","null"]},"marker":{"const":null},"skip":{"type":"string"}},"required":["label","marker"],"additionalProperties":false}`,
			wire:   `{"label":null,"marker":null,"skip":null}`,
			want:   `{"label":null,"marker":null}`,
		},
		{
			name:   "reference derived required null",
			schema: `{"type":"object","properties":{"label":{"$ref":"#/$defs/Label"}},"required":["label"],"additionalProperties":false,"$defs":{"Label":{"anyOf":[{"type":"string"},{"type":"null"}]}}}`,
			wire:   `{"label":null}`,
			want:   `{"label":null}`,
		},
		{
			name:   "nullable type constrained by enum",
			schema: `{"type":"object","properties":{"label":{"type":["string","null"],"enum":["ready"]}},"additionalProperties":false}`,
			wire:   `{"label":null}`,
			want:   `{}`,
		},
		{
			name:   "reused reference at optional and required sites",
			schema: `{"type":"object","properties":{"primary":{"$ref":"#/$defs/Entry"},"secondary":{"$ref":"#/$defs/Entry"},"unused":{"$ref":"#/$defs/Entry"}},"required":["primary"],"additionalProperties":false,"$defs":{"Entry":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false}}}`,
			wire:   `{"primary":{"label":null},"secondary":{"label":""},"unused":null}`,
			want:   `{"primary":{},"secondary":{"label":""}}`,
		},
		{
			name:   "typed closed reference wrapper",
			schema: `{"type":"object","properties":{"entry":{"type":"object","$ref":"#/$defs/Entry"}},"required":["entry"],"additionalProperties":false,"$defs":{"Entry":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false}}}`,
			wire:   `{"entry":{"label":null}}`,
			want:   `{"entry":{}}`,
		},
		{
			name:   "complete closed number branch",
			schema: `{"type":"object","properties":{"choice":{"type":"object","anyOf":[{"type":"object","properties":{"kind":{"const":"number"},"value":{"type":"integer"}},"required":["kind","value"],"additionalProperties":false},{"type":"object","properties":{"kind":{"const":"text"},"label":{"type":"string"}},"required":["kind","label"],"additionalProperties":false}]}},"required":["choice"],"additionalProperties":false}`,
			wire:   `{"choice":{"kind":"number","value":5}}`,
			want:   `{"choice":{"kind":"number","value":5}}`,
		},
		{
			name:   "complete closed text branch",
			schema: `{"type":"object","properties":{"choice":{"type":"object","anyOf":[{"type":"object","properties":{"kind":{"const":"number"},"value":{"type":"integer"}},"required":["kind","value"],"additionalProperties":false},{"type":"object","properties":{"kind":{"const":"text"},"label":{"type":"string"}},"required":["kind","label"],"additionalProperties":false}]}},"required":["choice"],"additionalProperties":false}`,
			wire:   `{"choice":{"kind":"text","label":""}}`,
			want:   `{"choice":{"kind":"text","label":""}}`,
		},
		{
			name:   "closed typed union wrapper",
			schema: `{"type":"object","properties":{"choice":{"type":"object","oneOf":[{"type":"object","properties":{"kind":{"const":"optional"},"value":{"type":"boolean"}},"required":["kind"],"additionalProperties":false},{"type":"object","properties":{"kind":{"const":"nullable"},"value":{"type":["boolean","null"]}},"required":["kind","value"],"additionalProperties":false}]}},"required":["choice"],"additionalProperties":false}`,
			wire:   `{"choice":{"kind":"optional","value":null}}`,
			want:   `{"choice":{"kind":"optional"}}`,
		},
		{
			name:   "union meaningful null",
			schema: `{"type":"object","properties":{"choice":{"oneOf":[{"type":"object","properties":{"kind":{"const":"optional"},"value":{"type":"boolean"}},"required":["kind"],"additionalProperties":false},{"type":"object","properties":{"kind":{"const":"nullable"},"value":{"type":["boolean","null"]}},"required":["kind","value"],"additionalProperties":false}]}},"required":["choice"],"additionalProperties":false}`,
			wire:   `{"choice":{"kind":"nullable","value":null}}`,
			want:   `{"choice":{"kind":"nullable","value":null}}`,
		},
		{
			name:   "pattern meaningful null and explicit values",
			schema: `{"type":"object","additionalProperties":false,"patternProperties":{"^entry[0-9]+$":{"type":"object","properties":{"label":{"type":["string","null"]}},"required":["label"],"additionalProperties":false}}}`,
			wire:   `{"entry1":{"label":null},"entry2":{"label":""}}`,
			want:   `{"entry1":{"label":null},"entry2":{"label":""}}`,
		},
		{
			name:   "pattern permits named member omission",
			schema: `{"type":"object","properties":{"label":{"type":"string"}},"patternProperties":{"^label$":{"type":["string","null"]}},"additionalProperties":false}`,
			wire:   `{"label":null}`,
			want:   `{}`,
		},
		{
			name:   "null array item remains a value",
			schema: `{"type":"object","properties":{"items":{"type":"array","items":{"type":["string","null"]}}},"additionalProperties":false}`,
			wire:   `{"items":[null,""]}`,
			want:   `{"items":[null,""]}`,
		},
		{
			name:   "object const required full",
			schema: `{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false,"required":["label"],"const":{"label":"ready"}}`,
			wire:   `{"label":"ready"}`,
			want:   `{"label":"ready"}`,
		},
		{
			name:   "ref sibling const required full",
			schema: `{"type":"object","properties":{"value":{"$ref":"#/$defs/Entry","const":{"label":"ready"}}},"required":["value"],"additionalProperties":false,"$defs":{"Entry":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false,"required":["label"]}}}`,
			wire:   `{"value":{"label":"ready"}}`,
			want:   `{"value":{"label":"ready"}}`,
		},
		{
			name:   "object enum required full",
			schema: `{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false,"required":["label"],"enum":[{"label":"ready"}]}`,
			wire:   `{"label":"ready"}`,
			want:   `{"label":"ready"}`,
		},
		{
			name:   "ref sibling enum required full",
			schema: `{"type":"object","properties":{"value":{"$ref":"#/$defs/Entry","enum":[{"label":"ready"}]}},"required":["value"],"additionalProperties":false,"$defs":{"Entry":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false,"required":["label"]}}}`,
			wire:   `{"value":{"label":"ready"}}`,
			want:   `{"value":{"label":"ready"}}`,
		},
		{
			name:   "scalar const with unrelated omission",
			schema: `{"type":"object","properties":{"kind":{"const":"ready"},"skip":{"type":"string"}},"required":["kind"],"additionalProperties":false}`,
			wire:   `{"kind":"ready","skip":null}`,
			want:   `{"kind":"ready"}`,
		},
		{
			name:   "ref scalar enum with unrelated omission",
			schema: `{"type":"object","properties":{"kind":{"$ref":"#/$defs/Kind","enum":["ready"]},"skip":{"type":"string"}},"required":["kind"],"additionalProperties":false,"$defs":{"Kind":{"type":"string"}}}`,
			wire:   `{"kind":"ready","skip":null}`,
			want:   `{"kind":"ready"}`,
		},
		{
			name:   "complete array const",
			schema: `{"type":"object","properties":{"value":{"type":"array","items":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false,"required":["label"]},"const":[{"label":"ready"}]}},"required":["value"],"additionalProperties":false}`,
			wire:   `{"value":[{"label":"ready"}]}`,
			want:   `{"value":[{"label":"ready"}]}`,
		},
		{
			name:   "complete array enum",
			schema: `{"type":"object","properties":{"value":{"type":"array","items":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false,"required":["label"]},"enum":[[{"label":"ready"}]]}},"required":["value"],"additionalProperties":false}`,
			wire:   `{"value":[{"label":"ready"}]}`,
			want:   `{"value":[{"label":"ready"}]}`,
		},
		{
			name:   "optional parent of complete constant omitted",
			schema: `{"type":"object","properties":{"value":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false,"required":["label"],"const":{"label":"ready"}}},"additionalProperties":false}`,
			wire:   `{"value":null}`,
			want:   `{}`,
		},
		{
			name:   "optional parent of complete constant present",
			schema: `{"type":"object","properties":{"value":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false,"required":["label"],"const":{"label":"ready"}}},"additionalProperties":false}`,
			wire:   `{"value":{"label":"ready"}}`,
			want:   `{"value":{"label":"ready"}}`,
		},
		{
			name:   "optional parent of complete reference constant",
			schema: `{"type":"object","properties":{"value":{"$ref":"#/$defs/Entry","const":{"label":"ready"}}},"additionalProperties":false,"$defs":{"Entry":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false,"required":["label"]}}}`,
			wire:   `{"value":null}`,
			want:   `{}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			definition := &model.ToolDefinition{
				Name: "records.inspect", Description: "Inspect a record.",
				Input: mustOpenAIToolInput(rawjson.Message(tt.schema)),
			}
			encoded, codec, err := encodeTools([]*model.ToolDefinition{definition}, "gpt-4o", false)
			require.NoError(t, err)
			name := encoded[0].OfFunction.Name
			compiler := jsonschema.NewCompiler()
			require.NoError(t, compiler.AddResource("schema://test/wire.json", codec.projections[name].schema))
			wireSchema, err := compiler.Compile("schema://test/wire.json")
			require.NoError(t, err)
			var wire any
			require.NoError(t, json.Unmarshal([]byte(tt.wire), &wire))
			require.NoError(t, wireSchema.Validate(wire))
			decoded, err := codec.canonicalPayload(name, []byte(tt.wire))
			require.NoError(t, err)
			assert.JSONEq(t, tt.want, string(decoded))
			responseJSON, err := json.Marshal(map[string]any{
				"model": "gpt-4o", "status": "completed",
				"output": []map[string]any{{
					"id": "fc_1", "type": "function_call", "call_id": "call_1",
					"name": name, "arguments": tt.wire, "status": "completed",
				}},
			})
			require.NoError(t, err)
			transport := &mockTransport{completeResponse: mustResponse(t, string(responseJSON))}
			client, err := New(Options{DefaultModel: "gpt-4o", transport: transport})
			require.NoError(t, err)
			response, err := client.Complete(t.Context(), &model.Request{
				Messages: []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Inspect the record."}}}},
				Tools:    []*model.ToolDefinition{definition},
			})
			require.NoError(t, err)
			require.Len(t, response.ToolCalls(), 1)
			assert.JSONEq(t, tt.want, string(response.ToolCalls()[0].Payload))
			assert.Len(t, transport.completeRequests, 1)
			assert.JSONEq(t, tt.schema, string(definition.Input.Contract().Schema))
		})
	}
}
