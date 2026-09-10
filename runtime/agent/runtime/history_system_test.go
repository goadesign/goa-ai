package runtime

// History may remove old conversation, but never live System instructions.
// These tests cover instructions around tools, final-answer evidence, and every
// candidate measured while selecting a fitting conversation.

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestHistorySystemMessagesKeepOriginalPlacement(t *testing.T) {
	for _, compress := range []bool{false, true} {
		name := "keep recent"
		if compress {
			name = "compress"
		}
		t.Run(name, func(t *testing.T) {
			beforeUser := historySystemMessage("Before current question")
			beforeCall := historySystemMessage("Before tool call")
			afterResult := historySystemMessage("After tool result")
			last := historySystemMessage("After newest question")
			messages := []*model.Message{systemMsg(), userMsg("Old question"), assistantTextMsg("Old answer"), beforeUser, userMsg("Current question"), beforeCall, assistantToolUseMsg("read", "lookup"), toolResultMsg("read", "value"), afterResult, userMsg("Newest question"), last}
			require.NoError(t, transcript.ValidatePlannerTranscript(messages))
			before := canonicalHistory(t, messages)
			for _, keep := range []int{1, 2} {
				provider := evidenceSummaryProvider(model.TextPart{Text: "Earlier observations"})
				policy := KeepRecentTurns(keep)
				if compress {
					policy = Compress(historyTestClient(t, provider), HistoryCompressionConfig{CompressAtTurns: 3, KeepMaxTurns: keep})
				}
				historyResult, err := policy(t.Context(), &model.Request{Messages: messages, Tools: fitTools()}, nil, nil)
				out := historyResult.Messages
				require.NoError(t, err)
				require.NoError(t, transcript.ValidatePlannerTranscript(out))
				want := []*model.Message{messages[0]}
				if compress {
					want = append(want, out[1])
					assert.Equal(t, "summary", out[1].Meta["goa_ai_history"])
					quoted := textPart(t, provider.request.Messages[1])
					assert.NotContains(t, quoted, "Before current question")
					assert.NotContains(t, quoted, "Before tool call")
					assert.NotContains(t, quoted, "After tool result")
				}
				want = append(want, beforeUser)
				if keep == 2 {
					want = append(want, messages[4:9]...)
				} else {
					want = append(want, beforeCall, afterResult)
				}
				want = append(want, messages[9:]...)
				assertExactHistory(t, want, out)
				assert.Equal(t, before, canonicalHistory(t, messages))
			}
		})
	}
}

// A final-answer request may place current instructions before a newly appended
// user message containing completed results. Those instructions belong to an
// older parsed turn, but remain exact when that turn's document is summarized.
func TestHistorySystemMessagesSurviveFinalAnswerProjection(t *testing.T) {
	provider := evidenceSummaryProvider(model.TextPart{Text: "Older document evidence"})
	between := historySystemMessage("Instruction before the document")
	current := historySystemMessage("All requested work is complete; include the saved artifact")
	last := historySystemMessage("Current response format")
	messages := []*model.Message{
		systemMsg(), userMsg("Older question"), assistantTextMsg("Older answer"), between,
		{Role: model.ConversationRoleUser, Parts: []model.Part{model.DocumentPart{Name: "reference", Format: "txt", Text: "Older document body"}}},
		current, userMsg("Completed tool results: the requested artifact is ready"), last,
	}
	before := canonicalHistory(t, messages)
	policy := Compress(historyTestClient(t, provider), HistoryCompressionConfig{CompressAtTurns: 3, KeepMaxTurns: 1})
	historyResult, err := policy(t.Context(), &model.Request{Messages: messages}, nil, nil)
	out := historyResult.Messages
	require.NoError(t, err)
	require.Len(t, out, 6)
	assertExactHistory(t, []*model.Message{messages[0], out[1], between, current, messages[6], last}, out)
	assert.Equal(t, before, canonicalHistory(t, messages))
	quoted := textPart(t, provider.request.Messages[1])
	assert.NotContains(t, quoted, "Instruction before the document")
	assert.NotContains(t, quoted, "All requested work is complete")
	assert.NotContains(t, quoted, "Current response format")
	assert.NotContains(t, quoted, "History message 3,")
	assert.Contains(t, quoted, "History message 4, part 0")
	require.Len(t, provider.request.Messages, 3)
	assert.Equal(t, model.TextPart{Text: "History message 4, part 0"}, provider.request.Messages[2].Parts[0])
}

func TestHistorySystemMessagesCountInEveryCandidate(t *testing.T) {
	provider := &fitProvider{
		evidenceProvider: evidenceSummaryProvider(model.TextPart{Text: "Earlier evidence"}),
		counts:           []fitCount{{tokens: 100}, {tokens: 160}, {tokens: 190}, {tokens: 201}, {tokens: 200}},
	}
	messages := fitHistory()
	old := historySystemMessage("Required instruction in discarded history")
	middle := historySystemMessage("Required instruction in optional history")
	last := historySystemMessage("Required instruction after newest answer")
	messages = append(messages[:3:3], append([]*model.Message{old}, messages[3:]...)...)
	messages = append(messages[:6:6], append([]*model.Message{middle}, messages[6:]...)...)
	messages = append(messages, last)
	before := canonicalHistory(t, messages)
	client := historyTestClient(t, provider)
	historyResult, err := Compress(client, HistoryCompressionConfig{CompressAtTurns: 4, KeepMaxTurns: 3, CompressAtMaxInputTokens: 200})(t.Context(), &model.Request{Messages: messages, Tools: fitTools()}, client, nil)
	out := historyResult.Messages
	require.NoError(t, err)
	require.Len(t, provider.requests, 5)
	for _, counted := range provider.requests {
		var instructions []*model.Message
		for _, message := range counted.Messages {
			if message.Role == model.ConversationRoleSystem && message.Meta["goa_ai_history"] != "summary" {
				instructions = append(instructions, message)
			}
		}
		assert.Equal(t, canonicalHistory(t, []*model.Message{messages[0], old, middle, last}), canonicalHistory(t, instructions))
		assert.Equal(t, canonicalHistory(t, []*model.Message{last}), canonicalHistory(t, counted.Messages[len(counted.Messages)-1:]))
	}
	assert.Equal(t, canonicalHistory(t, out), canonicalHistory(t, provider.requests[4].Messages))
	assert.Equal(t, before, canonicalHistory(t, messages))
}

func TestHistorySystemMessagesAreFixedTokenCost(t *testing.T) {
	for _, ceiling := range []int{1000, 99} {
		provider := evidenceSummaryProvider(model.TextPart{Text: "Earlier answer"})
		instruction := historySystemMessage(strings.Repeat("instruction ", 20))
		messages := []*model.Message{systemMsg(), userMsg("old"), assistantTextMsg("old answer"), instruction, userMsg("recent"), assistantTextMsg("recent answer"), userMsg("newest")}
		var counts int
		counter := historyCounterFunc(func(_ context.Context, req *model.Request) (model.TokenCount, error) {
			counts++
			tokens := 0
			for _, message := range req.Messages {
				if message == instruction {
					tokens += 90
				} else if message.Role != model.ConversationRoleSystem {
					tokens += 10
				}
			}
			return model.TokenCount{InputTokens: tokens, Exact: true}, nil
		})
		historyResult, err := Compress(historyTestClient(t, provider), HistoryCompressionConfig{CompressAtTurns: 3, KeepMaxTurns: 2, KeepMaxInputTokens: 20, CompressAtMaxInputTokens: ceiling})(t.Context(), &model.Request{Messages: messages}, counter, nil)
		out := historyResult.Messages
		if ceiling == 99 {
			require.ErrorContains(t, err, "newest history turn cannot fit")
			assertExactHistory(t, messages, out)
			assert.Equal(t, 1, counts)
			assert.Zero(t, provider.completeCalls)
		} else {
			require.NoError(t, err)
			assertExactHistory(t, messages[4:], out[len(out)-3:])
			assert.Same(t, instruction, out[2])
			assert.Equal(t, 3, counts)
		}
	}
}

func historySystemMessage(text string) *model.Message {
	return &model.Message{Role: model.ConversationRoleSystem, Parts: []model.Part{model.TextPart{Text: text}}}
}
