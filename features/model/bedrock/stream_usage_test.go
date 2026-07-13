package bedrock

import (
	"testing"

	"github.com/stretchr/testify/require"

	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"goa.design/goa-ai/runtime/agent/model"
)

func TestChunkProcessor_MetadataUsageIncludesCacheTokens(t *testing.T) {
	var (
		inTokens   int32 = 10
		outTokens  int32 = 4
		total      int32 = 14
		cacheRead  int32 = 3
		cacheWrite int32 = 5
	)

	var (
		recordedUsage model.TokenUsage
		chunks        []model.Chunk
	)

	cp := newChunkProcessor(
		func(ch model.Chunk) error {
			chunks = append(chunks, ch)
			return nil
		},
		func(u model.TokenUsage) {
			recordedUsage = u
		},
		func([]model.Citation) {
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

	require.Equal(t, int(inTokens), recordedUsage.InputTokens)
	require.Equal(t, int(outTokens), recordedUsage.OutputTokens)
	require.Equal(t, int(total), recordedUsage.TotalTokens)
	require.Equal(t, int(cacheRead), recordedUsage.CacheReadTokens)
	require.Equal(t, int(cacheWrite), recordedUsage.CacheWriteTokens)
	require.Equal(t, "test-model-id", recordedUsage.Model)
	require.Equal(t, model.ModelClassDefault, recordedUsage.ModelClass)

	require.Len(t, chunks, 2)
	usageChunk, ok := chunks[0].(model.UsageChunk)
	require.True(t, ok)
	require.Equal(t, int(cacheRead), usageChunk.Usage.CacheReadTokens)
	require.Equal(t, int(cacheWrite), usageChunk.Usage.CacheWriteTokens)
	require.Equal(t, "test-model-id", usageChunk.Usage.Model)
	require.Equal(t, model.ModelClassDefault, usageChunk.Usage.ModelClass)
	require.IsType(t, model.StopChunk{}, chunks[1])
}

func TestReasoningBufferFinalizeRequiresCanonicalVariant(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		signature string
		redacted  []byte
		wantErr   string
	}{
		{name: "plaintext", text: "reasoning", signature: "sig"},
		{name: "redacted", redacted: []byte("opaque")},
		{name: "missing signature", text: "reasoning", wantErr: "reasoning plaintext is missing provider signature"},
		{name: "missing text", signature: "sig", wantErr: "reasoning signature is missing plaintext content"},
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
			require.NotNil(t, part)
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
		func(model.TokenUsage) {
		},
		func([]model.Citation) {
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
	err = cp.Handle(&brtypes.ConverseStreamOutputMemberContentBlockStart{
		Value: brtypes.ContentBlockStartEvent{ContentBlockIndex: &idx},
	})
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
		func(model.TokenUsage) {
		},
		func([]model.Citation) {
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
	err = cp.Handle(&brtypes.ConverseStreamOutputMemberContentBlockStart{
		Value: brtypes.ContentBlockStartEvent{ContentBlockIndex: &idx},
	})
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

func TestChunkProcessorRejectsMessageStopWithOpenContentBlock(t *testing.T) {
	idx := int32(0)
	cp := newChunkProcessor(
		func(model.Chunk) error { return nil },
		func(model.TokenUsage) {},
		func([]model.Citation) {},
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
