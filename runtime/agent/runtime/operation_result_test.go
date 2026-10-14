// Completed-operation values retain business data and content while excluding
// observer messages and private failure causes.
package runtime

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genrunoutput "goa.design/goa-ai/registry/gen/runoutput"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/content"
)

func TestOperationResultPreservesExplicitEmptyAndJSON(t *testing.T) {
	for _, original := range []string{"", "null", "{}", "[]", `""`, `{"value":9007199254740993}`} {
		t.Run("result="+original, func(t *testing.T) {
			output := &api.RunOutput{FinalToolResult: &api.ToolEvent{Result: rawjson.Message(original)}}
			value, err := operationResultFromRunOutput(output)
			require.NoError(t, err)
			success, ok := value.Outcome.AsSuccess()
			require.True(t, ok)
			if original == "" {
				_, ok := success.Value.AsEmpty()
				assert.True(t, ok)
			} else {
				raw, ok := success.Value.AsJSON()
				require.True(t, ok)
				assert.Equal(t, original, string(raw))
			}
			encoded, err := genrunoutput.EncodeOperationResult(value)
			require.NoError(t, err)
			decoded, err := genrunoutput.DecodeOperationResult(encoded)
			require.NoError(t, err)
			reencoded, err := genrunoutput.EncodeOperationResult(decoded)
			require.NoError(t, err)
			assert.Equal(t, encoded, reencoded)
		})
	}
}

func TestOperationResultDoesNotExposeObserverOrFailureCause(t *testing.T) {
	output := &api.RunOutput{Final: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "private observer"}}}}
	value, err := operationResultFromRunOutput(output)
	require.NoError(t, err)
	assert.Nil(t, value)
	output.FinalToolResult = &api.ToolEvent{Failure: &planner.ToolFailure{Kind: planner.FailureUnavailable, Error: planner.NewToolErrorWithCause("Unavailable", errors.New("private cause")), Recovery: planner.RecoveryDirective{Action: planner.RecoveryReplan}}, Blocks: content.Blocks{&content.TextContent{Text: "Completed evidence"}}}
	value, err = operationResultFromRunOutput(output)
	require.NoError(t, err)
	failure, ok := value.Outcome.AsFailure()
	require.True(t, ok)
	assert.Equal(t, string(planner.FailureUnavailable), failure.Kind)
	assert.Equal(t, "Unavailable", failure.Message)
	encoded, err := genrunoutput.EncodeOperationResult(value)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "private observer")
	assert.NotContains(t, string(encoded), "private cause")
	assert.NotContains(t, string(encoded), "recovery")
	require.Len(t, failure.Blocks, 1)
	block, ok := failure.Blocks[0].Content.AsText()
	require.True(t, ok)
	assert.Equal(t, "Completed evidence", block.Text)
}

func TestOperationResultBoundsAndContentAreIndependent(t *testing.T) {
	total, cursor := 3, "next"
	output := &api.RunOutput{FinalToolResult: &api.ToolEvent{Result: rawjson.Message(`{"value":1}`), Bounds: &agent.Bounds{Returned: 1, Total: &total, Truncated: true, NextCursor: &cursor}, Blocks: content.Blocks{&content.TextContent{Text: "First", Meta: []byte(`{"extension":true}`)}}}}
	value, err := operationResultFromRunOutput(output)
	require.NoError(t, err)
	success, ok := value.Outcome.AsSuccess()
	require.True(t, ok)
	*success.Bounds.Total = 2
	*success.Bounds.NextCursor = "changed"
	block, ok := success.Blocks[0].Content.AsText()
	require.True(t, ok)
	block.Meta[0] = 'x'
	assert.Equal(t, 3, *output.FinalToolResult.Bounds.Total)
	assert.Equal(t, "next", *output.FinalToolResult.Bounds.NextCursor)
	assert.Equal(t, `{"extension":true}`, string(output.FinalToolResult.Blocks[0].(*content.TextContent).Meta))
}

func TestOperationResultCodecRejectsNullResourceIcon(t *testing.T) {
	output := &api.RunOutput{FinalToolResult: &api.ToolEvent{Blocks: content.Blocks{&content.ResourceLink{Name: "guide", URI: "https://example.test/guide", Icons: []content.Icon{{Src: "https://example.test/icon"}}}}}}
	value, err := operationResultFromRunOutput(output)
	require.NoError(t, err)
	encoded, err := genrunoutput.EncodeOperationResult(value)
	require.NoError(t, err)
	decoded, err := genrunoutput.DecodeOperationResult(encoded)
	require.NoError(t, err)
	assert.Equal(t, value, decoded)
	invalid := bytes.Replace(encoded, []byte(`"icons":[`), []byte(`"icons":[null,`), 1)
	require.NotEqual(t, encoded, invalid)
	_, err = genrunoutput.DecodeOperationResult(invalid)
	assert.Error(t, err)
}
