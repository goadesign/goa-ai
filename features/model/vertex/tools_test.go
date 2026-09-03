package vertex

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

// toolDef builds a ToolDefinition whose Input carries the given caller-authored
// raw JSON schema.
func toolDef(t *testing.T, name, schema string) *model.ToolDefinition {
	t.Helper()
	input, err := model.AdvertisedToolInputFromSchema(rawjson.Message(schema))
	require.NoError(t, err)
	return &model.ToolDefinition{Name: name, Description: "desc for " + name, Input: input}
}

func TestEncodeTools(t *testing.T) {
	defs := []*model.ToolDefinition{
		toolDef(t, "feed/find_duplicates", `{
			"$schema":"https://json-schema.org/draft/2020-12/schema",
			"$defs":{
				"Candidate":{
					"title":"Candidate",
					"type":"object",
					"properties":{
						"confidence":{"type":"number","minimum":0,"maximum":1,"example":0.9}
					},
					"required":["confidence"]
				}
			},
			"type":"object",
			"properties":{
				"candidates":{
					"type":"array",
					"items":{
						"oneOf":[
							{"$ref":"#/$defs/Candidate"},
							{"type":"string"}
						]
					},
					"maxItems":20
				}
			}
		}`),
	}
	canonToProv, _, err := buildToolNameMaps(defs)
	require.NoError(t, err)
	tools, err := encodeTools(defs, canonToProv)
	require.NoError(t, err)
	require.Len(t, tools, 1)
	require.Len(t, tools[0].FunctionDeclarations, 1)
	decl := tools[0].FunctionDeclarations[0]
	assert.Equal(t, "feed_find_duplicates", decl.Name)
	assert.Equal(t, "desc for feed/find_duplicates", decl.Description)
	schema, ok := decl.ParametersJsonSchema.(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, schema, "$schema")
	assert.Equal(t, "object", schema["type"])
	props, ok := schema["properties"].(map[string]any)
	require.True(t, ok, "properties must survive normalization")
	candidates := props["candidates"].(map[string]any)
	assert.NotContains(t, candidates, "maxItems")
	items := candidates["items"].(map[string]any)
	assert.NotContains(t, items, "oneOf")
	choices := items["anyOf"].([]any)
	require.Len(t, choices, 2)
	assert.Equal(t, "#/$defs/Candidate", choices[0].(map[string]any)["$ref"])
	assert.Equal(t, "string", choices[1].(map[string]any)["type"])
	definitions := schema["$defs"].(map[string]any)
	candidate := definitions["Candidate"].(map[string]any)
	assert.NotContains(t, candidate, "title")
	confidence := candidate["properties"].(map[string]any)["confidence"].(map[string]any)
	assert.NotContains(t, confidence, "minimum")
	assert.NotContains(t, confidence, "maximum")
	assert.NotContains(t, confidence, "example")
	assert.Equal(t, "number", confidence["type"])
}

func TestEncodeToolsMissingDescription(t *testing.T) {
	defs := []*model.ToolDefinition{
		{Name: "feed/find_duplicates", Input: toolDef(t, "feed/find_duplicates", `{"type":"object"}`).Input},
	}
	canonToProv, _, err := buildToolNameMaps(defs)
	require.NoError(t, err)
	tools, err := encodeTools(defs, canonToProv)
	require.Error(t, err)
	assert.Nil(t, tools)
	assert.Contains(t, err.Error(), `"feed/find_duplicates"`)
	assert.Contains(t, err.Error(), "description")
}

func TestEncodeToolsRejectsMissingProviderName(t *testing.T) {
	defs := []*model.ToolDefinition{
		toolDef(t, "feed/find_duplicates", `{"type":"object"}`),
	}

	tools, err := encodeTools(defs, map[string]string{})
	require.EqualError(t, err, `vertex: tool "feed/find_duplicates" has no provider name`)
	assert.Nil(t, tools)
}

func TestNormalizeSchemaMalformedJSON(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte(`{"type":`)} {
		schema, err := normalizeSchema(raw)
		require.Error(t, err)
		assert.Nil(t, schema)
	}
}

func TestNormalizeSchemaPreservesLargeIntegers(t *testing.T) {
	schema, err := normalizeSchema([]byte(`{"type":"integer","const":9007199254740993}`))
	require.NoError(t, err)
	object := schema.(map[string]any)
	assert.Equal(t, json.Number("9007199254740993"), object["const"])
}

func TestNormalizeToolSchemaRejectsUnknownKeyword(t *testing.T) {
	schema, err := normalizeToolSchema([]byte(`{"type":"object","properties":{"answer":{"type":"string","const":"yes"}}}`))
	require.EqualError(t, err, `gemini tool schema keyword "const" is unsupported`)
	assert.Nil(t, schema)
}

func TestNormalizeToolSchemaRejectsCombinedChoices(t *testing.T) {
	schema, err := normalizeToolSchema([]byte(`{"type":"object","oneOf":[{"type":"string"}],"anyOf":[{"type":"string"}]}`))
	require.EqualError(t, err, `gemini tool schema cannot contain both "oneOf" and "anyOf"`)
	assert.Nil(t, schema)
}

func TestEncodeToolConfig(t *testing.T) {
	canonToProv := map[string]string{"a/b": "a_b"}
	cases := []struct {
		name   string
		choice *model.ToolChoice
		mode   genai.FunctionCallingConfigMode
		names  []string
	}{
		{"nil is auto", nil, genai.FunctionCallingConfigModeAuto, nil},
		{"none", &model.ToolChoice{Mode: model.ToolChoiceModeNone}, genai.FunctionCallingConfigModeNone, nil},
		{"any", &model.ToolChoice{Mode: model.ToolChoiceModeAny}, genai.FunctionCallingConfigModeAny, nil},
		{"tool", &model.ToolChoice{Mode: model.ToolChoiceModeTool, Name: "a/b"}, genai.FunctionCallingConfigModeAny, []string{"a_b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := encodeToolConfig(tc.choice, canonToProv)
			require.NoError(t, err)
			require.NotNil(t, cfg)
			require.NotNil(t, cfg.FunctionCallingConfig)
			assert.Equal(t, tc.mode, cfg.FunctionCallingConfig.Mode)
			assert.Equal(t, tc.names, cfg.FunctionCallingConfig.AllowedFunctionNames)
		})
	}
}

func TestEncodeToolConfigRejectsUndeclaredTool(t *testing.T) {
	cfg, err := encodeToolConfig(
		&model.ToolChoice{Mode: model.ToolChoiceModeTool, Name: "missing/tool"},
		map[string]string{},
	)
	require.EqualError(t, err, `vertex: tool choice "missing/tool" is not declared in the request`)
	assert.Nil(t, cfg)
}
