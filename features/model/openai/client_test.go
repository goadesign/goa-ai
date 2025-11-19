package openai_test

import (
	"context"
	"encoding/json"
	"testing"

	openai "github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"

	openaimodel "goa.design/goa-ai/features/model/openai"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestClientComplete(t *testing.T) {
	mock := &mockChatClient{}
	client, err := openaimodel.New(openaimodel.Options{Client: mock, DefaultModel: "gpt-4o"})
	require.NoError(t, err)

	mock.response = openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{
			{
				FinishReason: "stop",
				Message: openai.ChatCompletionMessage{
					Role:    "assistant",
					Content: "hi there",
					ToolCalls: []openai.ToolCall{
						{
							Function: openai.FunctionCall{
								Name:      "lookup",
								Arguments: `{"query":"docs"}`,
							},
						},
					},
				},
			},
		},
		Usage: openai.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}

	resp, err := client.Complete(context.Background(), model.Request{
		Messages: []*model.Message{{Role: "user", Parts: []model.Part{model.TextPart{Text: "ping"}}}},
		Tools: []*model.ToolDefinition{{
			Name:        "lookup",
			Description: "Search",
			InputSchema: map[string]any{"type": "object"},
		}},
	})
	require.NoError(t, err)
	require.Len(t, resp.Content, 1)
	// Extract text from parts
	found := false
	for _, p := range resp.Content[0].Parts {
		if tp, ok := p.(model.TextPart); ok && tp.Text == "hi there" {
			found = true
			break
		}
	}
	require.True(t, found, "expected hi there text part")
	require.Equal(t, tools.Ident("lookup"), resp.ToolCalls[0].Name)
	require.Equal(t, "docs", resp.ToolCalls[0].Payload.(map[string]any)["query"])
	require.Equal(t, "stop", resp.StopReason)
	require.Equal(t, 15, resp.Usage.TotalTokens)

	req := mock.captured
	require.Equal(t, "gpt-4o", req.Model)
	require.Len(t, req.Messages, 1)
	require.Equal(t, "ping", req.Messages[0].Content)
	require.Len(t, req.Tools, 1)
	require.Equal(t, openai.ToolTypeFunction, req.Tools[0].Type)
	params, ok := req.Tools[0].Function.Parameters.(json.RawMessage)
	require.True(t, ok)
	require.JSONEq(t, `{"type":"object"}`, string(params))
}

func TestClientCompleteWithToolChoiceTool(t *testing.T) {
	mock := &mockChatClient{}
	client, err := openaimodel.New(openaimodel.Options{
		Client:       mock,
		DefaultModel: "gpt-4o",
	})
	require.NoError(t, err)

	mock.response = openai.ChatCompletionResponse{}

	_, err = client.Complete(context.Background(), model.Request{
		Messages: []*model.Message{
			{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: "ping"}},
			},
		},
		Tools: []*model.ToolDefinition{
			{
				Name:        "lookup",
				Description: "Search",
				InputSchema: map[string]any{"type": "object"},
			},
		},
		ToolChoice: &model.ToolChoice{
			Mode: model.ToolChoiceModeTool,
			Name: "lookup",
		},
	})
	require.NoError(t, err)

	req := mock.captured
	tc, ok := req.ToolChoice.(openai.ToolChoice)
	require.True(t, ok, "expected ToolChoice object")
	require.Equal(t, openai.ToolTypeFunction, tc.Type)
	require.Equal(t, "lookup", tc.Function.Name)
}

func TestClientCompleteWithToolChoiceNone(t *testing.T) {
	mock := &mockChatClient{}
	client, err := openaimodel.New(openaimodel.Options{
		Client:       mock,
		DefaultModel: "gpt-4o",
	})
	require.NoError(t, err)

	mock.response = openai.ChatCompletionResponse{}

	_, err = client.Complete(context.Background(), model.Request{
		Messages: []*model.Message{
			{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: "ping"}},
			},
		},
		Tools: []*model.ToolDefinition{
			{
				Name:        "lookup",
				Description: "Search",
				InputSchema: map[string]any{"type": "object"},
			},
		},
		ToolChoice: &model.ToolChoice{
			Mode: model.ToolChoiceModeNone,
		},
	})
	require.NoError(t, err)

	req := mock.captured
	require.Equal(t, "none", req.ToolChoice)
}

func TestClientRequiresDefaultModel(t *testing.T) {
	_, err := openaimodel.New(openaimodel.Options{Client: &mockChatClient{}})
	require.Error(t, err)
}

type mockChatClient struct {
	response openai.ChatCompletionResponse
	captured openai.ChatCompletionRequest
}

func (m *mockChatClient) CreateChatCompletion(ctx context.Context, request openai.ChatCompletionRequest) (
	openai.ChatCompletionResponse, error) {
	m.captured = request
	return m.response, nil
}
