package planner

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	recordingEvents struct {
		usage []model.TokenUsage
	}

	testStreamer struct {
		chunks []model.Chunk
		meta   map[string]any
		index  int
		closed bool
	}
)

func (e *recordingEvents) AssistantChunk(context.Context, string) {}

func (e *recordingEvents) ToolCallArgsDelta(context.Context, string, tools.Ident, string) {}

func (e *recordingEvents) PlannerThinkingBlock(context.Context, model.ThinkingPart) {}

func (e *recordingEvents) PlannerThought(context.Context, string, map[string]string) {}

func (e *recordingEvents) UsageDelta(_ context.Context, usage model.TokenUsage) {
	e.usage = append(e.usage, usage)
}

func (s *testStreamer) Recv() (model.Chunk, error) {
	if s.index >= len(s.chunks) {
		return model.Chunk{}, io.EOF
	}
	chunk := s.chunks[s.index]
	s.index++
	return chunk, nil
}

func (s *testStreamer) Close() error {
	s.closed = true
	return nil
}

func (s *testStreamer) Metadata() map[string]any {
	return s.meta
}

func TestConsumeStreamStampsUsageIdentityFromRequest(t *testing.T) {
	streamer := &testStreamer{
		chunks: []model.Chunk{
			{
				Type:       model.ChunkTypeUsage,
				UsageDelta: &model.TokenUsage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5},
			},
		},
		meta: map[string]any{
			"usage": model.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
		},
	}
	events := &recordingEvents{}

	summary, err := ConsumeStream(
		context.Background(),
		streamer,
		&model.Request{Model: "gpt-5", ModelClass: model.ModelClassHighReasoning},
		events,
	)

	require.NoError(t, err)
	require.True(t, streamer.closed)
	require.Equal(t, "gpt-5", summary.Usage.Model)
	require.Equal(t, model.ModelClassHighReasoning, summary.Usage.ModelClass)
	require.Equal(t, 3, summary.Usage.InputTokens)
	require.Equal(t, 4, summary.Usage.OutputTokens)
	require.Equal(t, 7, summary.Usage.TotalTokens)
	require.Len(t, events.usage, 2)
	require.Equal(t, "gpt-5", events.usage[0].Model)
	require.Equal(t, model.ModelClassHighReasoning, events.usage[0].ModelClass)
}
