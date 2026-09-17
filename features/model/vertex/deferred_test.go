// Gemini rejects deferred discovery while retaining ordinary forced tools.
package vertex

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestDeferredGeminiRejectsSearchAndExposesForcedTool(t *testing.T) {
	provider := &provider{opts: Options{DefaultModel: "gemini-2.5-pro"}}
	input, err := model.AdvertisedToolInputFromSchema(rawjson.Message(`{"type":"object"}`))
	require.NoError(t, err)
	request := &model.Request{
		Messages: []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Finish"}}}},
	}
	for _, name := range []string{"task.finish", "records.lookup"} {
		request.Tools = append(request.Tools, &model.ToolDefinition{
			Name: name, Description: name, Deferred: true,
			Search: tools.NewSearchDocument(name), Input: input,
		})
	}
	_, err = provider.prepareRequest(request)
	require.ErrorIs(t, err, model.ErrToolSearchUnsupported)
	request.ToolChoice = &model.ToolChoice{Mode: model.ToolChoiceModeTool, Name: "task.finish"}
	prepared, err := provider.prepareRequest(request)
	require.NoError(t, err)
	require.Len(t, prepared.config.Tools, 1)
	require.Len(t, prepared.config.Tools[0].FunctionDeclarations, 1)
	assert.Equal(t, "task.finish", prepared.config.Tools[0].FunctionDeclarations[0].Name)
	assert.Len(t, request.Tools, 2)
	assert.True(t, request.Tools[0].Deferred)
}
