package vertex

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

// toolDef builds a ToolDefinition whose Input carries the given raw JSON
// schema. model.ToolInputFromSchema is for caller-authored schemas (it takes
// a rawjson.Message and returns ToolInput directly, panicking on invalid
// JSON) which fits a test helper better than ToolInputFromSpec, which
// expects a generated tools.TypeSpec.
func toolDef(t *testing.T, name, schema string) *model.ToolDefinition {
	t.Helper()
	input := model.ToolInputFromSchema(rawjson.Message(schema))
	return &model.ToolDefinition{Name: name, Description: "desc for " + name, Input: input}
}

func TestEncodeTools(t *testing.T) {
	defs := []*model.ToolDefinition{
		toolDef(t, "feed/find_duplicates", `{"$schema":"x","type":"object","properties":{"title":{"type":"string"}}}`),
	}
	canonToProv, _ := buildToolNameMaps(defs)
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
	assert.Contains(t, props, "title")
}

func TestEncodeToolsMissingDescription(t *testing.T) {
	defs := []*model.ToolDefinition{
		{Name: "feed/find_duplicates", Input: model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`))},
	}
	canonToProv, _ := buildToolNameMaps(defs)
	tools, err := encodeTools(defs, canonToProv)
	require.Error(t, err)
	assert.Nil(t, tools)
	assert.Contains(t, err.Error(), `"feed/find_duplicates"`)
	assert.Contains(t, err.Error(), "description")
}

func TestNormalizeSchemaMalformedJSON(t *testing.T) {
	schema, err := normalizeSchema([]byte(`{"type":`))
	require.Error(t, err)
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
			cfg := encodeToolConfig(tc.choice, canonToProv)
			require.NotNil(t, cfg)
			require.NotNil(t, cfg.FunctionCallingConfig)
			assert.Equal(t, tc.mode, cfg.FunctionCallingConfig.Mode)
			assert.Equal(t, tc.names, cfg.FunctionCallingConfig.AllowedFunctionNames)
		})
	}
}
