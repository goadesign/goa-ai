package bedrock

// These tests drive the complete provider and validated-stream boundaries so a
// private Bedrock tool can never escape as a caller-visible tool invocation.

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	recordingAnthropicProvider struct {
		stream  model.Streamer
		request *model.Request
	}

	scriptedAnthropicStreamer struct {
		chunks   []model.Chunk
		response *model.Response
		index    int
		closed   bool
	}
)

// Complete is unused by this stream-focused provider.
func (p *recordingAnthropicProvider) Complete(context.Context, *model.Request) (*model.Response, error) {
	return nil, errors.New("unexpected complete call")
}

// Stream records the transformed request and returns the scripted Anthropic
// tool stream that the Bedrock wrapper must convert.
func (p *recordingAnthropicProvider) Stream(_ context.Context, req *model.Request) (model.Streamer, error) {
	p.request = req
	return p.stream, nil
}

// Recv returns each scripted provider chunk followed by clean EOF.
func (s *scriptedAnthropicStreamer) Recv() (model.Chunk, error) {
	if s.index == len(s.chunks) {
		return nil, io.EOF
	}
	chunk := s.chunks[s.index]
	s.index++
	return chunk, nil
}

// Response returns the scripted raw tool response after all chunks are read.
func (s *scriptedAnthropicStreamer) Response() *model.Response {
	if s.index < len(s.chunks) {
		return nil
	}
	return s.response
}

// Close records that the wrapper released the underlying stream.
func (s *scriptedAnthropicStreamer) Close() error {
	s.closed = true
	return nil
}

func TestAnthropicBedrockStructuredOutputStreamReifiesForcedTool(t *testing.T) {
	usage := model.TokenUsage{
		Model:        "us.anthropic.claude-opus-5",
		ModelClass:   model.ModelClassHighReasoning,
		InputTokens:  10,
		OutputTokens: 5,
	}
	raw := &scriptedAnthropicStreamer{
		chunks: []model.Chunk{
			model.UsageChunk{Usage: usage},
			model.ToolCallDeltaChunk{Delta: model.ToolCallDelta{
				Name:  tools.Ident("eval_judgments"),
				ID:    "toolu_01",
				Delta: `{"value":`,
			}},
			model.ToolCallChunk{ToolCall: model.ToolCall{
				Name:    tools.Ident("eval_judgments"),
				ID:      "toolu_01",
				Payload: rawjson.Message(`{"value":{"passed":true}}`),
			}},
			model.StopChunk{Reason: "tool_use"},
		},
		response: &model.Response{
			Content: []model.Message{{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{model.ToolUsePart{
					Name:  "eval_judgments",
					ID:    "toolu_01",
					Input: rawjson.Message(`{"value":{"passed":true}}`),
				}},
			}},
			Usage:      usage,
			StopReason: "tool_use",
		},
	}
	inference := &recordingAnthropicProvider{stream: raw}
	provider := &anthropicBedrockProvider{
		inference: inference,
		highModel: "us.anthropic.claude-opus-5",
	}
	client, err := model.NewClient(provider)
	require.NoError(t, err)
	stream, err := client.Stream(t.Context(), &model.Request{
		ModelClass: model.ModelClassHighReasoning,
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "Judge the assertion."}},
		}},
		StructuredOutput: &model.StructuredOutput{
			Name: "eval_judgments",
			Schema: rawjson.Message(`{
				"type":"object",
				"additionalProperties":false,
				"properties":{"passed":{"type":"boolean"}},
				"required":["passed"]
			}`),
		},
	})
	require.NoError(t, err)

	var chunks []model.Chunk
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		chunks = append(chunks, chunk)
	}

	require.Len(t, chunks, 3)
	require.IsType(t, model.UsageChunk{}, chunks[0])
	completion, ok := chunks[1].(model.CompletionChunk)
	require.True(t, ok)
	assert.Equal(t, "eval_judgments", completion.Completion.Name)
	assert.JSONEq(t, `{"passed":true}`, string(completion.Completion.Payload))
	require.IsType(t, model.StopChunk{}, chunks[2])
	response := stream.Response()
	require.NotNil(t, response)
	assert.Equal(t, []model.Part{
		model.TextPart{Text: `{"passed":true}`},
	}, response.Content[0].Parts)
	require.NotNil(t, inference.request)
	assert.Nil(t, inference.request.StructuredOutput)
	require.Len(t, inference.request.Tools, 1)
	assert.Equal(t, "eval_judgments", inference.request.Tools[0].Name)
	require.NoError(t, stream.Close())
	assert.True(t, raw.closed)
}
