// Count candidates, summary requests and final requests all pass through the
// same uncached image reader. Scripted token estimates exercise the actual
// Compress graph; they make no assertion about provider image capacity.
package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genpictures "goa.design/goa-ai/internal/testimage/gen/images/toolsets/pictures"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/storage/inmem"
)

type (
	imageWorkProvider struct {
		counts      []int
		countIndex  int
		operations  []string
		occurrences []int
		onDispatch  func()
	}
)

const imageWorkSummary = "summarize"

func TestNativeImageCompressActualWorkGraph(t *testing.T) {
	for _, tc := range []struct {
		name       string
		config     HistoryCompressionConfig
		prior      bool
		counts     []int
		operations []string
		images     []int
	}{
		{"unchanged", HistoryCompressionConfig{CompressAtMaxInputTokens: 200, KeepMaxTurns: 3, AllowEstimatedTokens: true},
			false, []int{100}, []string{"count", "final"}, []int{5, 5}},
		{"turn only", HistoryCompressionConfig{CompressAtTurns: 5, KeepMaxTurns: 1},
			false, nil, []string{imageWorkSummary, "final"}, []int{4, 1}},
		{"new summary longest search", HistoryCompressionConfig{CompressAtMaxInputTokens: 200, KeepMaxTurns: 3, AllowEstimatedTokens: true},
			false, []int{301, 50, 100, 150, 201, 201, 200},
			[]string{"count", "count", "count", "count", imageWorkSummary, "count", "count", "count", "final"},
			[]int{5, 1, 2, 3, 4, 3, 2, 1, 1}},
		{"prior reuse succeeds", HistoryCompressionConfig{CompressAtMaxInputTokens: 200, KeepMaxTurns: 3, AllowEstimatedTokens: true},
			true, []int{301, 50, 100, 150, 200},
			[]string{"count", "count", "count", "count", "count", "final"},
			[]int{3, 1, 2, 3, 3, 3}},
		{"failed prior reuse then new summary maximum", HistoryCompressionConfig{CompressAtMaxInputTokens: 200, KeepMaxTurns: 3, AllowEstimatedTokens: true},
			true, []int{301, 50, 100, 150, 201, 201, 201, 201, 200},
			[]string{"count", "count", "count", "count", "count", "count", imageWorkSummary, "count", "count", "count", "final"},
			[]int{3, 1, 2, 3, 3, 2, 4, 3, 2, 1, 1}},
		{"no keep turn cap returns original", HistoryCompressionConfig{CompressAtMaxInputTokens: 200, KeepMaxInputTokens: 999, AllowEstimatedTokens: true},
			false, []int{301, 50, 60, 70, 80, 90},
			[]string{"count", "count", "count", "count", "count", "count", "final"},
			[]int{5, 1, 2, 3, 4, 5, 5}},
		{"uncapped maximum failed prior reuse", HistoryCompressionConfig{CompressAtMaxInputTokens: 200, KeepMaxInputTokens: 999, AllowEstimatedTokens: true},
			true, []int{301, 50, 60, 70, 80, 201, 201, 201, 201, 201, 201, 201, 200},
			[]string{"count", "count", "count", "count", "count", "count", "count", "count", "count", imageWorkSummary, "count", "count", "count", "count", "final"},
			[]int{3, 1, 2, 3, 4, 5, 4, 3, 2, 4, 4, 3, 2, 1, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			image := nativeImagePNG(t)
			messages := nativeImageHistory(t, int64(len(image.Bytes)))
			before := canonicalHistory(t, messages)
			provider := &imageWorkProvider{counts: tc.counts}
			var reads, bytesRead, active, peak int
			rt := New(inmem.New(), WithImageSourceResolver(genpictures.NativeImageSources(),
				func(ctx context.Context, current run.Context, source model.ImageSourcePart, allowance int64) (model.ImagePart, error) {
					require.Equal(t, "current-holder", current.SessionID)
					require.NoError(t, ctx.Err())
					require.GreaterOrEqual(t, allowance, int64(len(image.Bytes)+len(image.Format)))
					active++
					peak = max(peak, active)
					reads++
					bytesRead += len(image.Bytes)
					active--
					return image, nil
				}))
			base := historyTestClient(t, provider)
			counter := rt.imageSourceClient(base, run.Context{SessionID: "current-holder"})
			policy := Compress(base, tc.config)
			var prior *HistorySummary
			if tc.prior {
				prior = nativeImagePrior(t, messages, 6, 4)
			}
			client, err := model.WithRequestPreparation(counter, func(ctx context.Context, req *model.Request) (context.Context, *model.Request, error) {
				result, err := policy(ctx, req, counter.(model.TokenCounter), prior)
				if err != nil {
					return ctx, nil, err
				}
				req.Messages = result.Messages
				return ctx, req, nil
			})
			require.NoError(t, err)
			_, err = client.Complete(ctx, &model.Request{Model: "fixture", Messages: messages, Tools: fitTools()})
			require.NoError(t, err)
			assert.Equal(t, tc.operations, provider.operations)
			assert.Equal(t, tc.images, provider.occurrences)
			assert.Equal(t, len(tc.counts), provider.countIndex)
			sum := 0
			for _, count := range tc.images {
				sum += count
			}
			assert.Equal(t, sum, reads, "every actual occurrence is freshly read")
			assert.Equal(t, sum*len(image.Bytes), bytesRead)
			assert.Equal(t, 1, peak, "reader concurrency is one")
			assert.Equal(t, before, canonicalHistory(t, messages))
		})
	}
}

func TestNativeImageCompressCapacityAndAccessFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failID    string
		err       error
		wantCalls int
	}{
		{"newest capacity", "image-5", model.ErrImageSourceCapacity, 0},
		{"summary capacity", "image-1", model.ErrImageSourceCapacity, 1},
		{"authorization is not capacity", "image-1", errors.New("access denied"), 0},
		{"checksum is not capacity", "image-1", errors.New("checksum mismatch"), 0},
		{"missing is not capacity", "image-1", errors.New("missing retained image"), 0},
		{"transport is not capacity", "image-1", errors.New("read transport failed"), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			image := nativeImagePNG(t)
			provider := &imageWorkProvider{counts: []int{50}}
			rt := New(inmem.New(), WithImageSourceResolver(genpictures.NativeImageSources(),
				func(_ context.Context, _ run.Context, source model.ImageSourcePart, _ int64) (model.ImagePart, error) {
					descriptor, err := genpictures.ViewFixtureImageV1ServerDataCodec().FromJSON(source.Data)
					require.NoError(t, err)
					if descriptor.ID == tc.failID {
						// Only this scripted known-byte capacity failure permits
						// optional shrink. Other error classes stop immediately.
						return model.ImagePart{}, tc.err
					}
					return image, nil
				}))
			base := historyTestClient(t, provider)
			inner := rt.imageSourceClient(base, run.Context{})
			policy := Compress(base, HistoryCompressionConfig{CompressAtMaxInputTokens: 200, KeepMaxTurns: 1, AllowEstimatedTokens: true})
			client := newRequestConfiguredClient(inner, CachePolicy{}, policy, "fixture", nil, nil)
			_, err := client.Complete(ctx, &model.Request{Model: "fixture", Messages: nativeImageHistory(t, int64(len(image.Bytes)))})
			require.ErrorIs(t, err, tc.err)
			assert.Len(t, provider.operations, tc.wantCalls)
			assert.NotContains(t, provider.operations, imageWorkSummary)
			assert.NotContains(t, provider.operations, "final")
		})
	}
}

func TestNativeImageCompressCancellationStopsNextCandidate(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	image := nativeImagePNG(t)
	reads := 0
	provider := &imageWorkProvider{counts: []int{301}, onDispatch: cancel}
	rt := New(inmem.New(), WithImageSourceResolver(genpictures.NativeImageSources(),
		func(context.Context, run.Context, model.ImageSourcePart, int64) (model.ImagePart, error) {
			reads++
			return image, nil
		}))
	base := historyTestClient(t, provider)
	client := newRequestConfiguredClient(rt.imageSourceClient(base, run.Context{}), CachePolicy{},
		Compress(base, HistoryCompressionConfig{CompressAtMaxInputTokens: 200, KeepMaxTurns: 3, AllowEstimatedTokens: true}),
		"fixture", nil, nil)
	_, err := client.Complete(ctx, &model.Request{Model: "fixture", Messages: nativeImageHistory(t, int64(len(image.Bytes)))})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 5, reads)
	assert.Equal(t, []string{"count"}, provider.operations)
}

func (p *imageWorkProvider) CountTokens(_ context.Context, request *model.Request) (model.TokenCount, error) {
	if p.countIndex >= len(p.counts) {
		return model.TokenCount{}, errors.New("unexpected count candidate")
	}
	value := p.counts[p.countIndex]
	p.countIndex++
	p.record("count", request)
	return model.TokenCount{InputTokens: value, Exact: false, Model: "fixture"}, nil
}

func (p *imageWorkProvider) Complete(_ context.Context, request *model.Request) (*model.Response, error) {
	operation := "final"
	if len(request.Messages) > 0 && len(request.Messages[0].Parts) > 0 {
		if text, ok := request.Messages[0].Parts[0].(model.TextPart); ok && text.Text == historySummaryInstruction {
			operation = imageWorkSummary
		}
	}
	p.record(operation, request)
	return &model.Response{Content: []model.Message{{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "Summary"}}}}, StopReason: "stop"}, nil
}

func (p *imageWorkProvider) Stream(context.Context, *model.Request) (model.Streamer, error) {
	return nil, model.ErrStreamingUnsupported
}

func (p *imageWorkProvider) record(operation string, request *model.Request) {
	images := 0
	for _, message := range request.Messages {
		for _, part := range message.Parts {
			switch part.(type) {
			case model.ImagePart:
				images++
			case model.ImageSourcePart:
				panic("provider received an unresolved source")
			}
		}
	}
	p.operations = append(p.operations, operation)
	p.occurrences = append(p.occurrences, images)
	if p.onDispatch != nil {
		p.onDispatch()
	}
}

func nativeImageHistory(t *testing.T, size int64) []*model.Message {
	t.Helper()
	messages := []*model.Message{systemMsg()}
	for i := 1; i <= 5; i++ {
		messages = append(messages, &model.Message{Role: model.ConversationRoleUser, Parts: []model.Part{
			model.TextPart{Text: fmt.Sprintf("Question %d", i)}, nativeImageSource(t, fmt.Sprintf("image-%d", i), size),
		}}, assistantTextMsg(fmt.Sprintf("Answer %d", i)))
	}
	return messages
}

func nativeImagePrior(t *testing.T, messages []*model.Message, source, replaced int) *HistorySummary {
	t.Helper()
	cfg := defaultCompressConfig()
	policy := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("history-summary-v2\n%q\n%q\n%q",
		historySummaryInstruction, cfg.summaryPrompt, cfg.summaryRole))))
	_, fingerprint, err := historySummarySource(messages, source, policy)
	require.NoError(t, err)
	return &HistorySummary{SourceMessages: source, ReplacedMessages: replaced, PolicyFingerprint: fingerprint,
		Message: model.Message{Role: model.ConversationRoleSystem, Parts: []model.Part{model.TextPart{Text: "[Conversation Summary]\n" + strings.Repeat("previous ", 2)}}, Meta: map[string]any{"goa_ai_history": "summary"}}}
}

func nativeImagePNG(t *testing.T) model.ImagePart {
	t.Helper()
	var data bytes.Buffer
	require.NoError(t, png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1, 1))))
	return model.ImagePart{Format: model.ImageFormatPNG, Bytes: data.Bytes()}
}
