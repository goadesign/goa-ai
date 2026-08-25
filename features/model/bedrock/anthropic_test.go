package bedrock

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

type recordingAnthropicCounter struct {
	request *model.Request
}

type testCredentialsProvider struct{}

// Retrieve returns fixed credentials that let the local request test exercise
// AWS signing without reading developer or production credentials.
func (testCredentialsProvider) Retrieve(context.Context) (aws.Credentials, error) {
	return aws.Credentials{
		AccessKeyID:     "key",
		SecretAccessKey: "secret",
	}, nil
}

// CountTokens records the request that the Claude-on-Bedrock provider sends to
// its exact Anthropic counter.
func (c *recordingAnthropicCounter) CountTokens(_ context.Context, req *model.Request) (model.TokenCount, error) {
	copied := *req
	c.request = &copied
	return model.TokenCount{
		Model:       req.Model,
		ModelClass:  req.ModelClass,
		InputTokens: 42,
		Exact:       true,
	}, nil
}

func TestAnthropicBedrockCountTokensUsesFoundationModel(t *testing.T) {
	counter := &recordingAnthropicCounter{}
	provider := &anthropicBedrockProvider{
		inference:    nil,
		counter:      counter,
		defaultModel: "global.anthropic.claude-sonnet-5",
		highModel:    "us.anthropic.claude-opus-5",
		smallModel:   "global.anthropic.claude-sonnet-5",
	}

	count, err := provider.CountTokens(t.Context(), &model.Request{
		ModelClass: model.ModelClassHighReasoning,
	})

	require.NoError(t, err)
	assert.Equal(t, 42, count.InputTokens)
	require.NotNil(t, counter.request)
	assert.Equal(t, "anthropic.claude-opus-5", counter.request.Model)
	assert.Equal(t, model.ModelClassHighReasoning, counter.request.ModelClass)
}

func TestAnthropicBedrockResumeKeepsSchemaToolExampleAndChoice(t *testing.T) {
	var requestBody []byte
	handlerErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var err error
		requestBody, err = io.ReadAll(req.Body)
		if err != nil {
			handlerErr <- err
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, err = io.WriteString(w, `{
			"id":"msg_test",
			"type":"message",
			"role":"assistant",
			"content":[{
				"type":"tool_use",
				"id":"toolu_01ABCDEFGHIJKLMNOPQRSTUV",
				"name":"tasks_progress_complete",
				"input":{"detail_blocks":[{"type":"markdown","text":"Done"}]}
			}],
			"model":"global.anthropic.claude-sonnet-5",
			"stop_reason":"tool_use",
			"usage":{"input_tokens":10,"output_tokens":5}
		}`)
		handlerErr <- err
	}))
	t.Cleanup(server.Close)

	input, err := model.ToolInputFromContract("tasks.progress.complete", model.ToolInputContract{
		Schema: rawjson.Message(`{
			"type":"object",
			"properties":{
				"detail_blocks":{
					"type":"array",
					"items":{
						"type":"object",
						"properties":{
							"type":{"type":"string"},
							"text":{"type":"string"}
						},
						"required":["type","text"]
					}
				}
			},
			"required":["detail_blocks"],
			"example":{"detail_blocks":[{"type":"markdown","text":"Done"}]}
		}`),
		SchemaWithoutRootExample: rawjson.Message(`{
			"type":"object",
			"properties":{
				"detail_blocks":{
					"type":"array",
					"items":{
						"type":"object",
						"properties":{
							"type":{"type":"string"},
							"text":{"type":"string"}
						},
						"required":["type","text"]
					}
				}
			},
			"required":["detail_blocks"]
		}`),
		ExampleJSON: rawjson.Message(`{"detail_blocks":[{"type":"markdown","text":"Done"}]}`),
	})
	require.NoError(t, err)

	cfg := aws.Config{
		Region:      "us-west-2",
		Credentials: aws.NewCredentialsCache(testCredentialsProvider{}),
	}
	client, err := NewAnthropic(
		cfg,
		&recordingAnthropicCounter{},
		AnthropicOptions{
			DefaultModel: "global.anthropic.claude-sonnet-5",
			MaxTokens:    128,
		},
		option.WithBaseURL(server.URL),
	)
	require.NoError(t, err)

	response, err := client.Complete(t.Context(), &model.Request{
		Messages: []*model.Message{
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{
					model.TextPart{Text: "Finish the task"},
				},
			},
			{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{
					model.ToolUsePart{
						ID:    "toolu_01PRIORABCDEFGHIJKLMNOP",
						Name:  "tasks.progress.update",
						Input: rawjson.Message(`{"status":"completed"}`),
					},
				},
			},
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{
					model.ToolResultPart{
						ToolUseID: "toolu_01PRIORABCDEFGHIJKLMNOP",
						Content:   rawjson.Message(`{"status":"completed"}`),
					},
					model.TextPart{Text: "Use this equipment photo when finishing."},
					model.ImagePart{
						Format: model.ImageFormatWEBP,
						Bytes:  []byte("equipment photo"),
					},
				},
			},
		},
		Tools: []*model.ToolDefinition{{
			Name:        "tasks.progress.complete",
			Description: "Finish the task with its final brief.",
			Input:       input,
		}},
		ToolChoice: &model.ToolChoice{
			Mode: model.ToolChoiceModeTool,
			Name: "tasks.progress.complete",
		},
		Thinking: &model.ThinkingOptions{Enable: true},
	})

	require.NoError(t, err)
	require.NoError(t, <-handlerErr)
	require.NotNil(t, response)
	require.NotEmpty(t, requestBody)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(requestBody, &payload))
	tools, ok := payload["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
	tool, ok := tools[0].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, tool, "input_examples")
	inputSchema, ok := tool["input_schema"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, map[string]any{
		"detail_blocks": []any{
			map[string]any{"type": "markdown", "text": "Done"},
		},
	}, inputSchema["example"])
	assert.Equal(t, map[string]any{
		"type": "tool",
		"name": "tasks_progress_complete",
	}, payload["tool_choice"])
	messages, ok := payload["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 3)
	resume, ok := messages[2].(map[string]any)
	require.True(t, ok)
	resumeContent, ok := resume["content"].([]any)
	require.True(t, ok)
	require.Len(t, resumeContent, 3)
	resultBlock, ok := resumeContent[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "tool_result", resultBlock["type"])
	textBlock, ok := resumeContent[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, map[string]any{
		"type": "text",
		"text": "Use this equipment photo when finishing.",
	}, textBlock)
	imageBlock, ok := resumeContent[2].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "image", imageBlock["type"])
	imageSource, ok := imageBlock["source"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, map[string]any{
		"type":       "base64",
		"media_type": "image/webp",
		"data":       base64.StdEncoding.EncodeToString([]byte("equipment photo")),
	}, imageSource)
	assert.NotContains(t, payload, "anthropic_beta")
	_, hasToolConfig := payload["toolConfig"]
	assert.False(t, hasToolConfig)
}
