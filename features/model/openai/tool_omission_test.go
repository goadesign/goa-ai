package openai

// These tests check the explanation sent with strict function tools and
// structured responses, and the corresponding decoding. Only null choices added
// by the adapter mean omission; explicit values and original nulls survive.

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials"
	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

// TestEncodeToolsExplainsOptionalFieldOmission checks the tool explanation and
// preserves the distinct meanings of explicit values, nulls, and absence.
func TestEncodeToolsExplainsOptionalFieldOmission(t *testing.T) {
	tests := []struct {
		name         string
		schema       rawjson.Message
		providerArgs string
		wantArgs     string
		wantGuidance bool
	}{
		{
			name: "omitted optional enum",
			schema: rawjson.Message(`{
				"type":"object",
				"properties":{
					"query":{"type":"string"},
					"priority":{"type":"string","enum":["low","high"]}
				},
				"required":["query"],
				"additionalProperties":false
			}`),
			providerArgs: `{"query":"alerts","priority":null}`,
			wantArgs:     `{"query":"alerts"}`,
			wantGuidance: true,
		},
		{
			name: "explicit optional enum",
			schema: rawjson.Message(`{
				"type":"object",
				"properties":{
					"query":{"type":"string"},
					"priority":{"type":"string","enum":["low","high"]}
				},
				"required":["query"],
				"additionalProperties":false
			}`),
			providerArgs: `{"query":"alerts","priority":"high"}`,
			wantArgs:     `{"query":"alerts","priority":"high"}`,
			wantGuidance: true,
		},
		{
			name: "optional field with required meaningful null",
			schema: rawjson.Message(`{
				"type":"object",
				"properties":{
					"priority":{"type":"string","enum":["low","high"]},
					"label":{"type":["string","null"],"description":"Null clears the label."}
				},
				"required":["label"],
				"additionalProperties":false
			}`),
			providerArgs: `{"priority":null,"label":null}`,
			wantArgs:     `{"label":null}`,
			wantGuidance: true,
		},
		{
			name: "nested optional value with meaningful null",
			schema: rawjson.Message(`{
				"type":"object",
				"properties":{
					"filters":{
						"type":"array",
						"items":{
							"type":"object",
							"properties":{
								"priority":{"type":"string","enum":["low","high"]},
								"label":{"type":["string","null"]}
							},
							"required":["label"],
							"additionalProperties":false
						}
					}
				},
				"required":["filters"],
				"additionalProperties":false
			}`),
			providerArgs: `{"filters":[{"priority":null,"label":null},{"priority":"high","label":"review"}]}`,
			wantArgs:     `{"filters":[{"label":null},{"priority":"high","label":"review"}]}`,
			wantGuidance: true,
		},
		{
			name: "required value",
			schema: rawjson.Message(`{
				"type":"object",
				"properties":{"query":{"type":"string"}},
				"required":["query"],
				"additionalProperties":false
			}`),
			providerArgs: `{"query":"alerts"}`,
			wantArgs:     `{"query":"alerts"}`,
		},
		{
			name: "original required null",
			schema: rawjson.Message(`{
				"type":"object",
				"properties":{"label":{"type":["string","null"]}},
				"required":["label"],
				"additionalProperties":false
			}`),
			providerArgs: `{"label":null}`,
			wantArgs:     `{"label":null}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const description = "Find matching records."
			definition := &model.ToolDefinition{
				Name:        "records.find",
				Description: description,
				Input:       mustOpenAIToolInput(tt.schema),
			}

			for _, exact := range []bool{false, true} {
				encoded, codec, err := encodeTools([]*model.ToolDefinition{definition}, "gpt-4o", exact)
				require.NoError(t, err)
				require.Len(t, encoded, 1)
				function := encoded[0].OfFunction
				require.NotNil(t, function)
				assert.Equal(t, !exact, function.Strict.Value)

				wantDescription := description
				if !exact && tt.wantGuidance {
					wantDescription += "\n\nIn the strict schema, an optional field that should be omitted is represented by null. Use null when the instructions say to omit a field."
				}
				assert.Equal(t, wantDescription, function.Description.Value)
				assert.Equal(t, description, definition.Description)
				assert.JSONEq(t, string(tt.schema), string(definition.Input.Contract().Schema))

				if exact {
					parameters, marshalErr := json.Marshal(function.Parameters)
					require.NoError(t, marshalErr)
					assert.JSONEq(t, string(tt.schema), string(parameters))
					continue
				}
				args, err := codec.canonicalPayload(function.Name, []byte(tt.providerArgs))
				require.NoError(t, err)
				assert.JSONEq(t, tt.wantArgs, string(args))
			}
		})
	}
}

// TestStructuredOutputExplainsOptionalFieldOmission checks descriptions in the
// SDK's actual HTTP body and canonical results through all Responses routes.
// In-memory HTTP proves the advertised contract, not model generation quality.
func TestStructuredOutputExplainsOptionalFieldOmission(t *testing.T) {
	tests := []struct {
		name         string
		schema       string
		description  string
		wire         string
		want         string
		wantGuidance bool
	}{
		{
			name:         "optional value omitted",
			schema:       `{"type":"object","properties":{"priority":{"type":"string","enum":["low","high"],"description":"Omit when unknown."}},"additionalProperties":false}`,
			description:  "Describe the record.",
			wire:         `{"priority":null}`,
			want:         `{}`,
			wantGuidance: true,
		},
		{
			name:         "explicit optional value without authored description",
			schema:       `{"type":"object","properties":{"priority":{"type":"string","enum":["low","high"]}},"additionalProperties":false}`,
			wire:         `{"priority":"high"}`,
			want:         `{"priority":"high"}`,
			wantGuidance: true,
		},
		{
			name:         "required meaningful null alongside optional value",
			schema:       `{"type":"object","properties":{"priority":{"type":"string"},"label":{"type":["string","null"],"description":"Null clears the label."}},"required":["label"],"additionalProperties":false}`,
			description:  "Describe the record.",
			wire:         `{"priority":null,"label":null}`,
			want:         `{"label":null}`,
			wantGuidance: true,
		},
		{
			name:        "required value retains authored description",
			schema:      `{"type":"object","properties":{"priority":{"type":"string"}},"required":["priority"],"additionalProperties":false}`,
			description: "Describe the record.",
			wire:        `{"priority":"high"}`,
			want:        `{"priority":"high"}`,
		},
		{
			name:   "required meaningful null without description",
			schema: `{"type":"object","properties":{"label":{"type":["string","null"]}},"required":["label"],"additionalProperties":false}`,
			wire:   `{"label":null}`,
			want:   `{"label":null}`,
		},
	}
	for _, route := range []string{"direct", "bedrock complete schemas", "bedrock strict schemas"} {
		t.Run(route, func(t *testing.T) {
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					calls := 0
					httpClient := &http.Client{Transport: bedrockRoundTripFunc(func(request *http.Request) (*http.Response, error) {
						calls++
						body, err := io.ReadAll(request.Body)
						if err != nil {
							return nil, err
						}
						var params responses.ResponseNewParams
						require.NoError(t, params.UnmarshalJSON(body))
						format := params.Text.Format.OfJSONSchema
						require.NotNil(t, format)
						assert.True(t, format.Strict.Value)
						assert.Equal(t, "record", format.Name)
						if tt.wantGuidance {
							assert.Contains(t, format.Description.Value, "an optional field that should be omitted is represented by null")
							assert.Contains(t, format.Description.Value, "Use null when the instructions say to omit a field.")
							assert.Contains(t, format.Description.Value, tt.description)
						} else {
							assert.Equal(t, tt.description, format.Description.Value)
						}
						if tt.description == "" && !tt.wantGuidance {
							assert.NotContains(t, string(body), `"description"`)
						}
						if tt.name == "optional value omitted" {
							encodedSchema, err := json.Marshal(format.Schema)
							require.NoError(t, err)
							assert.Contains(t, string(encodedSchema), "Omit when unknown.")
						}
						text, err := json.Marshal(tt.wire)
						if err != nil {
							return nil, err
						}
						return bedrockJSONResponse(`{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":` + string(text) + `}]}]}`), nil
					})}
					requestOptions := []option.RequestOption{option.WithMaxRetries(0), option.WithHTTPClient(httpClient)}
					var provider model.Provider
					var err error
					switch route {
					case "direct":
						sdk := openaisdk.NewClient(append(requestOptions, option.WithAPIKey("test-key"))...)
						provider, err = NewProvider(Options{DefaultModel: "gpt-4o", Client: &sdk.Responses})
					case "bedrock complete schemas":
						provider, err = NewBedrockProvider(t.Context(), "us-west-2",
							credentials.NewStaticCredentialsProvider("test-access", "test-secret", "test-session"),
							Options{DefaultModel: bedrockTestModel}, requestOptions...)
					case "bedrock strict schemas":
						provider, err = NewBedrockStrictProvider(t.Context(), "us-west-2",
							credentials.NewStaticCredentialsProvider("test-access", "test-secret", "test-session"),
							Options{DefaultModel: bedrockTestModel}, requestOptions...)
					}
					require.NoError(t, err)
					client, err := model.NewClient(provider)
					require.NoError(t, err)
					output := &model.StructuredOutput{Name: "record", Schema: []byte(tt.schema), Description: tt.description}
					response, err := client.Complete(t.Context(), &model.Request{
						Messages:         []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Describe the record."}}}},
						StructuredOutput: output,
					})
					require.NoError(t, err)
					require.Len(t, response.Content, 1)
					assert.JSONEq(t, tt.want, response.Content[0].Text())
					assert.Equal(t, 1, calls)
					assert.Equal(t, tt.schema, string(output.Schema))
					assert.Equal(t, tt.description, output.Description)
				})
			}
		})
	}
}
