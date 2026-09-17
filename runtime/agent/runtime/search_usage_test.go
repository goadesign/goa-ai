// Multi-request model calls retain failed usage through the same invocation
// journal used by ordinary model responses. Failure counts replace deltas.
package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestModelInvocationRetainsFailedSearchUsage(t *testing.T) {
	usage := model.TokenUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}
	failure := model.RetainUsage(context.Canceled, usage)
	for _, streaming := range []bool{false, true} {
		invocations := &modelInvocationJournal{}
		client := newTestModelInvocationClient(stubModelClient{
			complete: func(context.Context, *model.Request) (*model.Response, error) {
				return nil, failure
			},
			stream: func(context.Context, *model.Request) (model.Streamer, error) {
				return &chunkStreamer{
					chunks:      []model.Chunk{model.UsageChunk{Usage: model.TokenUsage{TotalTokens: 4}}},
					terminalErr: failure,
				}, nil
			},
		}, invocations)
		if streaming {
			stream, err := client.Stream(t.Context(), &model.Request{})
			require.NoError(t, err)
			_, err = stream.Recv()
			require.NoError(t, err)
			_, err = stream.Recv()
			require.ErrorIs(t, err, context.Canceled)
			require.NoError(t, stream.Close())
		} else {
			_, err := client.Complete(t.Context(), &model.Request{})
			require.ErrorIs(t, err, context.Canceled)
		}
		assert.Equal(t, 12, invocations.exportUsage().TotalTokens)
		assert.Nil(t, invocations.recoverableModelInvocationRecovery())
	}
}
