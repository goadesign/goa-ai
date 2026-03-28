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
		gotChunk      model.Chunk
	)

	cp := newChunkProcessor(
		func(ch model.Chunk) error {
			gotChunk = ch
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

	err := cp.Handle(event)
	require.NoError(t, err)

	require.Equal(t, int(inTokens), recordedUsage.InputTokens)
	require.Equal(t, int(outTokens), recordedUsage.OutputTokens)
	require.Equal(t, int(total), recordedUsage.TotalTokens)
	require.Equal(t, int(cacheRead), recordedUsage.CacheReadTokens)
	require.Equal(t, int(cacheWrite), recordedUsage.CacheWriteTokens)
	require.Equal(t, "test-model-id", recordedUsage.Model)
	require.Equal(t, model.ModelClassDefault, recordedUsage.ModelClass)

	require.Equal(t, model.ChunkTypeUsage, gotChunk.Type)
	require.NotNil(t, gotChunk.UsageDelta)
	require.Equal(t, int(cacheRead), gotChunk.UsageDelta.CacheReadTokens)
	require.Equal(t, int(cacheWrite), gotChunk.UsageDelta.CacheWriteTokens)
	require.Equal(t, "test-model-id", gotChunk.UsageDelta.Model)
	require.Equal(t, model.ModelClassDefault, gotChunk.UsageDelta.ModelClass)
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
	err = cp.Handle(&brtypes.ConverseStreamOutputMemberMessageStop{})
	require.NoError(t, err)

	require.Len(t, chunks, 3)
	require.Equal(t, model.ChunkTypeCompletionDelta, chunks[0].Type)
	require.NotNil(t, chunks[0].CompletionDelta)
	require.Equal(t, "draft_from_transcript", chunks[0].CompletionDelta.Name)
	require.JSONEq(t, `{"assistant_text":"created a draft"}`, chunks[0].CompletionDelta.Delta)

	require.Equal(t, model.ChunkTypeCompletion, chunks[1].Type)
	require.NotNil(t, chunks[1].Completion)
	require.Equal(t, "draft_from_transcript", chunks[1].Completion.Name)
	require.JSONEq(t, `{"assistant_text":"created a draft"}`, string(chunks[1].Completion.Payload))

	require.Equal(t, model.ChunkTypeStop, chunks[2].Type)
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
