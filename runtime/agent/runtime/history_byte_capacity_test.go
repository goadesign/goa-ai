// These tests run the real history policy through validated model clients.
// Scripted local admission errors prove selection and failure behavior, not a
// particular transport's byte measurement or any external model's capacity.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// finalCapacityProvider counts selected candidates but rejects the final
	// invocation, proving that a successful count is not admission for that call.
	finalCapacityProvider struct {
		*fitProvider
		completed []*model.Request
		streamed  []*model.Request
	}
)

func TestCompressRequestByteCapacitySelectsWholeTurns(t *testing.T) {
	capacity := fmt.Errorf("synthetic encoded request: %w", model.ErrRequestByteCapacity)
	for _, tc := range []struct {
		name       string
		images     bool
		structured bool
		counts     []fitCount
	}{
		{"text only older growth", false, false, []fitCount{{err: capacity}, {tokens: 50}, {tokens: 100}, {err: capacity}, {tokens: 150}}},
		{"native images older growth", true, false, []fitCount{{err: capacity}, {tokens: 50}, {tokens: 100}, {err: capacity}, {tokens: 150}}},
		{"summary fit", false, false, []fitCount{{err: capacity}, {tokens: 50}, {tokens: 100}, {tokens: 150}, {err: capacity}, {tokens: 180}}},
		{"structured output", false, true, []fitCount{{err: capacity}, {tokens: 50}, {tokens: 100}, {err: capacity}, {tokens: 150}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := capacityHistoryRequest()
			if tc.structured {
				request.Tools = nil
				request.ToolChoice = nil
				request.StructuredOutput = &model.StructuredOutput{
					Schema: []byte(`{"type":"object","properties":{"message":{"type":"string"}}}`),
					Name:   "reply",
				}
			}
			if tc.images {
				image := nativeImagePNG(t)
				request.Messages[1].Parts = append(request.Messages[1].Parts, image)
				request.Messages[len(request.Messages)-2].Parts = append(request.Messages[len(request.Messages)-2].Parts, image)
			}
			before := canonicalHistory(t, request.Messages)
			provider := &fitProvider{evidenceProvider: evidenceSummaryProvider(model.TextPart{Text: "Retained observations"}), counts: tc.counts}
			client := historyTestClient(t, provider)
			result, err := Compress(client, HistoryCompressionConfig{CompressAtMaxInputTokens: 200, KeepMaxTurns: 3})(t.Context(), request, client, nil)
			require.NoError(t, err)
			require.NotNil(t, result.Summary)
			assert.Equal(t, 6, result.Summary.SourceMessages)
			assert.Equal(t, 4, result.Summary.ReplacedMessages)
			require.Len(t, result.Messages, 7)
			assert.Same(t, request.Messages[0], result.Messages[0])
			assert.Equal(t, "[Conversation Summary]\nRetained observations", textPart(t, result.Messages[1]))
			assert.Equal(t, append([]*model.Message{request.Messages[3]}, request.Messages[len(request.Messages)-4:]...), result.Messages[2:])
			assert.Equal(t, before, canonicalHistory(t, request.Messages))
			assert.Equal(t, 1, provider.completeCalls)
			require.Len(t, provider.requests, len(tc.counts))
			for _, counted := range provider.requests {
				assertCapacityRequestSettings(t, request, counted)
			}
			assert.Equal(t, canonicalHistory(t, result.Messages), canonicalHistory(t, provider.requests[len(provider.requests)-1].Messages))
			quoted := textPart(t, provider.request.Messages[1])
			assert.Contains(t, quoted, "oldest question")
			assert.Contains(t, quoted, "older question")
			assert.Contains(t, quoted, "recent question")
			assert.NotContains(t, quoted, "newest question")
			if tc.images {
				assert.Equal(t, []model.ImagePart{nativeImagePNG(t)}, capacityImages(provider.request.Messages))
				assert.Equal(t, []model.ImagePart{nativeImagePNG(t)}, capacityImages(result.Messages))
			}
		})
	}
}

func TestCompressRequestByteCapacityRequiredCandidatesStop(t *testing.T) {
	capacity := fmt.Errorf("synthetic local admission: %w", model.ErrRequestByteCapacity)
	for _, tc := range []struct {
		name      string
		newest    bool
		counts    []fitCount
		summaries int
	}{
		{"newest complete turn", true, []fitCount{{err: capacity}, {err: capacity}}, 0},
		{"fixed tools and instructions", false, []fitCount{{err: capacity}, {err: capacity}}, 0},
		{"summary plus newest", false, []fitCount{{err: capacity}, {tokens: 50}, {tokens: 100}, {tokens: 150}, {err: capacity}, {err: capacity}, {err: capacity}}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := capacityHistoryRequest()
			if tc.newest {
				request.Messages = append([]*model.Message{request.Messages[0]}, request.Messages[len(request.Messages)-2:]...)
			}
			before := canonicalHistory(t, request.Messages)
			provider := &fitProvider{evidenceProvider: evidenceSummaryProvider(model.TextPart{Text: "Summary"}), counts: tc.counts}
			client := historyTestClient(t, provider)
			result, err := Compress(client, HistoryCompressionConfig{CompressAtMaxInputTokens: 200, KeepMaxTurns: 3})(t.Context(), request, client, nil)
			require.ErrorIs(t, err, model.ErrRequestByteCapacity)
			var terminal *model.RequestValidationError
			require.ErrorAs(t, err, &terminal)
			assert.Equal(t, before, canonicalHistory(t, request.Messages))
			assert.Equal(t, request.Messages, result.Messages)
			assert.Nil(t, result.Summary)
			assert.Equal(t, tc.summaries, provider.completeCalls)
			require.Len(t, provider.requests, len(tc.counts))
			for _, counted := range provider.requests {
				assertCapacityRequestSettings(t, request, counted)
			}
			newest := provider.requests[1].Messages
			assert.Contains(t, newest, request.Messages[0])
			assert.Equal(t, request.Messages[len(request.Messages)-2:], newest[len(newest)-2:])
		})
	}
}

func TestCompressRequestByteCapacitySummaryRetainsCompleteEvidence(t *testing.T) {
	image := nativeImagePNG(t)
	request := capacityHistoryRequest()
	request.Messages[1].Parts = append(request.Messages[1].Parts, image)
	request.Messages[6].Parts = append(request.Messages[6].Parts, image)
	request.Messages[len(request.Messages)-2].Parts = append(request.Messages[len(request.Messages)-2].Parts, image)
	before := canonicalHistory(t, request.Messages)
	capacity := fmt.Errorf("complete summary request: %w", model.ErrRequestByteCapacity)
	provider := &fitProvider{
		evidenceProvider: &evidenceProvider{err: capacity},
		counts:           []fitCount{{err: capacity}, {tokens: 50}, {tokens: 100}, {tokens: 150}},
	}
	client := historyTestClient(t, provider)
	result, err := Compress(client, HistoryCompressionConfig{CompressAtMaxInputTokens: 200, KeepMaxTurns: 3})(t.Context(), request, client, nil)
	require.ErrorIs(t, err, capacity)
	assert.Equal(t, request.Messages, result.Messages)
	assert.Nil(t, result.Summary)
	assert.Equal(t, before, canonicalHistory(t, request.Messages))
	require.Equal(t, 1, provider.completeCalls)
	assert.Len(t, provider.requests, 4)
	assert.Equal(t, []model.ImagePart{image, image}, capacityImages(provider.request.Messages))
	quoted := textPart(t, provider.request.Messages[1])
	for _, text := range []string{"oldest question", "older question", "recent question", "middle instruction"} {
		assert.Contains(t, quoted, text)
	}
	assert.NotContains(t, quoted, "newest question")
}

func TestCompressRequestByteCapacityReusesCoveredSummary(t *testing.T) {
	capacity := model.ErrRequestByteCapacity
	provider := &fitProvider{
		evidenceProvider: evidenceSummaryProvider(model.TextPart{Text: "Original evidence"}),
		counts: []fitCount{
			{err: capacity}, {tokens: 50}, {tokens: 100}, {tokens: 150}, {tokens: 180},
			{err: capacity}, {tokens: 50}, {tokens: 100}, {tokens: 150}, {err: capacity}, {err: capacity}, {tokens: 180},
			{err: capacity}, {tokens: 50}, {tokens: 100}, {tokens: 150}, {err: capacity}, {err: capacity}, {err: capacity},
		},
	}
	request := capacityHistoryRequest()
	before := canonicalHistory(t, request.Messages)
	client := historyTestClient(t, provider)
	policy := Compress(client, HistoryCompressionConfig{CompressAtMaxInputTokens: 200, KeepMaxTurns: 3})
	first, err := policy(t.Context(), request, client, nil)
	require.NoError(t, err)
	require.NotNil(t, first.Summary)
	saved := *first.Summary
	second, err := policy(t.Context(), request, client, first.Summary)
	require.NoError(t, err)
	require.NotNil(t, second.Summary)
	assert.Equal(t, 6, second.Summary.SourceMessages)
	assert.Equal(t, 6, second.Summary.ReplacedMessages)
	assert.Equal(t, saved.Message, second.Summary.Message)
	assert.Equal(t, saved, *first.Summary)
	_, err = policy(t.Context(), request, client, second.Summary)
	require.ErrorIs(t, err, capacity)
	assert.Equal(t, 1, provider.completeCalls, "byte-fit failures never regenerate the same summary")
	assert.Len(t, provider.requests, len(provider.counts))
	assert.Equal(t, before, canonicalHistory(t, request.Messages))
}

func TestCompressRequestByteCapacityDoesNotReclassifyOtherErrors(t *testing.T) {
	contract, err := model.NewRequestContract(&model.Request{})
	require.NoError(t, err)
	output := contract.RejectResponse(model.OutputValidationResponseShape, nil, errors.New("invalid model output"))
	for _, cause := range []error{
		model.NewRequestValidationError(errors.New(model.ErrRequestByteCapacity.Error())),
		model.NewProviderError("synthetic", "count", 503, model.ProviderErrorKindUnavailable, "unavailable", "provider failed", "", true, nil),
		status.Error(codes.ResourceExhausted, "remote request capacity"),
		status.Error(codes.PermissionDenied, "access denied"),
		errors.New("retained image missing"),
		errors.New("image checksum mismatch"),
		context.Canceled,
		context.DeadlineExceeded,
		model.ErrTokenCountingUnsupported,
		model.ErrRateLimited,
		output,
	} {
		t.Run(cause.Error(), func(t *testing.T) {
			for _, stage := range []struct {
				name      string
				prefix    []fitCount
				summaries int
			}{
				{"trigger", nil, 0},
				{"older growth", []fitCount{{tokens: 301}, {tokens: 50}}, 0},
				{"summary fit", []fitCount{{tokens: 301}, {tokens: 50}, {tokens: 100}, {tokens: 150}}, 1},
			} {
				t.Run(stage.name, func(t *testing.T) {
					request := capacityHistoryRequest()
					before := canonicalHistory(t, request.Messages)
					counts := slices.Clone(stage.prefix)
					counts = append(counts, fitCount{err: cause})
					provider := &fitProvider{evidenceProvider: evidenceSummaryProvider(model.TextPart{Text: "Summary"}), counts: counts}
					client := historyTestClient(t, provider)
					result, err := Compress(client, HistoryCompressionConfig{CompressAtMaxInputTokens: 200, KeepMaxTurns: 3})(t.Context(), request, client, nil)
					require.ErrorIs(t, err, cause)
					require.NotErrorIs(t, err, model.ErrRequestByteCapacity)
					assert.Equal(t, request.Messages, result.Messages)
					assert.Equal(t, before, canonicalHistory(t, request.Messages))
					assert.Equal(t, stage.summaries, provider.completeCalls)
					assert.Len(t, provider.requests, len(counts))
				})
			}
		})
	}
}

func TestCompressRequestByteCapacityFinalCallIsIndependent(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			request := capacityHistoryRequest()
			request.Stream = stream
			before := canonicalHistory(t, request.Messages)
			summary := evidenceSummaryProvider(model.TextPart{Text: "Summary"})
			provider := &finalCapacityProvider{fitProvider: &fitProvider{
				counts: []fitCount{{err: model.ErrRequestByteCapacity}, {tokens: 50}, {tokens: 100}, {tokens: 150}, {tokens: 180}},
			}}
			base := historyTestClient(t, provider)
			client := newRequestConfiguredClient(base, CachePolicy{}, Compress(historyTestClient(t, summary), HistoryCompressionConfig{CompressAtMaxInputTokens: 200, KeepMaxTurns: 3}), "synthetic.agent", request.Messages, nil)
			var err error
			if stream {
				var result model.Streamer
				result, err = client.Stream(t.Context(), request)
				assert.Nil(t, result)
				assert.Empty(t, provider.completed)
				assert.Len(t, provider.streamed, 1)
			} else {
				var result *model.Response
				result, err = client.Complete(t.Context(), request)
				assert.Nil(t, result)
				assert.Empty(t, provider.streamed)
				assert.Len(t, provider.completed, 1)
			}
			require.ErrorIs(t, err, model.ErrRequestByteCapacity)
			var terminal *model.RequestValidationError
			require.ErrorAs(t, err, &terminal)
			assert.Equal(t, 1, summary.completeCalls)
			require.Len(t, provider.requests, 5)
			final := append(provider.completed, provider.streamed...)[0]
			assert.Equal(t, canonicalHistory(t, provider.requests[4].Messages), canonicalHistory(t, final.Messages))
			assertCapacityRequestSettings(t, request, final)
			assert.Equal(t, before, canonicalHistory(t, request.Messages))
		})
	}
}

// capacityHistoryRequest includes an instruction between older turns. It must
// remain exact even when those turns are replaced by a summary.
func capacityHistoryRequest() *model.Request {
	messages := fitHistory()
	messages = append(messages[:3:3], append([]*model.Message{historySystemMessage("middle instruction")}, messages[3:]...)...)
	return &model.Request{
		Model: "synthetic-history", ModelClass: model.ModelClassSmall,
		Messages: messages, Tools: fitTools(), MaxTokens: 777, Temperature: 0.25,
		Thinking:   &model.ThinkingOptions{Enable: true, BudgetTokens: 400},
		Cache:      &model.CacheOptions{AfterSystem: true, AfterTools: true},
		ToolChoice: &model.ToolChoice{Mode: "auto"},
	}
}

// assertCapacityRequestSettings checks the fixed context independently of the
// candidate's selected messages; smaller history must not remove other costs.
func assertCapacityRequestSettings(t *testing.T, want, got *model.Request) {
	t.Helper()
	assert.Equal(t, want.Model, got.Model)
	assert.Equal(t, want.ModelClass, got.ModelClass)
	assert.Equal(t, math.Float32bits(want.Temperature), math.Float32bits(got.Temperature))
	assert.Equal(t, want.MaxTokens, got.MaxTokens)
	assert.Equal(t, want.Stream, got.Stream)
	assert.Equal(t, want.Thinking, got.Thinking)
	assert.Equal(t, want.Cache, got.Cache)
	assert.Equal(t, want.ToolChoice, got.ToolChoice)
	assert.Equal(t, want.StructuredOutput, got.StructuredOutput)
	assertToolDefinitionsEqual(t, want.Tools, got.Tools)
}

func capacityImages(messages []*model.Message) []model.ImagePart {
	var images []model.ImagePart
	for _, message := range messages {
		for _, part := range message.Parts {
			if image, ok := part.(model.ImagePart); ok {
				images = append(images, image)
			}
		}
	}
	return images
}

func (p *finalCapacityProvider) Complete(_ context.Context, request *model.Request) (*model.Response, error) {
	p.completed = append(p.completed, request)
	return nil, model.ErrRequestByteCapacity
}

func (p *finalCapacityProvider) Stream(_ context.Context, request *model.Request) (model.Streamer, error) {
	p.streamed = append(p.streamed, request)
	return nil, model.ErrRequestByteCapacity
}
