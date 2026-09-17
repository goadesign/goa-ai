// Converse has no deferred-search protocol. A forced tool remains usable
// because the caller has already selected it and no discovery is necessary.
package bedrock

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestDeferredConverseRejectsSearchAndExposesForcedTool(t *testing.T) {
	provider := &provider{defaultModel: "us.anthropic.claude-opus-5"}
	request := &model.Request{
		Messages: []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Finish"}}}},
	}
	for _, name := range []string{"task.finish", "records.lookup"} {
		request.Tools = append(request.Tools, &model.ToolDefinition{
			Name: name, Description: name, Deferred: true,
			Search: tools.NewSearchDocument(name),
			Input:  mustBedrockToolInput(t, rawjson.Message(`{"type":"object"}`)),
		})
	}
	_, err := provider.prepareRequest(request)
	require.ErrorIs(t, err, model.ErrToolSearchUnsupported)
	request.ToolChoice = &model.ToolChoice{Mode: model.ToolChoiceModeTool, Name: "task.finish"}
	prepared, err := provider.prepareRequest(request)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"task_finish": "task.finish"}, prepared.toolNameProvToCanonical)
	assert.Len(t, request.Tools, 2)
	assert.True(t, request.Tools[0].Deferred)
}
