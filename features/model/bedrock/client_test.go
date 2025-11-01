package bedrock_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/features/model/bedrock"
	"goa.design/goa-ai/runtime/agent/model"
)

func TestClientComplete(t *testing.T) {
	mock := &mockRuntime{}
	client, err := bedrock.New(bedrock.Options{
		Runtime: mock,
		Model:   "anthropic.claude-3",
	})
	require.NoError(t, err)

	mock.output = &bedrockruntime.ConverseOutput{
		Output: &brtypes.ConverseOutputMemberMessage{Value: brtypes.Message{
			Role: brtypes.ConversationRoleAssistant,
			Content: []brtypes.ContentBlock{
				&brtypes.ContentBlockMemberText{Value: "hello"},
				&brtypes.ContentBlockMemberToolUse{Value: brtypes.ToolUseBlock{
					Name:  aws.String("calc.tool"),
					Input: document.NewLazyDocument(&map[string]any{"value": 42}),
				}},
			},
		}},
		Usage: &brtypes.TokenUsage{
			InputTokens:  aws.Int32(100),
			OutputTokens: aws.Int32(20),
			TotalTokens:  aws.Int32(120),
		},
		StopReason: brtypes.StopReasonToolUse,
	}

	resp, err := client.Complete(context.Background(), model.Request{
		Messages: []model.Message{
			{Role: "system", Content: "You are smart."},
			{Role: "user", Content: "hi"},
		},
		Tools: []model.ToolDefinition{
			{
				Name:        "calc.tool",
				Description: "calculator",
				InputSchema: map[string]any{"type": "object"},
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, resp.Content, 1)
	require.Equal(t, "hello", resp.Content[0].Content)
	require.Len(t, resp.ToolCalls, 1)
	require.Equal(t, "calc.tool", resp.ToolCalls[0].Name)
	require.InDelta(t, 42.0, resp.ToolCalls[0].Payload.(map[string]any)["value"], 0.001)
	require.Equal(t, "tool_use", resp.StopReason)
	require.Equal(t, 120, resp.Usage.TotalTokens)

	input := mock.captured
	require.Equal(t, "anthropic.claude-3", *input.ModelId)
	require.Len(t, input.System, 1)
	require.Len(t, input.Messages, 1)
	require.Equal(t, brtypes.ConversationRoleUser, input.Messages[0].Role)
	require.Equal(t, "hi", input.Messages[0].Content[0].(*brtypes.ContentBlockMemberText).Value)
	require.NotNil(t, input.ToolConfig)
	require.Len(t, input.ToolConfig.Tools, 1)
}

func TestClientRequiresUserMessage(t *testing.T) {
	client, err := bedrock.New(bedrock.Options{Runtime: &mockRuntime{}, Model: "id"})
	require.NoError(t, err)
	_, err = client.Complete(context.Background(), model.Request{
		Messages: []model.Message{{Role: "system", Content: "only system"}},
	})
	require.Error(t, err)
}

func TestClientStream(t *testing.T) {
	mock := &mockRuntime{}
	client, err := bedrock.New(bedrock.Options{
		Runtime: mock,
		Model:   "anthropic.claude-3",
	})
	require.NoError(t, err)

	events := []brtypes.ConverseStreamOutput{
		&brtypes.ConverseStreamOutputMemberMessageStart{Value: brtypes.MessageStartEvent{}},
		&brtypes.ConverseStreamOutputMemberContentBlockDelta{Value: brtypes.ContentBlockDeltaEvent{
			ContentBlockIndex: aws.Int32(0),
			Delta:             &brtypes.ContentBlockDeltaMemberText{Value: "Hello"},
		}},
		&brtypes.ConverseStreamOutputMemberContentBlockDelta{Value: brtypes.ContentBlockDeltaEvent{
			ContentBlockIndex: aws.Int32(0),
			Delta: &brtypes.ContentBlockDeltaMemberReasoningContent{
				Value: &brtypes.ReasoningContentBlockDeltaMemberText{Value: "Thinking"},
			},
		}},
		&brtypes.ConverseStreamOutputMemberContentBlockStart{Value: brtypes.ContentBlockStartEvent{
			ContentBlockIndex: aws.Int32(1),
			Start: &brtypes.ContentBlockStartMemberToolUse{Value: brtypes.ToolUseBlockStart{
				Name:      aws.String("$FUNCTIONS.search"),
				ToolUseId: aws.String("tool-1"),
			}},
		}},
		&brtypes.ConverseStreamOutputMemberContentBlockDelta{Value: brtypes.ContentBlockDeltaEvent{
			ContentBlockIndex: aws.Int32(1),
			Delta: &brtypes.ContentBlockDeltaMemberToolUse{Value: brtypes.ToolUseBlockDelta{
				Input: aws.String("{\"query\":\"goa\"}"),
			}},
		}},
		&brtypes.ConverseStreamOutputMemberContentBlockStop{Value: brtypes.ContentBlockStopEvent{
			ContentBlockIndex: aws.Int32(1),
		}},
		&brtypes.ConverseStreamOutputMemberMetadata{Value: brtypes.ConverseStreamMetadataEvent{
			Usage: &brtypes.TokenUsage{
				InputTokens:  aws.Int32(10),
				OutputTokens: aws.Int32(2),
				TotalTokens:  aws.Int32(12),
			},
		}},
		&brtypes.ConverseStreamOutputMemberMessageStop{
			Value: brtypes.MessageStopEvent{StopReason: brtypes.StopReasonToolUse},
		},
	}

	mock.streamOutput = newFakeStreamOutput(events, nil)
	streamer, err := client.Stream(context.Background(), model.Request{
		Messages: []model.Message{
			{Role: "system", Content: "system"},
			{Role: "user", Content: "hello"},
		},
		Tools: []model.ToolDefinition{{
			Name:        "search",
			Description: "search",
			InputSchema: map[string]any{"type": "object"},
		}},
		Thinking: &model.ThinkingOptions{Enable: true, BudgetTokens: 1024},
	})
	require.NoError(t, err)
	defer func() {
		_ = streamer.Close()
	}()

	var chunks []model.Chunk
	for {
		chunk, err := streamer.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		chunks = append(chunks, chunk)
	}
	require.Len(t, chunks, 5)
	require.Equal(t, model.ChunkTypeText, chunks[0].Type)
	require.Equal(t, "Hello", chunks[0].Message.Content)
	require.Equal(t, model.ChunkTypeThinking, chunks[1].Type)
	require.Equal(t, model.ChunkTypeToolCall, chunks[2].Type)
	require.Equal(t, "search", chunks[2].ToolCall.Name)
	require.Equal(t, "goa", chunks[2].ToolCall.Payload.(map[string]any)["query"])
	require.Equal(t, model.ChunkTypeUsage, chunks[3].Type)
	require.Equal(t, 12, chunks[3].UsageDelta.TotalTokens)
	require.Equal(t, model.ChunkTypeStop, chunks[4].Type)
	require.Equal(t, "tool_use", chunks[4].StopReason)

	meta := streamer.Metadata()
	require.NotNil(t, meta)
	usage, ok := meta["usage"].(model.TokenUsage)
	require.True(t, ok)
	require.Equal(t, 12, usage.TotalTokens)

	require.NotNil(t, mock.streamInput)
	require.NotNil(t, mock.streamInput.AdditionalModelRequestFields)
	raw, err := mock.streamInput.AdditionalModelRequestFields.MarshalSmithyDocument()
	require.NoError(t, err)
	var thinking map[string]any
	require.NoError(t, json.Unmarshal(raw, &thinking))
	require.Contains(t, thinking, "thinking")
}

type mockRuntime struct {
	captured     *bedrockruntime.ConverseInput
	output       *bedrockruntime.ConverseOutput
	streamInput  *bedrockruntime.ConverseStreamInput
	streamOutput bedrock.StreamOutput
	streamErr    error
}

func (m *mockRuntime) Converse(ctx context.Context, params *bedrockruntime.ConverseInput,
	optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	m.captured = params
	return m.output, nil
}

func (m *mockRuntime) ConverseStream(ctx context.Context, params *bedrockruntime.ConverseStreamInput,
	optFns ...func(*bedrockruntime.Options)) (bedrock.StreamOutput, error) {
	m.streamInput = params
	if m.streamErr != nil {
		return nil, m.streamErr
	}
	return m.streamOutput, nil
}

type fakeStreamOutput struct {
	stream *bedrockruntime.ConverseStreamEventStream
}

func (f *fakeStreamOutput) GetStream() *bedrockruntime.ConverseStreamEventStream {
	return f.stream
}

type fakeStreamReader struct {
	events chan brtypes.ConverseStreamOutput
	err    error
}

func (r *fakeStreamReader) Events() <-chan brtypes.ConverseStreamOutput { return r.events }
func (r *fakeStreamReader) Close() error                                { return nil }
func (r *fakeStreamReader) Err() error                                  { return r.err }

func newFakeStreamOutput(events []brtypes.ConverseStreamOutput, err error) *fakeStreamOutput {
	ch := make(chan brtypes.ConverseStreamOutput, len(events))
	for _, ev := range events {
		ch <- ev
	}
	close(ch)
	reader := &fakeStreamReader{events: ch, err: err}
	stream := bedrockruntime.NewConverseStreamEventStream(func(es *bedrockruntime.ConverseStreamEventStream) {
		es.Reader = reader
	})
	return &fakeStreamOutput{stream: stream}
}
