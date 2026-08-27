package bedrock

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/stretchr/testify/require"

	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
)

type nonIdempotentBedrockReader struct {
	mu         sync.Mutex
	events     <-chan brtypes.ConverseStreamOutput
	closeCalls int
}

func (r *nonIdempotentBedrockReader) Events() <-chan brtypes.ConverseStreamOutput {
	return r.events
}

func (r *nonIdempotentBedrockReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeCalls++
	if r.closeCalls > 1 {
		return errors.New("bedrock reader closed more than once")
	}
	return nil
}

func (r *nonIdempotentBedrockReader) Err() error {
	return nil
}

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
		"",
	)

	err := cp.Handle(&brtypes.ConverseStreamOutputMemberMessageStart{})
	require.NoError(t, err)
	err = cp.Handle(&brtypes.ConverseStreamOutputMemberMessageStop{
		Value: brtypes.MessageStopEvent{StopReason: brtypes.StopReasonMaxTokens},
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
	stopChunk, ok := chunks[1].(model.StopChunk)
	require.True(t, ok)
	require.Equal(t, string(brtypes.StopReasonMaxTokens), stopChunk.Reason)
	require.True(t, stopChunk.OutputLimited)
	response := cp.response()
	require.Equal(t, usageChunk.Usage, response.Usage)
	require.True(t, response.OutputLimited)
}

func TestBedrockStreamRejectsMissingToolCallIDWithUsage(t *testing.T) {
	contract, err := model.NewRequestContract(&model.Request{ModelClass: model.ModelClassDefault})
	require.NoError(t, err)
	processor := newChunkProcessor(
		func(model.Chunk) error { return nil },
		map[string]string{"lookup": "svc.lookup"},
		"test-model-id",
		model.ModelClassDefault,
		nil,
		"",
	)
	err = processor.Handle(&brtypes.ConverseStreamOutputMemberMessageStart{})
	require.NoError(t, err)
	index := int32(0)
	err = processor.Handle(&brtypes.ConverseStreamOutputMemberContentBlockStart{
		Value: brtypes.ContentBlockStartEvent{
			ContentBlockIndex: &index,
			Start: &brtypes.ContentBlockStartMemberToolUse{Value: brtypes.ToolUseBlockStart{
				Name: strPtr("lookup"),
			}},
		},
	})
	require.Error(t, err)
	usage := model.TokenUsage{
		Model:        "test-model-id",
		ModelClass:   model.ModelClassDefault,
		InputTokens:  7,
		OutputTokens: 2,
		TotalTokens:  9,
	}

	classified := (&bedrockStreamer{contract: contract}).outputError(usage, err)

	var validationErr *model.OutputValidationError
	require.ErrorAs(t, classified, &validationErr)
	require.ErrorContains(t, classified, "tool use block missing tool_use_id")
	require.Equal(t, &usage, validationErr.Usage())
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
		// Opus 4.8-class models with thinking display "omitted" (the default)
		// stream thinking blocks whose text is empty but which still carry the
		// replay signature; they must decode to a signed empty-text part.
		{name: "signature only", signature: "sig"},
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
		map[string]string{},
		"test-model-id",
		model.ModelClassDefault,
		&model.StructuredOutput{
			Name:   "draft_from_transcript",
			Schema: []byte(`{"type":"object"}`),
		},
		"",
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
		"",
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
		"",
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

func TestChunkProcessorAssignsDenseReasoningIndexes(t *testing.T) {
	var finalIndexes []int
	var finalSignatures []string
	processor := newChunkProcessor(
		func(chunk model.Chunk) error {
			thinking, ok := chunk.(model.ThinkingChunk)
			if !ok {
				return nil
			}
			part := thinking.Message.Parts[0].(model.ThinkingPart)
			if part.Final {
				finalIndexes = append(finalIndexes, part.Index)
				finalSignatures = append(finalSignatures, part.Signature)
			}
			return nil
		},
		nil,
		"",
		"",
		nil,
		"",
	)
	require.NoError(t, processor.Handle(&brtypes.ConverseStreamOutputMemberMessageStart{}))
	for sequence, contentIndex := range []int32{3, 8} {
		if sequence > 0 {
			require.NoError(t, processor.Handle(&brtypes.ConverseStreamOutputMemberContentBlockDelta{
				Value: brtypes.ContentBlockDeltaEvent{
					ContentBlockIndex: &contentIndex,
					Delta: &brtypes.ContentBlockDeltaMemberReasoningContent{
						Value: &brtypes.ReasoningContentBlockDeltaMemberText{Value: "reasoning"},
					},
				},
			}))
		}
		require.NoError(t, processor.Handle(&brtypes.ConverseStreamOutputMemberContentBlockDelta{
			Value: brtypes.ContentBlockDeltaEvent{
				ContentBlockIndex: &contentIndex,
				Delta: &brtypes.ContentBlockDeltaMemberReasoningContent{
					Value: &brtypes.ReasoningContentBlockDeltaMemberSignature{
						Value: []string{"sig-1", "sig-2"}[sequence],
					},
				},
			},
		}))
		require.NoError(t, processor.Handle(&brtypes.ConverseStreamOutputMemberContentBlockStop{
			Value: brtypes.ContentBlockStopEvent{ContentBlockIndex: &contentIndex},
		}))
	}

	require.Equal(t, []int{0, 1}, finalIndexes)
	require.Equal(t, []string{"sig-1", "sig-2"}, finalSignatures)
}

func TestBedrockStreamerClosesProviderStreamOnce(t *testing.T) {
	events := make(chan brtypes.ConverseStreamOutput)
	close(events)
	reader := &nonIdempotentBedrockReader{events: events}
	providerStream := bedrockruntime.NewConverseStreamEventStream(func(stream *bedrockruntime.ConverseStreamEventStream) {
		stream.Reader = reader
	})
	contract, err := model.NewRequestContract(&model.Request{})
	require.NoError(t, err)
	streamer := newBedrockStreamer(
		context.Background(),
		providerStream,
		nil,
		"model",
		model.ModelClassDefault,
		nil,
		"",
		nil,
		nil,
		contract,
	)

	_, recvErr := streamer.Recv()
	require.Error(t, recvErr)
	require.NoError(t, streamer.Close())
	require.NoError(t, streamer.Close())
	reader.mu.Lock()
	closeCalls := reader.closeCalls
	reader.mu.Unlock()
	require.Equal(t, 1, closeCalls)
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
		"",
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

// TestChunkProcessorDerivesEmptyInputToolPayload verifies that Bedrock argument
// text cannot invalidate a tool whose selection is the complete model decision.
func TestChunkProcessorDerivesEmptyInputToolPayload(t *testing.T) {
	idx := int32(0)
	providerName := "ada_continue_alarms"
	canonicalName := "ada.continue_alarms"
	id := "tooluse_1"
	malformed := `{"cursor":`
	var chunks []model.Chunk
	cp := newChunkProcessor(
		func(chunk model.Chunk) error {
			chunks = append(chunks, chunk)
			return nil
		},
		map[string]string{providerName: canonicalName},
		"test-model-id",
		model.ModelClassDefault,
		nil,
		"",
	)
	cp.noArgumentTools = map[string]struct{}{canonicalName: {}}

	require.NoError(t, cp.Handle(&brtypes.ConverseStreamOutputMemberMessageStart{}))
	require.NoError(t, cp.Handle(&brtypes.ConverseStreamOutputMemberContentBlockStart{
		Value: brtypes.ContentBlockStartEvent{
			ContentBlockIndex: &idx,
			Start: &brtypes.ContentBlockStartMemberToolUse{
				Value: brtypes.ToolUseBlockStart{
					Name:      &providerName,
					ToolUseId: &id,
				},
			},
		},
	}))
	require.NoError(t, cp.Handle(&brtypes.ConverseStreamOutputMemberContentBlockDelta{
		Value: brtypes.ContentBlockDeltaEvent{
			ContentBlockIndex: &idx,
			Delta: &brtypes.ContentBlockDeltaMemberToolUse{
				Value: brtypes.ToolUseBlockDelta{Input: &malformed},
			},
		},
	}))
	require.NoError(t, cp.Handle(&brtypes.ConverseStreamOutputMemberContentBlockStop{
		Value: brtypes.ContentBlockStopEvent{ContentBlockIndex: &idx},
	}))

	require.Len(t, chunks, 1)
	call, ok := chunks[0].(model.ToolCallChunk)
	require.True(t, ok)
	require.Equal(t, tools.Ident(canonicalName), call.ToolCall.Name)
	require.JSONEq(t, `{}`, string(call.ToolCall.Payload))
}

// TestChunkProcessorDerivesOmittedOptionalPayload verifies that Bedrock's
// missing argument delta becomes an empty object only when the advertised tool
// contract accepts that object.
func TestChunkProcessorDerivesOmittedOptionalPayload(t *testing.T) {
	idx := int32(0)
	providerName := "atlas_discover_list_apps"
	canonicalName := "atlas.discover.list_apps"
	id := "tooluse_2"
	var chunks []model.Chunk
	cp := newChunkProcessor(
		func(chunk model.Chunk) error {
			chunks = append(chunks, chunk)
			return nil
		},
		map[string]string{providerName: canonicalName},
		"test-model-id",
		model.ModelClassDefault,
		nil,
		"",
	)
	cp.emptyObjectTools = map[string]struct{}{canonicalName: {}}

	require.NoError(t, cp.Handle(&brtypes.ConverseStreamOutputMemberMessageStart{}))
	require.NoError(t, cp.Handle(&brtypes.ConverseStreamOutputMemberContentBlockStart{
		Value: brtypes.ContentBlockStartEvent{
			ContentBlockIndex: &idx,
			Start: &brtypes.ContentBlockStartMemberToolUse{
				Value: brtypes.ToolUseBlockStart{
					Name:      &providerName,
					ToolUseId: &id,
				},
			},
		},
	}))
	require.NoError(t, cp.Handle(&brtypes.ConverseStreamOutputMemberContentBlockStop{
		Value: brtypes.ContentBlockStopEvent{ContentBlockIndex: &idx},
	}))

	require.Len(t, chunks, 1)
	call, ok := chunks[0].(model.ToolCallChunk)
	require.True(t, ok)
	require.Equal(t, tools.Ident(canonicalName), call.ToolCall.Name)
	require.JSONEq(t, `{}`, string(call.ToolCall.Payload))
}

func TestChunkProcessorRejectsMessageStopWithOpenContentBlock(t *testing.T) {
	idx := int32(0)
	cp := newChunkProcessor(
		func(model.Chunk) error { return nil },
		map[string]string{},
		"test-model-id",
		model.ModelClassDefault,
		nil,
		"",
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
		"",
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
		"",
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

// TestChunkProcessorChargesSyntheticCompletionBeforeAppend verifies Bedrock's
// private completion buffer cannot grow beyond the validated stream budget.
func TestChunkProcessorChargesSyntheticCompletionBeforeAppend(t *testing.T) {
	cp := newChunkProcessor(
		func(model.Chunk) error { return nil },
		nil,
		"model",
		model.ModelClassDefault,
		&model.StructuredOutput{Name: "answer"},
		"",
	)
	cp.retainedBytes = 16 << 20

	err := cp.handleCompletionDelta(0, "x")

	require.ErrorContains(t, err, "retained output exceeds 16777216 bytes")
	require.Zero(t, cp.completion.fragments.Len())
}
