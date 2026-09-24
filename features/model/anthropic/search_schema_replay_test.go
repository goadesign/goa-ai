// Schema replay tests keep current declarations and retained native definitions
// consistent across JSON escaping without weakening contract-change detection.
package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestClaudeSearchSchemaEscapingReplay(t *testing.T) {
	for _, description := range []string{"plain", "x > 0", "x < 1", "left & right", "line\u2028paragraph\u2029"} {
		for _, persisted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/persisted=%t", description, persisted), func(t *testing.T) {
				// Keep literal characters in the source schema, as runtime catalog
				// providers may encode JSON with HTML escaping disabled.
				schema := rawjson.Message(`{"type":"object","properties":{"city":{"type":"string","description":"` + description + `"},"count":{"type":"integer","minimum":9007199254740993}},"required":["city"],"additionalProperties":false}`)
				provider, req, initial := retainedSchemaRequest(t, mustAnthropicToolInput(t, schema), persisted)
				replayed, err := provider.encodeRequest(t.Context(), req)
				require.NoError(t, err)
				assert.Equal(t, initial.search.definitions, replayed.search.definitions)
				assert.Empty(t, replayed.search.changes)
				assert.Contains(t, string(replayed.search.definitions["weather_lookup"].InputSchema), "9007199254740993")
				assert.JSONEq(t, string(schema), string(replayed.search.definitions["weather_lookup"].InputSchema))
			})
		}
	}
}

func TestClaudeSearchChangedSchemaStillRejected(t *testing.T) {
	const schema = `{"type":"object","properties":{"city":{"type":"string","description":"x > 0"},"count":{"type":"integer","minimum":9007199254740993}},"required":["city"],"additionalProperties":false}`
	for _, tc := range []struct {
		name, from, to string
	}{
		{"description", "x > 0", "x >= 0"},
		{"exact_integer_constraint", "9007199254740993", "9007199254740992"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, req, _ := retainedSchemaRequest(t, mustAnthropicToolInput(t, rawjson.Message(schema)), true)
			req.Tools[0].Input = mustAnthropicToolInput(t, rawjson.Message(strings.ReplaceAll(schema, tc.from, tc.to)))
			_, err := provider.encodeRequest(t.Context(), req)
			require.ErrorIs(t, err, model.ErrToolSearchUnsupported)
			assert.Contains(t, err.Error(), "conflicts with another definition")
		})
	}
}

func TestClaudeSearchSchemaAndLiftedExampleReplay(t *testing.T) {
	const schema = `{"type":"object","properties":{"city":{"type":"string","description":"Use <city> & country."}},"required":["city"],"additionalProperties":false,"example":{"city":"<Paris> & suburbs"}}`
	input, err := model.ToolInputFromContract("weather.lookup", model.ToolInputContract{
		Schema:                   rawjson.Message(schema),
		SchemaWithoutRootExample: rawjson.Message(`{"type":"object","properties":{"city":{"type":"string","description":"Use <city> & country."}},"required":["city"],"additionalProperties":false}`),
		ExampleJSON:              rawjson.Message(`{"city":"<Paris> & suburbs"}`),
	})
	require.NoError(t, err)
	provider, req, initial := retainedSchemaRequest(t, input, true)
	replayed, err := provider.encodeRequest(t.Context(), req)
	require.NoError(t, err)
	assert.Equal(t, initial.search.definitions, replayed.search.definitions)
	definition := replayed.search.definitions["weather_lookup"]
	assert.NotContains(t, string(definition.InputSchema), `"example"`)
	assert.JSONEq(t, `[{"city":"<Paris> & suburbs"}]`, string(definition.InputExamples))
	assert.Contains(t, string(definition.InputSchema), `\u003ccity\u003e \u0026 country`)
}

// retainedSchemaRequest captures the adapter's own search history and optionally
// sends it through the same JSON boundary used by durable conversation storage.
func retainedSchemaRequest(t *testing.T, input model.ToolInput, persisted bool) (*provider, *model.Request, *encodedRequest) {
	t.Helper()
	raw, err := NewProvider(&stubMessagesClient{}, Options{DefaultModel: "claude-opus-5"})
	require.NoError(t, err)
	client := raw.(*provider)
	req := claudeSearchRequest(t)
	req.Tools[0].Input = input
	initial, err := client.encodeRequest(t.Context(), req)
	require.NoError(t, err)
	var native sdk.Message
	require.NoError(t, json.Unmarshal([]byte(claudeSearchResponseJSON), &native))
	response, err := translateSearchResponse(&native, initial)
	require.NoError(t, err)
	message := response.Content[0]
	if persisted {
		saved, err := json.Marshal(message)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(saved, &message))
	}
	req.Messages = append(req.Messages, &message, &model.Message{
		Role:  model.ConversationRoleUser,
		Parts: []model.Part{model.ToolResultPart{ToolUseID: "toolu_lookup", Content: "sunny"}},
	})
	return client, req, initial
}
