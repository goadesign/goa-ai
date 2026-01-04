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

	require.Equal(t, model.ChunkTypeUsage, gotChunk.Type)
	if gotChunk.UsageDelta == nil {
		t.Fatalf("expected UsageDelta to be set on chunk")
	}
	require.Equal(t, int(cacheRead), gotChunk.UsageDelta.CacheReadTokens)
	require.Equal(t, int(cacheWrite), gotChunk.UsageDelta.CacheWriteTokens)
}
