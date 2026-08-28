// This file verifies that Gemini and Vertex function-call decoding preserves
// an unadvertised name while rejecting the complete response or stream.

package vertex

import (
	"context"
	"errors"
	"io"
	"iter"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
)

type blockingVertexClient struct {
	*stubGenerativeClient
	response *genai.GenerateContentResponse
	blocked  chan struct{}
}

func (c *blockingVertexClient) GenerateContentStream(
	ctx context.Context,
	_ string,
	_ []*genai.Content,
	_ *genai.GenerateContentConfig,
) iter.Seq2[*genai.GenerateContentResponse, error] {
	return func(yield func(*genai.GenerateContentResponse, error) bool) {
		if !yield(c.response, nil) {
			return
		}
		close(c.blocked)
		<-ctx.Done()
		yield(nil, ctx.Err())
	}
}

func TestTranslateResponseMarksUnadvertisedToolName(t *testing.T) {
	response, err := translateResponse(
		&genai.GenerateContentResponse{Candidates: []*genai.Candidate{{
			FinishReason: genai.FinishReasonStop,
			Content: &genai.Content{Parts: []*genai.Part{
				{Text: "partial text"},
				{FunctionCall: &genai.FunctionCall{
					ID:   "call-1",
					Name: "catalog_list_items",
					Args: map[string]any{},
				}},
				{FunctionCall: &genai.FunctionCall{
					ID:   "call-2",
					Name: "catalog_list_nearby",
					Args: map[string]any{"ignored": true},
				}},
			}},
		}}},
		"test-model",
		model.ModelClassDefault,
		map[string]string{"catalog_list_items": "catalog.list_items"},
		nil,
	)

	assert.Nil(t, response)
	name, ok := model.UnadvertisedToolName(err)
	assert.True(t, ok)
	assert.Equal(t, "catalog_list_nearby", name)
	assert.NotContains(t, err.Error(), name)
}

func TestTranslateResponseAcceptsExactAdvertisedToolName(t *testing.T) {
	response, err := translateResponse(
		&genai.GenerateContentResponse{Candidates: []*genai.Candidate{{
			FinishReason: genai.FinishReasonStop,
			Content: &genai.Content{Parts: []*genai.Part{{
				FunctionCall: &genai.FunctionCall{
					ID:   "call-1",
					Name: "catalog_list_items",
					Args: map[string]any{},
				},
			}}},
		}}},
		"test-model",
		model.ModelClassDefault,
		map[string]string{"catalog_list_items": "catalog.list_items"},
		nil,
	)

	require.NoError(t, err)
	require.Len(t, response.ToolCalls(), 1)
	assert.Equal(t, "catalog.list_items", string(response.ToolCalls()[0].Name))
}

func TestStreamMarksUnadvertisedToolName(t *testing.T) {
	providerClient := &stubGenerativeClient{streamChunks: []*genai.GenerateContentResponse{
		{
			Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
				{Text: "partial text"},
				{FunctionCall: &genai.FunctionCall{
					ID:   "call-1",
					Name: "catalog_list_items",
					Args: map[string]any{},
				}},
			}}}},
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
				PromptTokenCount:     3,
				CandidatesTokenCount: 1,
				TotalTokenCount:      4,
			},
		},
		{
			Candidates: []*genai.Candidate{{
				Content: &genai.Content{Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
					ID:   "call-2",
					Name: "catalog_list_nearby",
					Args: map[string]any{"ignored": true},
				}}}},
			}},
		},
		{
			Candidates: []*genai.Candidate{{
				FinishReason: genai.FinishReasonStop,
			}},
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
				PromptTokenCount:     4,
				CandidatesTokenCount: 3,
				TotalTokenCount:      7,
			},
		},
	}}
	client, err := New(providerClient, Options{DefaultModel: "gemini-2.5-flash"})
	require.NoError(t, err)
	streamer, err := client.Stream(context.Background(), &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "list items"}},
		}},
		Tools: []*model.ToolDefinition{
			toolDef(t, "catalog/list_items", `{"type":"object"}`),
		},
	})
	require.NoError(t, err)
	defer func() {
		assert.NoError(t, streamer.Close())
	}()

	var chunks []model.Chunk
	for {
		chunk, recvErr := streamer.Recv()
		if recvErr != nil {
			err = recvErr
			break
		}
		chunks = append(chunks, chunk)
	}

	require.NotErrorIs(t, err, io.EOF)
	var validationErr *model.OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	name, ok := model.UnadvertisedToolName(validationErr)
	assert.True(t, ok)
	assert.Equal(t, "catalog_list_nearby", name)
	assert.NotContains(t, err.Error(), name)
	assert.NotContains(t, errors.Unwrap(validationErr).Error(), name)
	assert.Equal(t, &model.TokenUsage{
		Model:        "gemini-2.5-flash",
		InputTokens:  4,
		OutputTokens: 3,
		TotalTokens:  7,
	}, validationErr.Usage())
	assert.Nil(t, streamer.Response())
	require.Len(t, chunks, 1)
	assert.Equal(t, "partial text", chunks[0].(model.TextChunk).Message.Parts[0].(model.TextPart).Text)
}

func TestStreamTransportFailureSupersedesLatchedUnadvertisedName(t *testing.T) {
	providerClient := &stubGenerativeClient{
		streamChunks: []*genai.GenerateContentResponse{{
			Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{{
				FunctionCall: &genai.FunctionCall{
					ID:   "call-1",
					Name: "catalog_list_nearby",
					Args: map[string]any{},
				},
			}}}}},
		}},
		streamErr: errors.New("connection lost"),
	}
	client, err := New(providerClient, Options{DefaultModel: "gemini-2.5-flash"})
	require.NoError(t, err)
	streamer, err := client.Stream(context.Background(), &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "list items"}},
		}},
		Tools: []*model.ToolDefinition{
			toolDef(t, "catalog/list_items", `{"type":"object"}`),
		},
	})
	require.NoError(t, err)

	_, err = streamer.Recv()

	_, recoverable := model.UnadvertisedToolName(err)
	assert.False(t, recoverable)
	_, providerFailure := model.AsProviderError(err)
	assert.True(t, providerFailure)
	assert.NoError(t, streamer.Close())
}

func TestStreamCancellationSupersedesLatchedUnadvertisedName(t *testing.T) {
	providerClient := &blockingVertexClient{
		stubGenerativeClient: &stubGenerativeClient{},
		response: &genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{{
				FunctionCall: &genai.FunctionCall{
					ID:   "call-1",
					Name: "catalog_list_nearby",
					Args: map[string]any{},
				},
			}}}}},
		},
		blocked: make(chan struct{}),
	}
	client, err := New(providerClient, Options{DefaultModel: "gemini-2.5-flash"})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	streamer, err := client.Stream(ctx, &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "list items"}},
		}},
		Tools: []*model.ToolDefinition{
			toolDef(t, "catalog/list_items", `{"type":"object"}`),
		},
	})
	require.NoError(t, err)
	<-providerClient.blocked
	cancel()
	_, err = streamer.Recv()

	require.ErrorIs(t, err, context.Canceled)
	_, recoverable := model.UnadvertisedToolName(err)
	assert.False(t, recoverable)
	assert.ErrorIs(t, streamer.Close(), context.Canceled)
}
