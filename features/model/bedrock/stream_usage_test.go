package bedrock

import (
	"testing"

	"github.com/stretchr/testify/require"

	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"goa.design/goa-ai/runtime/agent/model"
)

func TestChunkProcessorUsageIncludesCacheTokens(t *testing.T) {
	var (
		inTokens   int32 = 10
		outTokens  int32 = 4
		total      int32 = 14
		cacheRead  int32 = 3
		cacheWrite int32 = 5
	)

	var chunks []model.Chunk

	cp := newChunkProcessor(
		func(ch model.Chunk) error {
			chunks = append(chunks, ch)
			return nil
		},
		map[string]string{},
		"test-model-id",
		model.ModelClassDefault,
		nil,
	)

	err := cp.Handle(&brtypes.ConverseStreamOutputMemberMessageStart{})
	require.NoError(t, err)
	err = cp.Handle(&brtypes.ConverseStreamOutputMemberMessageStop{
		Value: brtypes.MessageStopEvent{StopReason: brtypes.StopReasonEndTurn},
	})
	require.NoError(t, err)
	event := &brtypes.ConverseStreamOutputMemberMetadata{
		Value: brtypes.ConverseStreamMetadataEvent{
			Usage: &brtypes.TokenUsage{
				InputTokens:           &inTokens,
				OutputTokens:          &outTokens,
				TotalTokens:           &total,
				CacheReadInputTokens:  &cacheRead,
				CacheWriteInputTokens: &cacheWrite,
			},
		},
	}

	err = cp.Handle(event)
	require.NoError(t, err)

	require.Len(t, chunks, 2)
	usageChunk, ok := chunks[0].(model.UsageChunk)
	require.True(t, ok)
	require.Equal(t, int(inTokens), usageChunk.Usage.InputTokens)
	require.Equal(t, int(outTokens), usageChunk.Usage.OutputTokens)
	require.Equal(t, int(total), usageChunk.Usage.TotalTokens)
	require.Equal(t, int(cacheRead), usageChunk.Usage.CacheReadTokens)
	require.Equal(t, int(cacheWrite), usageChunk.Usage.CacheWriteTokens)
	require.Equal(t, "test-model-id", usageChunk.Usage.Model)
	require.Equal(t, model.ModelClassDefault, usageChunk.Usage.ModelClass)
	require.IsType(t, model.StopChunk{}, chunks[1])
	require.Equal(t, usageChunk.Usage, cp.response().Usage)
}

func TestReasoningBufferFinalize(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		signature string
		redacted  []byte
		wantErr   string
		wantPart  bool
	}{
		{name: "plaintext", text: "reasoning", signature: "sig", wantPart: true},
		{name: "redacted", redacted: []byte("opaque"), wantPart: true},
		{name: "missing signature", text: "reasoning"},
		{name: "missing text", signature: "sig"},
		{
			name:      "mixed variants",
			text:      "reasoning",
			signature: "sig",
			redacted:  []byte("opaque"),
			wantErr:   "reasoning block contains both redacted and plaintext content",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			buffer := &reasoningBuffer{
				signature: test.signature,
				redacted:  test.redacted,
			}
			buffer.text.WriteString(test.text)

			part, err := buffer.finalize()

			if test.wantErr != "" {
				require.EqualError(t, err, test.wantErr)
				require.Nil(t, part)
				return
			}
			require.NoError(t, err)
			if test.wantPart {
				require.NotNil(t, part)
				return
			}
			require.Nil(t, part)
		})
	}
}

func TestChunkProcessor_StructuredOutputEmitsCompletionDeltaAndFinalCompletion(t *testing.T) {
	idx := int32(0)
	var chunks []model.Chunk

	cp := newChunkProcessor(
		func(ch model.Chunk) error {
			chunks = append(chunks, ch)
			return nil
		},
		map[string]string{},
		"test-model-id",
		model.ModelClassDefault,
		&model.StructuredOutput{
			Name:   "draft_from_transcript",
			Schema: []byte(`{"type":"object"}`),
		},
	)

	err := cp.Handle(&brtypes.ConverseStreamOutputMemberMessageStart{})
	require.NoError(t, err)
	err = cp.Handle(&brtypes.ConverseStreamOutputMemberContentBlockDelta{
		Value: brtypes.ContentBlockDeltaEvent{
			ContentBlockIndex: &idx,
			Delta: &brtypes.ContentBlockDeltaMemberText{
				Value: `{"assistant_text":"created a draft"}`,
			},
		},
	})
	require.NoError(t, err)
	err = cp.Handle(&brtypes.ConverseStreamOutputMemberContentBlockStop{
		Value: brtypes.ContentBlockStopEvent{ContentBlockIndex: &idx},
	})
	require.NoError(t, err)
	err = cp.Handle(&brtypes.ConverseStreamOutputMemberMessageStop{
		Value: brtypes.MessageStopEvent{StopReason: brtypes.StopReasonEndTurn},
	})
	require.NoError(t, err)
	usage := int32(3)
	err = cp.Handle(&brtypes.ConverseStreamOutputMemberMetadata{
		Value: brtypes.ConverseStreamMetadataEvent{
			Usage: &brtypes.TokenUsage{TotalTokens: &usage},
		},
	})
	require.NoError(t, err)

	require.Len(t, chunks, 4)
	delta, ok := chunks[0].(model.CompletionDeltaChunk)
	require.True(t, ok)
	require.Equal(t, "draft_from_transcript", delta.Delta.Name)
	require.JSONEq(t, `{"assistant_text":"created a draft"}`, delta.Delta.Delta)

	completion, ok := chunks[1].(model.CompletionChunk)
	require.True(t, ok)
	require.Equal(t, "draft_from_transcript", completion.Completion.Name)
	require.JSONEq(t, `{"assistant_text":"created a draft"}`, string(completion.Completion.Payload))

	require.IsType(t, model.UsageChunk{}, chunks[2])
	require.IsType(t, model.StopChunk{}, chunks[3])
	response := cp.response()
	require.NoError(t, model.ValidateResponse(response))
	require.Len(t, response.Content, 1)
	require.Equal(t, model.TextPart{Text: `{"assistant_text":"created a draft"}`}, response.Content[0].Parts[0])
}

func TestChunkProcessor_StructuredOutputRejectsInvalidFinalJSON(t *testing.T) {
	idx := int32(0)

	cp := newChunkProcessor(
		func(model.Chunk) error {
			return nil
		},
		map[string]string{},
		"test-model-id",
		model.ModelClassDefault,
		&model.StructuredOutput{
			Name:   "draft_from_transcript",
			Schema: []byte(`{"type":"object"}`),
		},
	)

	err := cp.Handle(&brtypes.ConverseStreamOutputMemberMessageStart{})
	require.NoError(t, err)
	err = cp.Handle(&brtypes.ConverseStreamOutputMemberContentBlockDelta{
		Value: brtypes.ContentBlockDeltaEvent{
			ContentBlockIndex: &idx,
			Delta: &brtypes.ContentBlockDeltaMemberText{
				Value: `{"assistant_text":"created a draft"`,
			},
		},
	})
	require.NoError(t, err)
	err = cp.Handle(&brtypes.ConverseStreamOutputMemberContentBlockStop{
		Value: brtypes.ContentBlockStopEvent{ContentBlockIndex: &idx},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not valid JSON")
}

func TestChunkProcessorReasoningBlockStartsWithFirstDelta(t *testing.T) {
	idx := int32(0)
	var chunks []model.Chunk
	cp := newChunkProcessor(
		func(chunk model.Chunk) error {
			chunks = append(chunks, chunk)
			return nil
		},
		map[string]string{},
		"test-model-id",
		model.ModelClassDefault,
		nil,
	)

	err := cp.Handle(&brtypes.ConverseStreamOutputMemberMessageStart{})
	require.NoError(t, err)
	err = cp.Handle(&brtypes.ConverseStreamOutputMemberContentBlockDelta{
		Value: brtypes.ContentBlockDeltaEvent{
			ContentBlockIndex: &idx,
			Delta: &brtypes.ContentBlockDeltaMemberReasoningContent{
				Value: &brtypes.ReasoningContentBlockDeltaMemberText{Value: "reasoning"},
			},
		},
	})
	require.NoError(t, err)
	err = cp.Handle(&brtypes.ConverseStreamOutputMemberContentBlockDelta{
		Value: brtypes.ContentBlockDeltaEvent{
			ContentBlockIndex: &idx,
			Delta: &brtypes.ContentBlockDeltaMemberReasoningContent{
				Value: &brtypes.ReasoningContentBlockDeltaMemberSignature{Value: "signature"},
			},
		},
	})
	require.NoError(t, err)
	err = cp.Handle(&brtypes.ConverseStreamOutputMemberContentBlockStop{
		Value: brtypes.ContentBlockStopEvent{ContentBlockIndex: &idx},
	})
	require.NoError(t, err)

	require.Len(t, chunks, 2)
	final, ok := chunks[1].(model.ThinkingChunk)
	require.True(t, ok)
	require.Equal(t, model.ThinkingPart{
		Text:      "reasoning",
		Signature: "signature",
		Index:     0,
		Final:     true,
	}, final.Message.Parts[0])
}

func TestChunkProcessorIgnoresEmptyToolUseDelta(t *testing.T) {
	idx := int32(0)
	name := "reports_lookup"
	id := "tooluse_1"
	empty := ""
	var chunks []model.Chunk
	cp := newChunkProcessor(
		func(chunk model.Chunk) error {
			chunks = append(chunks, chunk)
			return nil
		},
		map[string]string{name: "reports.lookup"},
		"test-model-id",
		model.ModelClassDefault,
		nil,
	)

	err := cp.Handle(&brtypes.ConverseStreamOutputMemberMessageStart{})
	require.NoError(t, err)
	err = cp.Handle(&brtypes.ConverseStreamOutputMemberContentBlockStart{
		Value: brtypes.ContentBlockStartEvent{
			ContentBlockIndex: &idx,
			Start: &brtypes.ContentBlockStartMemberToolUse{
				Value: brtypes.ToolUseBlockStart{
					Name:      &name,
					ToolUseId: &id,
				},
			},
		},
	})
	require.NoError(t, err)
	err = cp.Handle(&brtypes.ConverseStreamOutputMemberContentBlockDelta{
		Value: brtypes.ContentBlockDeltaEvent{
			ContentBlockIndex: &idx,
			Delta: &brtypes.ContentBlockDeltaMemberToolUse{
				Value: brtypes.ToolUseBlockDelta{Input: &empty},
			},
		},
	})

	require.NoError(t, err)
	require.Empty(t, chunks)
}

func TestChunkProcessorRejectsMessageStopWithOpenContentBlock(t *testing.T) {
	idx := int32(0)
	cp := newChunkProcessor(
		func(model.Chunk) error { return nil },
		map[string]string{},
		"test-model-id",
		model.ModelClassDefault,
		nil,
	)

	err := cp.Handle(&brtypes.ConverseStreamOutputMemberMessageStart{})
	require.NoError(t, err)
	err = cp.Handle(&brtypes.ConverseStreamOutputMemberContentBlockStart{
		Value: brtypes.ContentBlockStartEvent{ContentBlockIndex: &idx},
	})
	require.NoError(t, err)
	err = cp.Handle(&brtypes.ConverseStreamOutputMemberMessageStop{})

	require.EqualError(t, err, "bedrock stream: message stopped with 1 open content blocks")
}

// TestChunkProcessorClassifiesMessageStopWithoutStartAsEmptyStream verifies
// that a messageStop arriving before messageStart is classified as a
// retryable empty stream (model.ErrEmptyStream). Bedrock intermittently
// produces this wire shape when the model emits an empty completion, so retry
// middleware must be able to detect it without string matching.
func TestChunkProcessorClassifiesMessageStopWithoutStartAsEmptyStream(t *testing.T) {
	cp := newChunkProcessor(
		func(model.Chunk) error { return nil },
		map[string]string{},
		"test-model-id",
		model.ModelClassDefault,
		nil,
	)

	err := cp.Handle(&brtypes.ConverseStreamOutputMemberMessageStop{
		Value: brtypes.MessageStopEvent{StopReason: brtypes.StopReasonEndTurn},
	})

	require.ErrorIs(t, err, model.ErrEmptyStream)
	pe, ok := model.AsProviderError(err)
	require.True(t, ok)
	require.Equal(t, model.ProviderErrorKindUnavailable, pe.Kind())
	require.Equal(t, "empty_stream", pe.Code())
	require.True(t, pe.Retryable())
}

// TestChunkProcessorRejectsDuplicateMessageStop verifies that a second
// messageStop after a completed message stays a hard protocol error and is
// not mistaken for an empty stream.
func TestChunkProcessorRejectsDuplicateMessageStop(t *testing.T) {
	cp := newChunkProcessor(
		func(model.Chunk) error { return nil },
		map[string]string{},
		"test-model-id",
		model.ModelClassDefault,
		nil,
	)

	require.NoError(t, cp.Handle(&brtypes.ConverseStreamOutputMemberMessageStart{}))
	require.NoError(t, cp.Handle(&brtypes.ConverseStreamOutputMemberMessageStop{
		Value: brtypes.MessageStopEvent{StopReason: brtypes.StopReasonEndTurn},
	}))
	err := cp.Handle(&brtypes.ConverseStreamOutputMemberMessageStop{
		Value: brtypes.MessageStopEvent{StopReason: brtypes.StopReasonEndTurn},
	})

	require.EqualError(t, err, "bedrock stream: duplicate message stop")
	require.NotErrorIs(t, err, model.ErrEmptyStream)
}
