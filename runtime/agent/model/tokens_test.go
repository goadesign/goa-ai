package model

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/rawjson"
)

// TestTokenEstimatorCountsLargestToolProjection verifies the estimator charges
// each tool for the largest single provider projection of its input contract —
// either the annotated schema, or the schema-without-root-example plus the
// separate example — rather than summing every projection. Providers send one
// projection per request, so summing all of them inflates estimates for
// tool-heavy requests roughly threefold.
func TestTokenEstimatorCountsLargestToolProjection(t *testing.T) {
	input, err := ToolInputFromContract("lookup", ToolInputContract{
		Schema:                   rawjson.Message(`{"type":"object","properties":{"id":{"type":"string"}},"example":{"id":"abc"}}`),
		SchemaWithoutRootExample: rawjson.Message(`{"type":"object","properties":{"id":{"type":"string"}}}`),
		ExampleJSON:              rawjson.Message(`{"id":"abc"}`),
	})
	require.NoError(t, err)
	tool := &ToolDefinition{
		Name:        "lookup",
		Description: "Looks up data.",
		Input:       input,
	}
	req := &Request{
		Messages: []*Message{
			{
				Role:  ConversationRoleUser,
				Parts: []Part{TextPart{Text: "question"}},
			},
		},
		Tools: []*ToolDefinition{tool},
	}

	inputContract := input.Contract()
	annotated := len(inputContract.Schema)
	split := len(inputContract.SchemaWithoutRootExample) + len(inputContract.ExampleJSON)
	projection := annotated
	if split > projection {
		projection = split
	}
	chars := len(ConversationRoleUser) + len("question") +
		len(tool.Name) + len(tool.Description) + projection

	estimator := TokenEstimator{CharactersPerToken: 1, OverheadTokens: 1}
	count, err := estimator.CountTokens(context.Background(), req)

	require.NoError(t, err)
	require.False(t, count.Exact)
	require.Equal(t, chars+1, count.InputTokens)
}

func TestTokenEstimatorCountsLargestStructuredOutputProjection(t *testing.T) {
	output := &StructuredOutput{
		Name:                     "draft",
		Description:              "Return a draft.",
		Schema:                   []byte(`{"type":"object","example":{"title":"Inspect"},"properties":{"title":{"type":"string"}}}`),
		SchemaWithoutRootExample: []byte(`{"type":"object","properties":{"title":{"type":"string"}}}`),
		ExampleJSON:              rawjson.Message(`{"title":"Inspect"}`),
	}
	req := &Request{
		Messages: []*Message{{
			Role:  ConversationRoleUser,
			Parts: []Part{TextPart{Text: "question"}},
		}},
		StructuredOutput: output,
	}

	annotated := len(output.Schema)
	split := len(output.SchemaWithoutRootExample) + len(output.ExampleJSON)
	projection := max(annotated, split)
	chars := len(ConversationRoleUser) + len("question") +
		len(output.Name) + len(output.Description) + projection

	estimator := TokenEstimator{CharactersPerToken: 1, OverheadTokens: 1}
	count, err := estimator.CountTokens(context.Background(), req)

	require.NoError(t, err)
	require.False(t, count.Exact)
	require.Equal(t, chars+1, count.InputTokens)
}

func TestTokenEstimatorIncludesReplayedThinking(t *testing.T) {
	withoutThinking := &Request{
		Model:      "model-1",
		ModelClass: ModelClassDefault,
		Messages: []*Message{{
			Role:  ConversationRoleAssistant,
			Parts: []Part{TextPart{Text: "visible answer"}},
		}},
	}
	withThinking := &Request{
		Model:      withoutThinking.Model,
		ModelClass: withoutThinking.ModelClass,
		Messages: []*Message{
			{
				Role: ConversationRoleAssistant,
				Parts: []Part{
					ThinkingPart{Text: "private reasoning", Signature: "signature"},
					TextPart{Text: "visible answer"},
				},
			},
			{
				Role:  ConversationRoleAssistant,
				Parts: []Part{ThinkingPart{Text: "thinking-only message"}},
			},
		},
		Thinking: &ThinkingOptions{Enable: true, BudgetTokens: 4096},
	}
	estimator := TokenEstimator{CharactersPerToken: 1, OverheadTokens: 1}

	withoutCount, err := estimator.CountTokens(t.Context(), withoutThinking)
	require.NoError(t, err)
	withCount, err := estimator.CountTokens(t.Context(), withThinking)

	require.NoError(t, err)
	assert.Greater(t, withCount.InputTokens, withoutCount.InputTokens)
}
