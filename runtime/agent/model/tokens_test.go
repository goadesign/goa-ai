package model

import (
	"context"
	"testing"

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

	annotated := len(input.JSONSchema())
	split := len(input.SchemaWithoutRootExample()) + len(input.ExampleJSON())
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
