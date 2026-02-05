package bedrock

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestTranslateResponse_UsageIncludesCacheTokens(t *testing.T) {
	var (
		inTokens   int32 = 100
		outTokens  int32 = 25
		total      int32 = 125
		cacheRead  int32 = 40
		cacheWrite int32 = 60
	)

	output := &bedrockruntime.ConverseOutput{
		Usage: &brtypes.TokenUsage{
			InputTokens:           &inTokens,
			OutputTokens:          &outTokens,
			TotalTokens:           &total,
			CacheReadInputTokens:  &cacheRead,
			CacheWriteInputTokens: &cacheWrite,
		},
	}

	resp, err := translateResponse(output, map[string]string{}, "test-model", model.ModelClassDefault)
	require.NoError(t, err)

	require.Equal(t, int(inTokens), resp.Usage.InputTokens)
	require.Equal(t, int(outTokens), resp.Usage.OutputTokens)
	require.Equal(t, int(total), resp.Usage.TotalTokens)
	require.Equal(t, int(cacheRead), resp.Usage.CacheReadTokens)
	require.Equal(t, int(cacheWrite), resp.Usage.CacheWriteTokens)
	require.Equal(t, "test-model", resp.Usage.Model)
	require.Equal(t, model.ModelClassDefault, resp.Usage.ModelClass)
}
