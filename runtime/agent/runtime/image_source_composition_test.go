// Fresh run bindings and summary bindings share unbound model bases. Actual
// Compress calls must read every candidate under the same current run while
// preserving provider observers and ordinary request preparations.
package runtime

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genpictures "goa.design/goa-ai/internal/testimage/gen/images/toolsets/pictures"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/storage/inmem"
)

type imageCompositionProvider struct {
	*imageWorkProvider
	observed int
}

var _ model.ProviderCallObserver = (*imageCompositionProvider)(nil)

func TestNativeImageRunAndSummaryBindingComposition(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	image := nativeImagePNG(t)
	var holders []string
	rt := New(inmem.New(), WithImageSourceResolver(genpictures.NativeImageSources(),
		func(_ context.Context, current run.Context, _ model.ImageSourcePart, _ int64) (model.ImagePart, error) {
			holders = append(holders, current.SessionID)
			return image, nil
		}))
	provider := &imageCompositionProvider{imageWorkProvider: &imageWorkProvider{}}
	base := historyTestClient(t, provider)
	preparations := 0
	base, err := model.WithRequestPreparation(base, func(ctx context.Context, req *model.Request) (context.Context, *model.Request, error) {
		preparations++
		return ctx, req, nil
	})
	require.NoError(t, err)
	base, err = model.WrapClient(base, func(raw model.Provider) model.Provider {
		return struct {
			model.Provider
			model.TokenCounter
		}{raw, raw.(model.TokenCounter)}
	})
	require.NoError(t, err)
	messages := nativeImageHistory(t, int64(len(image.Bytes)))
	before := canonicalHistory(t, messages)
	for _, holder := range []string{"first-run", "second-run"} {
		for _, streaming := range []bool{false, true} {
			provider.counts = []int{301, 50, 100}
			provider.countIndex = 0
			provider.operations, provider.occurrences = nil, nil
			provider.observed, preparations, holders = 0, 0, nil
			bound := rt.imageSourceClient(base, run.Context{SessionID: holder})
			client := newRequestConfiguredClient(bound, CachePolicy{},
				Compress(base, HistoryCompressionConfig{
					CompressAtMaxInputTokens: 200, KeepMaxTurns: 1, AllowEstimatedTokens: true,
				}), "fixture", nil, nil)
			request := &model.Request{Model: "fixture", Messages: messages, Tools: fitTools()}
			if streaming {
				stream, err := client.Stream(ctx, request)
				require.NoError(t, err)
				for {
					_, err := stream.Recv()
					if errors.Is(err, io.EOF) {
						break
					}
					require.NoError(t, err)
				}
				require.NoError(t, stream.Close())
			} else {
				_, err := client.Complete(ctx, request)
				require.NoError(t, err)
			}
			final := "final"
			if streaming {
				final = "stream"
			}
			assert.Equal(t, []string{"count", "count", imageWorkSummary, "count", final}, provider.operations)
			assert.Equal(t, []int{5, 1, 4, 1, 1}, provider.occurrences)
			require.Len(t, holders, 12)
			for _, actual := range holders {
				assert.Equal(t, holder, actual)
			}
			assert.Equal(t, 2, preparations, "summary and final each preserve base preparation")
			assert.Equal(t, 2, provider.observed, "summary and final each preserve the provider observer")
			assert.Equal(t, before, canonicalHistory(t, messages))
		}
	}
}

func (p *imageCompositionProvider) PrepareClientCall(ctx context.Context, _ *model.Request) (context.Context, model.ClientCallObserver, error) {
	p.observed++
	return ctx, nil, nil
}

func (p *imageCompositionProvider) Stream(_ context.Context, request *model.Request) (model.Streamer, error) {
	p.record("stream", request)
	message := model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "Summary"}}}
	return &chunkStreamer{
		chunks:   []model.Chunk{model.TextChunk{Message: message}, model.StopChunk{Reason: "stop"}},
		response: &model.Response{Content: []model.Message{message}, StopReason: "stop"},
	}, nil
}
