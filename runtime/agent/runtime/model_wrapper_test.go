package runtime

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/features/model/gateway"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/internal/outputcontract"
	"goa.design/goa-ai/runtime/agent/internal/provenance"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	recordingPlannerEvents struct {
		usage []model.TokenUsage
	}

	chunkStreamer struct {
		chunks      []model.Chunk
		response    *model.Response
		terminalErr error
		index       int
		closed      bool
		closeErr    error
	}

	nilStreamClient struct{}

	streamResultClient struct {
		stream model.Streamer
		err    error
	}
)

func (nilStreamClient) Complete(context.Context, *model.Request) (*model.Response, error) {
	return nil, errors.New("unexpected Complete call")
}

func (nilStreamClient) Stream(context.Context, *model.Request) (model.Streamer, error) {
	return nil, nil
}

func (streamResultClient) Complete(context.Context, *model.Request) (*model.Response, error) {
	return nil, errors.New("unexpected Complete call")
}

func (c streamResultClient) Stream(context.Context, *model.Request) (model.Streamer, error) {
	return c.stream, c.err
}

func newTestModelInvocationClient(provider model.Provider, sink modelInvocationSink) model.Client {
	return newModelInvocationClient(mustTestModelClient(provider), sink)
}

func newTestDesignatedModelInvocationClient(provider model.Provider, sink modelInvocationSink) model.Client {
	return newDesignatedModelInvocationClient(mustTestModelClient(provider), sink)
}

func (e *recordingPlannerEvents) PlannerThought(context.Context, string, map[string]string) {}

func (e *recordingPlannerEvents) UsageDelta(_ context.Context, usage model.TokenUsage) {
	e.usage = append(e.usage, usage)
}

func (s *chunkStreamer) Recv() (model.Chunk, error) {
	if s.index >= len(s.chunks) {
		if s.terminalErr != nil {
			return nil, s.terminalErr
		}
		return nil, io.EOF
	}
	chunk := s.chunks[s.index]
	s.index++
	return chunk, nil
}

func (s *chunkStreamer) Close() error {
	s.closed = true
	return s.closeErr
}

func (s *chunkStreamer) Response() *model.Response {
	return s.response
}

func TestModelInvocationClientRejectsNilStream(t *testing.T) {
	invocations := &modelInvocationJournal{}
	client := newTestModelInvocationClient(nilStreamClient{}, invocations)

	stream, err := client.Stream(t.Context(), &model.Request{})

	require.Nil(t, stream)
	var outputErr *planner.OutputContractError
	require.ErrorAs(t, err, &outputErr)
	require.ErrorContains(t, err, "model stream is nil")
}

func TestRuntimeModelWrappersCloseStreamsReturnedWithErrors(t *testing.T) {
	callErr := errors.New("stream call failed")
	closeErr := errors.New("stream close failed")

	tests := []struct {
		name string
		wrap func(model.Client) model.Client
	}{
		{
			name: "cache policy",
			wrap: func(inner model.Client) model.Client {
				return newCacheConfiguredClient(inner, CachePolicy{AfterSystem: true})
			},
		},
		{
			name: "invocation journal",
			wrap: func(inner model.Client) model.Client {
				return newModelInvocationClient(inner, &modelInvocationJournal{})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := &chunkStreamer{closeErr: closeErr}
			client := test.wrap(mustTestModelClient(streamResultClient{stream: raw, err: callErr}))

			got, err := client.Stream(t.Context(), &model.Request{})

			require.Nil(t, got)
			require.ErrorIs(t, err, callErr)
			require.ErrorIs(t, err, closeErr)
			require.True(t, raw.closed)
		})
	}
}

func TestPlannerModelClientClosesStreamReturnedWithError(t *testing.T) {
	callErr := errors.New("stream call failed")
	closeErr := errors.New("stream close failed")
	raw := &chunkStreamer{closeErr: closeErr}
	client := newPlannerModelClient(
		mustTestModelClient(streamResultClient{stream: raw, err: callErr}),
	)

	summary, err := client.Stream(t.Context(), &model.Request{})

	require.Equal(t, planner.StreamSummary{}, summary)
	require.ErrorIs(t, err, callErr)
	require.ErrorIs(t, err, closeErr)
	require.True(t, raw.closed)
}

func TestModelInvocationStreamFingerprintsRejectedCompleteResponse(t *testing.T) {
	invocations := &modelInvocationJournal{}
	client := newTestModelInvocationClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			return &chunkStreamer{
				chunks: []model.Chunk{model.StopChunk{Reason: "end_turn"}},
				response: &model.Response{
					Content: []model.Message{{
						Role:  model.ConversationRoleAssistant,
						Parts: []model.Part{nil},
					}},
					StopReason: "end_turn",
				},
			}, nil
		},
	}, invocations)
	stream, err := client.Stream(t.Context(), &model.Request{})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()
	require.Error(t, err)
	require.ErrorContains(t, err, "invalid canonical response")

	evidence, present := rejectedResponseEvidence(invocations)
	require.True(t, present)
	require.Len(t, evidence.SHA256, 64)
	require.Positive(t, evidence.Size)
}

func TestModelInvocationClientRejectsInvalidRequestBeforeProviderCall(t *testing.T) {
	providerCalls := 0
	client := newTestModelInvocationClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			providerCalls++
			return &model.Response{}, nil
		},
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			providerCalls++
			return &chunkStreamer{}, nil
		},
	}, &modelInvocationJournal{})
	tool := &model.ToolDefinition{
		Name:  "duplicate",
		Input: mustRuntimeToolInput(rawjson.Message(`{"type":"object"}`)),
	}
	request := &model.Request{Tools: []*model.ToolDefinition{tool, tool}}

	response, err := client.Complete(t.Context(), request)
	require.Nil(t, response)
	require.ErrorContains(t, err, `duplicate tool definition "duplicate"`)
	stream, err := client.Stream(t.Context(), request)
	require.Nil(t, stream)
	require.ErrorContains(t, err, `duplicate tool definition "duplicate"`)
	require.Zero(t, providerCalls)
}

func TestModelInvocationClientRejectsNilRequestBeforeProviderCall(t *testing.T) {
	providerCalls := 0
	client := newTestModelInvocationClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			providerCalls++
			return &model.Response{}, nil
		},
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			providerCalls++
			return &chunkStreamer{}, nil
		},
	}, &modelInvocationJournal{})

	response, err := client.Complete(t.Context(), nil)
	require.Nil(t, response)
	require.ErrorContains(t, err, "model request is required")
	stream, err := client.Stream(t.Context(), nil)
	require.Nil(t, stream)
	require.ErrorContains(t, err, "model request is required")
	require.Zero(t, providerCalls)
}

func TestModelInvocationClientKeepsPreProviderRequestContract(t *testing.T) {
	request := testModelRequest()
	client := newTestModelInvocationClient(stubModelClient{
		complete: func(_ context.Context, request *model.Request) (*model.Response, error) {
			request.Tools = testModelRequest("late_tool").Tools
			return testModelResponse(nil, model.ToolCall{
				ID:      "call-1",
				Name:    "late_tool",
				Payload: rawjson.Message(`{}`),
			}), nil
		},
	}, &modelInvocationJournal{})

	response, err := client.Complete(t.Context(), request)
	require.Nil(t, response)
	require.ErrorContains(t, err, `model returned tool "late_tool" that was not present in its request`)
}

func TestModelInvocationJournalSelectsEarliestStartedRejection(t *testing.T) {
	invocations := &modelInvocationJournal{}
	firstID, err := invocations.beginModelInvocation("", func() {})
	require.NoError(t, err)
	secondID, err := invocations.beginModelInvocation("", func() {})
	require.NoError(t, err)

	firstFingerprint := model.ResponseEvidence{Present: true, SHA256: "first", Size: 5}
	secondFingerprint := model.ResponseEvidence{Present: true, SHA256: "second", Size: 6}

	require.NoError(t, invocations.recordRejectedResponseEvidence(secondID, secondFingerprint))
	secondErr := outputcontract.NewWithOrigin(
		errors.New("second rejection completed first"),
		planner.OutputContractOriginModel,
	)
	require.Equal(t, secondErr, invocations.rejectModelInvocation(secondID, secondErr))
	invocations.invocations[secondID].recoveryCorrection = "replace second invocation"

	require.NoError(t, invocations.recordRejectedResponseEvidence(firstID, firstFingerprint))
	firstErr := outputcontract.NewWithOrigin(
		errors.New("first rejection completed second"),
		planner.OutputContractOriginModel,
	)
	require.Equal(t, firstErr, invocations.rejectModelInvocation(firstID, firstErr))
	invocations.invocations[firstID].recoveryCorrection = "replace first invocation"

	require.Equal(t, firstErr, invocations.outputContractError())
	recovery := invocations.recoverableModelInvocationRecovery()
	require.NotNil(t, recovery)
	require.Equal(t, "replace first invocation", recovery.Correction)
	evidence, present := rejectedResponseEvidence(invocations)
	require.True(t, present)
	require.Equal(t, firstFingerprint, evidence)
}

func TestModelInvocationClientKeepsValidatedResponseBookkeepingFailurePlannerOwned(t *testing.T) {
	bookkeepingErr := planner.NewOutputContractError(errors.New("save validated response"))
	sink := &fakeModelInvocationSink{recordValidatedErr: bookkeepingErr}
	client := newTestModelInvocationClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return testModelResponse([]model.Message{{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "accepted"}},
			}}), nil
		},
	}, sink)

	response, err := client.Complete(t.Context(), &model.Request{})

	require.Nil(t, response)
	require.ErrorIs(t, err, bookkeepingErr)
	require.Zero(t, sink.rejectedResponses)
}

func TestModelInvocationJournalBlocksCallsAfterSeal(t *testing.T) {
	invocations := &modelInvocationJournal{}
	require.NoError(t, invocations.seal())

	_, err := invocations.beginModelInvocation("", func() {})

	require.EqualError(t, err, "planner model invocation journal is sealed")
}

func TestModelInvocationStreamLatchesTerminalError(t *testing.T) {
	upstream := &chunkStreamer{chunks: []model.Chunk{
		model.StopChunk{Reason: "stop"},
		model.TextChunk{Message: model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "late"}},
		}},
	}}
	client := newTestModelInvocationClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			return upstream, nil
		},
	}, &modelInvocationJournal{})
	stream, err := client.Stream(t.Context(), &model.Request{})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.NoError(t, err)

	_, firstErr := stream.Recv()
	_, secondErr := stream.Recv()

	require.ErrorContains(t, firstErr, "after stop")
	require.Equal(t, firstErr, secondErr)
	require.Equal(t, 2, upstream.index)
}

func TestModelInvocationStreamCloseLatchesPlannerContractFailure(t *testing.T) {
	providerCalls := 0
	invocations := &modelInvocationJournal{}
	client := newTestModelInvocationClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			providerCalls++
			return &chunkStreamer{}, nil
		},
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			providerCalls++
			return testModelResponse([]model.Message{{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "late"}},
			}}), nil
		},
	}, invocations)
	stream, err := client.Stream(t.Context(), &model.Request{})
	require.NoError(t, err)

	closeErr := stream.Close()
	require.ErrorContains(t, closeErr, "planner closed model stream before EOF")
	var outputErr *planner.OutputContractError
	require.ErrorAs(t, closeErr, &outputErr)
	require.Equal(t, planner.OutputContractOriginPlanner, outputErr.Origin())

	response, err := client.Complete(t.Context(), &model.Request{})
	require.Nil(t, response)
	require.ErrorIs(t, err, outputErr)
	require.Equal(t, 1, providerCalls)
}

func TestRejectedTerminalStreamUsageReplacesPriorDeltas(t *testing.T) {
	usage := model.TokenUsage{InputTokens: 40, OutputTokens: 60, TotalTokens: 100}
	response := &model.Response{
		Content: []model.Message{{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "different terminal text"}},
		}},
		Usage:      usage,
		StopReason: "end_turn",
	}
	invocations := &modelInvocationJournal{}
	client := newTestModelInvocationClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			return &chunkStreamer{
				chunks: []model.Chunk{
					model.TextChunk{Message: model.Message{
						Role:  model.ConversationRoleAssistant,
						Parts: []model.Part{model.TextPart{Text: "streamed text"}},
					}},
					model.UsageChunk{Usage: usage},
					model.StopChunk{Reason: "end_turn"},
				},
				response: response,
			}, nil
		},
	}, invocations)

	stream, err := client.Stream(t.Context(), &model.Request{})
	require.NoError(t, err)
	_, err = planner.ConsumeStream(t.Context(), stream)
	require.Error(t, err)
	require.Equal(t, usage, invocations.exportUsage())
}

func TestModelPresentationPublishesUsageForEveryInvocation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	events := &recordingPlannerEvents{}
	invocations := &modelInvocationJournal{}
	probeRequest := &model.Request{Model: "gpt-5-mini", ModelClass: model.ModelClassSmall}
	failed := newTestModelInvocationClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			return &chunkStreamer{
				chunks: []model.Chunk{
					model.TextChunk{Message: model.Message{
						Role:  model.ConversationRoleAssistant,
						Parts: []model.Part{model.TextPart{Text: "discarded partial"}},
					}},
					model.UsageChunk{Usage: model.TokenUsage{
						Model:       "provider-mini",
						InputTokens: 1, OutputTokens: 2, TotalTokens: 3,
					}},
				},
				terminalErr: errors.New("retryable provider failure"),
			}, nil
		},
	}, invocations)
	failedStream, err := failed.Stream(ctx, probeRequest)
	require.NoError(t, err)
	_, err = planner.ConsumeStream(ctx, failedStream)
	require.EqualError(t, err, "retryable provider failure")
	require.Empty(t, events.usage)

	response := &model.Response{
		Content: []model.Message{{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "selected answer"}},
		}},
		StopReason: "end_turn",
		Usage: model.TokenUsage{
			Model:       "provider-main",
			InputTokens: 4, OutputTokens: 5, TotalTokens: 9,
		},
	}
	selected := newTestDesignatedModelInvocationClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			return &chunkStreamer{
				chunks: []model.Chunk{
					model.TextChunk{Message: response.Content[0]},
					model.UsageChunk{Usage: response.Usage},
					model.StopChunk{Reason: response.StopReason},
				},
				response: response,
			}, nil
		},
	}, invocations)
	selectedRequest := &model.Request{Model: "gpt-5", ModelClass: model.ModelClassDefault}
	selectedStream, err := selected.Stream(ctx, selectedRequest)
	require.NoError(t, err)
	summary, err := planner.ConsumeStream(ctx, selectedStream)
	require.NoError(t, err)
	require.Empty(t, events.usage)

	result := &planner.PlanResult{FinalResponse: summary.FinalResponse()}
	_, err = invocations.exportModelInvocation(result)
	require.NoError(t, err)
	invocations.publishUsage(ctx, events)

	require.Equal(t, []model.TokenUsage{
		{
			Model:        "provider-mini",
			ModelClass:   model.ModelClassSmall,
			InputTokens:  1,
			OutputTokens: 2,
			TotalTokens:  3,
		},
		{
			Model:        "provider-main",
			ModelClass:   model.ModelClassDefault,
			InputTokens:  4,
			OutputTokens: 5,
			TotalTokens:  9,
		},
	}, events.usage)
}

func TestSimplePlannerContextModelClientDoesNotEmitPlannerEvents(t *testing.T) {
	events := &recordingPlannerEvents{}
	rt := &Runtime{
		models: map[string]model.Client{
			"primary": mustTestModelClient(stubModelClient{
				complete: func(context.Context, *model.Request) (*model.Response, error) {
					return &model.Response{
						Usage: model.TokenUsage{InputTokens: 3, OutputTokens: 5, TotalTokens: 8},
						Content: []model.Message{
							{
								Role:  model.ConversationRoleAssistant,
								Parts: []model.Part{model.TextPart{Text: "hello"}},
							},
						},
						StopReason: "stop",
					}, nil
				},
			}),
		},
		logger: telemetry.NewNoopLogger(),
		tracer: telemetry.NoopTracer{},
	}
	ctx := &simplePlannerContext{
		rt:        rt,
		agent:     "svc.agent",
		runID:     "run-1",
		sessionID: "sess-1",
		ev:        events,
	}

	client, ok := ctx.ModelClient("primary")
	require.True(t, ok)

	resp, err := client.Complete(context.Background(), &model.Request{Model: "gpt-5"})

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Empty(t, events.usage)
}

func TestSimplePlannerContextPlannerModelClientOwnsEventEmission(t *testing.T) {
	events := &recordingPlannerEvents{}
	streamer := &chunkStreamer{
		chunks: []model.Chunk{
			model.TextChunk{
				Message: model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "hello"}},
				},
			},
			model.ToolCallChunk{
				ToolCall: model.ToolCall{ID: "call-1", Name: "svc.lookup", Payload: []byte(`{"q":"x"}`)},
			},
			model.UsageChunk{
				Usage: model.TokenUsage{
					Model:        "provider-gpt-5",
					InputTokens:  2,
					OutputTokens: 4,
					TotalTokens:  6,
				},
			},
			model.StopChunk{
				Reason: "tool_use",
			},
		},
		response: testModelResponseWithUsage(
			[]model.Message{{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "hello"}}}},
			model.TokenUsage{
				Model:        "provider-gpt-5",
				InputTokens:  2,
				OutputTokens: 4,
				TotalTokens:  6,
			},
			model.ToolCall{ID: "call-1", Name: "svc.lookup", Payload: []byte(`{"q":"x"}`)},
		),
	}
	rt := &Runtime{
		models: map[string]model.Client{
			"primary": mustTestModelClient(stubModelClient{
				stream: func(context.Context, *model.Request) (model.Streamer, error) {
					return streamer, nil
				},
			}),
		},
		logger: telemetry.NewNoopLogger(),
		tracer: telemetry.NoopTracer{},
	}
	ctx := &simplePlannerContext{
		rt:        rt,
		agent:     "svc.agent",
		runID:     "run-1",
		sessionID: "sess-1",
		ev:        events,
	}

	client, ok := ctx.PlannerModelClient("primary")
	require.True(t, ok)
	_, isRawModelClient := any(client).(model.Client)
	require.False(t, isRawModelClient)

	request := testModelRequest("svc.lookup")
	request.Model = "gpt-5"
	request.ModelClass = model.ModelClassDefault
	summary, err := client.Stream(context.Background(), request)

	require.NoError(t, err)
	require.Equal(t, "hello", summary.Text)
	require.Len(t, summary.ToolCalls, 1)
	require.Equal(t, tools.Ident("svc.lookup"), summary.ToolCalls[0].Name)
	require.Equal(t, "tool_use", summary.StopReason)
	require.Equal(t, "provider-gpt-5", summary.Usage.Model)
	require.Equal(t, model.ModelClassDefault, summary.Usage.ModelClass)
	require.Equal(t, 2, summary.Usage.InputTokens)
	require.Equal(t, 4, summary.Usage.OutputTokens)
	require.Equal(t, 6, summary.Usage.TotalTokens)
	require.True(t, streamer.closed)
	require.Empty(t, events.usage)
}

func TestPlannerModelClientIsSingleUseAndSelectsCanonicalResponse(t *testing.T) {
	invocations := &modelInvocationJournal{}
	client := newPlannerModelClient(newTestDesignatedModelInvocationClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return testModelResponse([]model.Message{{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "selected"}},
			}}), nil
		},
	}, invocations))

	response, err := client.Complete(context.Background(), &model.Request{})
	require.NoError(t, err)
	_, err = client.Complete(context.Background(), &model.Request{})
	require.EqualError(t, err, "runtime: PlannerModelClient permits exactly one model invocation per planner turn")

	transcript, err := invocations.exportModelInvocation(&planner.PlanResult{
		FinalResponse: &planner.FinalResponse{Message: &response.Content[0]},
	})
	require.NoError(t, err)
	require.Equal(t, "selected", transcript[0].Parts[0].(model.TextPart).Text)
}

func TestModelInvocationExportPreservesAdditivePlannerMetadata(t *testing.T) {
	invocations := &modelInvocationJournal{}
	originalParts := []model.Part{
		model.ThinkingPart{
			Text:      "provider reasoning",
			Signature: "opaque-provider-signature",
			Index:     0,
			Final:     true,
		},
		model.TextPart{Text: "provider answer"},
	}
	client := newTestModelInvocationClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return testModelResponse([]model.Message{{
				Role:  model.ConversationRoleAssistant,
				Parts: originalParts,
				Meta: map[string]any{
					"provider": map[string]any{"request_id": "req-1"},
				},
			}}), nil
		},
	}, invocations)

	response, err := client.Complete(t.Context(), &model.Request{})
	require.NoError(t, err)
	response.Content[0].Meta["aura.assistant_citations.v1"] =
		`[{"index":1,"file_id":"document-1"}]`
	result := &planner.PlanResult{
		FinalResponse: &planner.FinalResponse{Message: &response.Content[0]},
	}
	transcript, err := invocations.exportModelInvocation(result)

	require.NoError(t, err)
	require.Len(t, transcript, 1)
	assert.Equal(t, originalParts, transcript[0].Parts)
	assert.Equal(t, originalParts, result.FinalResponse.Message.Parts)
	assert.Equal(t, map[string]any{
		"provider":                    map[string]any{"request_id": "req-1"},
		"aura.assistant_citations.v1": `[{"index":1,"file_id":"document-1"}]`,
	}, transcript[0].Meta)
}

func TestModelInvocationExportRejectsProviderContentChanges(t *testing.T) {
	t.Parallel()

	invocations := &modelInvocationJournal{}
	client := newTestModelInvocationClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return testModelResponse([]model.Message{{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "provider answer"}},
			}}), nil
		},
	}, invocations)
	response, err := client.Complete(t.Context(), &model.Request{})
	require.NoError(t, err)
	response.Content[0].Parts = []model.Part{model.TextPart{Text: "planner rewrite"}}

	_, err = invocations.exportModelInvocation(&planner.PlanResult{
		FinalResponse: &planner.FinalResponse{Message: &response.Content[0]},
	})

	require.EqualError(t, err, "planner result modified provider-owned message content")
}

func TestModelInvocationExportRejectsProviderMetadataChanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "change",
			mutate: func(meta map[string]any) {
				meta["provider"] = map[string]any{"request_id": "changed"}
			},
		},
		{
			name: "delete",
			mutate: func(meta map[string]any) {
				delete(meta, "provider")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invocations := &modelInvocationJournal{}
			client := newTestModelInvocationClient(stubModelClient{
				complete: func(context.Context, *model.Request) (*model.Response, error) {
					return testModelResponse([]model.Message{{
						Role:  model.ConversationRoleAssistant,
						Parts: []model.Part{model.TextPart{Text: "provider answer"}},
						Meta: map[string]any{
							"provider": map[string]any{"request_id": "req-1"},
						},
					}}), nil
				},
			}, invocations)
			response, err := client.Complete(t.Context(), &model.Request{})
			require.NoError(t, err)
			test.mutate(response.Content[0].Meta)

			_, err = invocations.exportModelInvocation(&planner.PlanResult{
				FinalResponse: &planner.FinalResponse{Message: &response.Content[0]},
			})

			assert.EqualError(t, err, "planner result modified provider-owned message metadata")
		})
	}
}

func TestDesignatedModelInvocationWinsIdenticalProbeToolCalls(t *testing.T) {
	invocations := &modelInvocationJournal{}
	call := model.ToolCall{ID: "call-1", Name: "svc.lookup", Payload: rawjson.Message(`{"q":"status"}`)}
	probe := newTestModelInvocationClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return testModelResponse([]model.Message{{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "probe"}},
			}}, call), nil
		},
	}, invocations)
	designated := newTestDesignatedModelInvocationClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return testModelResponse([]model.Message{{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "designated"}},
			}}, call), nil
		},
	}, invocations)

	request := testModelRequest("svc.lookup")
	_, err := probe.Complete(context.Background(), request)
	require.NoError(t, err)
	_, err = designated.Complete(context.Background(), request)
	require.NoError(t, err)

	transcript, err := invocations.exportModelInvocation(&planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{
			Name:            call.Name,
			Payload:         call.Payload,
			ModelToolCallID: call.ID,
		}},
	})
	require.NoError(t, err)
	require.Equal(t, "designated", transcript[0].Parts[0].(model.TextPart).Text)
}

// fakeModelInvocationSink records complete responses and stream chunks by
// runtime-owned invocation.
type fakeModelInvocationSink struct {
	begins             uint64
	last               modelInvocationID
	responses          map[modelInvocationID]*model.Response
	chunks             map[modelInvocationID][]model.Chunk
	finished           map[modelInvocationID]error
	cancels            map[modelInvocationID]context.CancelFunc
	recordValidatedErr error
	rejectedResponses  int
}

func (s *fakeModelInvocationSink) beginModelInvocation(
	_ model.ModelClass,
	cancel context.CancelFunc,
) (modelInvocationID, error) {
	s.begins++
	s.last = provenance.New()
	if s.cancels == nil {
		s.cancels = make(map[modelInvocationID]context.CancelFunc)
	}
	s.cancels[s.last] = cancel
	return s.last, nil
}

func (s *fakeModelInvocationSink) designateModelInvocation(modelInvocationID) error {
	return nil
}

func (s *fakeModelInvocationSink) recordModelResponse(invocationID modelInvocationID, response *model.Response) error {
	if s.responses == nil {
		s.responses = make(map[modelInvocationID]*model.Response)
	}
	s.responses[invocationID] = response
	return nil
}

func (s *fakeModelInvocationSink) recordRejectedModelOutput(
	_ modelInvocationID,
	_ model.ResponseEvidence,
	err error,
) error {
	s.rejectedResponses++
	return err
}

func (*fakeModelInvocationSink) recordRejectedModelUsageTotal(_ modelInvocationID, _ model.TokenUsage) error {
	return nil
}

func (*fakeModelInvocationSink) recordRejectedModelUsageDelta(context.Context, modelInvocationID, model.TokenUsage) error {
	return nil
}

func (s *fakeModelInvocationSink) recordValidatedModelResponse(
	invocationID modelInvocationID,
	response *model.Response,
) error {
	if s.recordValidatedErr != nil {
		return s.recordValidatedErr
	}
	return s.recordModelResponse(invocationID, response)
}

func (s *fakeModelInvocationSink) recordModelChunk(
	_ context.Context,
	invocationID modelInvocationID,
	chunk model.Chunk,
) error {
	if s.chunks == nil {
		s.chunks = make(map[modelInvocationID][]model.Chunk)
	}
	s.chunks[invocationID] = append(s.chunks[invocationID], chunk)
	return nil
}

func (s *fakeModelInvocationSink) finishModelInvocation(
	_ context.Context,
	invocationID modelInvocationID,
	err error,
) error {
	if s.finished == nil {
		s.finished = make(map[modelInvocationID]error)
	}
	s.finished[invocationID] = err
	s.cancels[invocationID]()
	return nil
}

func TestNewModelInvocationClientReturnsInnerWhenSinkNil(t *testing.T) {
	inner := mustTestModelClient(stubModelClient{})
	client := newModelInvocationClient(inner, nil)
	require.Equal(t, inner, client)
}

func TestModelInvocationClientMakesRemoteMalformedOutputTerminal(t *testing.T) {
	providerCalls := 0
	remote, err := gateway.NewRemoteClient(
		func(context.Context, *model.Request) (*model.Response, error) {
			providerCalls++
			return &model.Response{
				Content: []model.Message{{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "missing stop reason"}},
				}},
			}, nil
		},
		func(context.Context, *model.Request) (model.Streamer, error) {
			return nil, errors.New("unexpected stream call")
		},
	)
	require.NoError(t, err)
	client := newModelInvocationClient(remote, &fakeModelInvocationSink{})

	_, firstErr := client.Complete(t.Context(), &model.Request{})
	var outputErr *planner.OutputContractError
	require.ErrorAs(t, firstErr, &outputErr)
	require.Equal(t, planner.OutputContractOriginModel, outputErr.Origin())

	_, secondErr := client.Complete(t.Context(), &model.Request{})
	require.ErrorIs(t, secondErr, outputErr)
	require.Equal(t, 1, providerCalls)
}

// TestModelInvocationClientCapturesFromCompleteResponse verifies that a
// complete response is saved before planner code receives it.
func TestModelInvocationClientCapturesFromCompleteResponse(t *testing.T) {
	sink := &fakeModelInvocationSink{}
	client := newTestModelInvocationClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return testModelResponse(nil,
				model.ToolCall{ID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`), ThoughtSignature: "sig-1"},
				model.ToolCall{ID: "call-2", Name: "svc.other", Payload: []byte(`{}`)},
			), nil
		},
	}, sink)

	resp, err := client.Complete(context.Background(), testModelRequest("svc.lookup", "svc.other"))

	require.NoError(t, err)
	require.Equal(t, uint64(1), sink.begins)
	require.NotSame(t, resp, sink.responses[sink.last])
	require.Equal(t, resp, sink.responses[sink.last])
}

func TestPlannerModelClientScopesCompleteResponseTranscript(t *testing.T) {
	events := &recordingPlannerEvents{}
	invocations := &modelInvocationJournal{}
	client := newPlannerModelClient(newTestDesignatedModelInvocationClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return testModelResponse([]model.Message{{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{
					model.ThinkingPart{
						Text:      "reasoning",
						Signature: "thinking-signature",
						Index:     0,
						Final:     true,
					},
					model.TextPart{Text: "answer"},
				},
			}},
				model.ToolCall{
					ID:      "call-1",
					Name:    "svc.lookup",
					Payload: []byte(`{"query":"status"}`),
				},
			), nil
		},
	}, invocations))

	_, err := client.Complete(context.Background(), testModelRequest("svc.lookup"))
	require.NoError(t, err)
	transcript, err := invocations.exportModelInvocation(&planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{
			ModelToolCallID: "call-1",
			Name:            "svc.lookup",
			Payload:         []byte(`{"query":"status"}`),
		}},
	})
	require.NoError(t, err)
	invocations.publishUsage(context.Background(), events)
	require.Len(t, transcript, 1)
	require.Equal(t, model.ConversationRoleAssistant, transcript[0].Role)
	require.Equal(t, []model.Part{
		model.ThinkingPart{
			Text:      "reasoning",
			Signature: "thinking-signature",
			Index:     0,
			Final:     true,
		},
		model.TextPart{Text: "answer"},
		model.ToolUsePart{
			ID:    "call-1",
			Name:  "svc.lookup",
			Input: rawjson.Message(`{"query":"status"}`),
		},
	}, transcript[0].Parts)
}

// TestModelInvocationClientCapturesFromStreamedToolCallChunk is the
// capture-side test for the streaming path: a ChunkTypeToolCall chunk
// observed via Recv must be recorded into the sink as it is received.
func TestModelInvocationClientCapturesFromStreamedToolCallChunk(t *testing.T) {
	sink := &fakeModelInvocationSink{}
	streamer := &chunkStreamer{
		chunks: []model.Chunk{
			model.TextChunk{
				Message: model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "working"}},
				},
			},
			model.ToolCallChunk{
				ToolCall: model.ToolCall{ID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`), ThoughtSignature: "sig-1"},
			},
			model.ToolCallChunk{
				ToolCall: model.ToolCall{ID: "call-2", Name: "svc.other", Payload: []byte(`{}`)}, // no signature
			},
			model.StopChunk{Reason: "tool_use"},
		},
		response: testModelResponse([]model.Message{{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "working"}},
		}},
			model.ToolCall{ID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`), ThoughtSignature: "sig-1"},
			model.ToolCall{ID: "call-2", Name: "svc.other", Payload: []byte(`{}`)},
		),
	}
	client := newTestModelInvocationClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			return streamer, nil
		},
	}, sink)

	request := testModelRequest("svc.lookup", "svc.other")
	st, err := client.Stream(context.Background(), request)
	require.NoError(t, err)
	for {
		_, err := st.Recv()
		if err != nil {
			require.ErrorIs(t, err, io.EOF)
			break
		}
	}
	require.NoError(t, st.Close())

	require.Equal(t, uint64(1), sink.begins)
	require.Equal(t, streamer.chunks, sink.chunks[sink.last])
	require.Contains(t, sink.finished, sink.last)
	require.NoError(t, sink.finished[sink.last])
}

func TestModelInvocationClientCapturesCanonicalResponseAtEOF(t *testing.T) {
	invocations := &modelInvocationJournal{}
	response := testModelResponse([]model.Message{{
		Role:  model.ConversationRoleAssistant,
		Parts: []model.Part{model.TextPart{Text: "canonical"}},
		Meta:  map[string]any{"provider_item": "item-1"},
	}},
		model.ToolCall{ID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`)},
	)
	streamer := &chunkStreamer{
		chunks: []model.Chunk{
			model.ToolCallChunk{
				ToolCall: model.ToolCall{ID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`)},
			},
			model.StopChunk{Reason: "tool_use"},
		},
		response: response,
	}
	client := newTestModelInvocationClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			return streamer, nil
		},
	}, invocations)
	request := testModelRequest("svc.lookup")
	stream, err := client.Stream(context.Background(), request)
	require.NoError(t, err)
	summary, err := planner.ConsumeStream(context.Background(), stream)
	require.NoError(t, err)

	transcript, err := invocations.exportModelInvocation(&planner.PlanResult{ToolCalls: summary.ToolCalls})

	require.NoError(t, err)
	require.Equal(t, "canonical", agentMessageText(transcript[0]))
	require.Equal(t, map[string]any{"provider_item": "item-1"}, transcript[0].Meta)
}

func TestModelInvocationClientRejectsStreamWithoutCanonicalResponse(t *testing.T) {
	invocations := &modelInvocationJournal{}
	client := newTestModelInvocationClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			return &chunkStreamer{chunks: []model.Chunk{
				model.TextChunk{
					Message: model.Message{
						Role:  model.ConversationRoleAssistant,
						Parts: []model.Part{model.TextPart{Text: "partial"}},
					},
				},
				model.StopChunk{Reason: "stop"},
			}}, nil
		},
	}, invocations)

	stream, err := client.Stream(context.Background(), &model.Request{})
	require.NoError(t, err)
	_, err = planner.ConsumeStream(context.Background(), stream)

	var outputErr *planner.OutputContractError
	require.ErrorAs(t, err, &outputErr)
	require.ErrorContains(t, err, "invalid canonical response: model: response is nil")
}

func TestModelInvocationClientRejectsMalformedUnaryResponse(t *testing.T) {
	t.Parallel()

	client := newTestModelInvocationClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return nil, nil
		},
	}, &modelInvocationJournal{})

	response, err := client.Complete(context.Background(), &model.Request{})

	require.Nil(t, response)
	var outputErr *planner.OutputContractError
	require.ErrorAs(t, err, &outputErr)
}

func TestModelInvocationClientDropsResponseReturnedWithError(t *testing.T) {
	client := newTestModelInvocationClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return testModelResponse([]model.Message{{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "failed"}},
			}}), assert.AnError
		},
	}, &modelInvocationJournal{})

	response, err := client.Complete(context.Background(), &model.Request{})

	require.Nil(t, response)
	require.ErrorIs(t, err, assert.AnError)
}

func TestModelInvocationClientRejectsMalformedStreamChunk(t *testing.T) {
	invocations := &modelInvocationJournal{}
	client := newTestModelInvocationClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			return &chunkStreamer{chunks: []model.Chunk{model.ToolCallChunk{}}}, nil
		},
	}, invocations)

	stream, err := client.Stream(context.Background(), &model.Request{})
	require.NoError(t, err)
	_, err = stream.Recv()

	require.ErrorContains(t, err, "tool call is missing its ID")
	var outputErr *planner.OutputContractError
	require.ErrorAs(t, err, &outputErr)
}

// TestRawModelInvocationSelectionKeepsResponsesIsolated reproduces two probe
// calls and proves that call order does not choose the durable transcript.
func TestRawModelInvocationSelectionKeepsResponsesIsolated(t *testing.T) {
	events := &recordingPlannerEvents{}
	invocations := &modelInvocationJournal{}
	streamers := []model.Streamer{
		&chunkStreamer{
			chunks: []model.Chunk{
				model.ThinkingChunk{
					Message: model.Message{
						Role: model.ConversationRoleAssistant,
						Parts: []model.Part{model.ThinkingPart{
							Text:  "tentative ",
							Index: 0,
						}},
					},
				},
				model.ThinkingChunk{
					Message: model.Message{
						Role: model.ConversationRoleAssistant,
						Parts: []model.Part{model.ThinkingPart{
							Text:      "tentative reasoning",
							Signature: "tentative-thinking-signature",
							Index:     0,
							Final:     true,
						}},
					},
				},
				model.TextChunk{
					Message: model.Message{
						Role:  model.ConversationRoleAssistant,
						Parts: []model.Part{model.TextPart{Text: "tentative response"}},
					},
				},
				model.ToolCallDeltaChunk{Delta: model.ToolCallDelta{
					ID:    "tentative-call",
					Name:  "svc.lookup",
					Delta: `{}`,
				}},
				model.ToolCallChunk{
					ToolCall: model.ToolCall{
						ID:               "tentative-call",
						Name:             "svc.lookup",
						Payload:          []byte(`{}`),
						ThoughtSignature: "tentative-tool-signature",
					},
				},
				model.UsageChunk{
					Usage: model.TokenUsage{InputTokens: 3, OutputTokens: 5, TotalTokens: 8},
				},
				model.StopChunk{Reason: "tool_use"},
			},
			response: testModelResponseWithUsage([]model.Message{{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{
					model.ThinkingPart{
						Text:      "tentative reasoning",
						Signature: "tentative-thinking-signature",
						Index:     0,
						Final:     true,
					},
					model.TextPart{Text: "tentative response"},
				},
			}}, model.TokenUsage{InputTokens: 3, OutputTokens: 5, TotalTokens: 8},
				model.ToolCall{
					ID:               "tentative-call",
					Name:             "svc.lookup",
					Payload:          []byte(`{}`),
					ThoughtSignature: "tentative-tool-signature",
				},
			),
		},
		&chunkStreamer{
			chunks: []model.Chunk{
				model.ThinkingChunk{
					Message: model.Message{
						Role: model.ConversationRoleAssistant,
						Parts: []model.Part{model.ThinkingPart{
							Text:      "accepted reasoning",
							Signature: "accepted-thinking-signature",
							Index:     0,
							Final:     true,
						}},
					},
				},
				model.TextChunk{
					Message: model.Message{
						Role:  model.ConversationRoleAssistant,
						Parts: []model.Part{model.TextPart{Text: "accepted response"}},
					},
				},
				model.ToolCallDeltaChunk{Delta: model.ToolCallDelta{
					ID:    "accepted-call",
					Name:  "svc.lookup",
					Delta: `{}`,
				}},
				model.ToolCallChunk{
					ToolCall: model.ToolCall{
						ID:               "accepted-call",
						Name:             "svc.lookup",
						Payload:          []byte(`{}`),
						ThoughtSignature: "accepted-tool-signature",
					},
				},
				model.UsageChunk{
					Usage: model.TokenUsage{InputTokens: 7, OutputTokens: 11, TotalTokens: 18},
				},
				model.StopChunk{Reason: "tool_use"},
			},
			response: testModelResponseWithUsage([]model.Message{{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{
					model.ThinkingPart{
						Text:      "accepted reasoning",
						Signature: "accepted-thinking-signature",
						Index:     0,
						Final:     true,
					},
					model.TextPart{Text: "accepted response"},
				},
			}}, model.TokenUsage{InputTokens: 7, OutputTokens: 11, TotalTokens: 18},
				model.ToolCall{
					ID:               "accepted-call",
					Name:             "svc.lookup",
					Payload:          []byte(`{}`),
					ThoughtSignature: "accepted-tool-signature",
				},
			),
		},
	}
	next := 0
	client := newTestModelInvocationClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			streamer := streamers[next]
			next++
			return streamer, nil
		},
	}, invocations)

	request := testModelRequest("svc.lookup")
	stream, err := client.Stream(context.Background(), request)
	require.NoError(t, err)
	tentative, err := planner.ConsumeStream(context.Background(), stream)
	require.NoError(t, err)
	stream, err = client.Stream(context.Background(), request)
	require.NoError(t, err)
	accepted, err := planner.ConsumeStream(context.Background(), stream)
	require.NoError(t, err)
	require.Empty(t, events.usage)

	transcript, err := invocations.exportModelInvocation(&planner.PlanResult{
		ToolCalls: accepted.ToolCalls,
	})
	require.NoError(t, err)
	invocations.publishUsage(context.Background(), events)
	require.Len(t, transcript, 1)
	require.Equal(t, []model.Part{
		model.ThinkingPart{
			Text:      "accepted reasoning",
			Signature: "accepted-thinking-signature",
			Index:     0,
			Final:     true,
		},
		model.TextPart{Text: "accepted response"},
		model.ToolUsePart{
			ID:               "accepted-call",
			Name:             "svc.lookup",
			Input:            rawjson.Message(`{}`),
			ThoughtSignature: "accepted-tool-signature",
		},
	}, transcript[0].Parts)

	tentativeTranscript, err := invocations.exportModelInvocation(&planner.PlanResult{
		ToolCalls: tentative.ToolCalls,
	})
	require.NoError(t, err)
	require.Len(t, tentativeTranscript, 1)
	require.Equal(t, []model.Part{
		model.ThinkingPart{
			Text:      "tentative reasoning",
			Signature: "tentative-thinking-signature",
			Index:     0,
			Final:     true,
		},
		model.TextPart{Text: "tentative response"},
		model.ToolUsePart{
			ID:               "tentative-call",
			Name:             "svc.lookup",
			Input:            rawjson.Message(`{}`),
			ThoughtSignature: "tentative-tool-signature",
		},
	}, tentativeTranscript[0].Parts)
	require.Equal(t, model.TokenUsage{
		InputTokens:  10,
		OutputTokens: 16,
		TotalTokens:  26,
	}, invocations.exportUsage())
}

// TestConfiguredModelClientCapturesToolCallSignatureViaRawModelClient exercises
// the full runtime wiring for the "Option 2" streaming style (AGENTS.md):
// PlannerContext.ModelClient returns the opaque client, and a planner drains it
// directly with planner.ConsumeStream. Capture must still happen even though
// ConsumeStream itself never sees or forwards a signature.
func TestConfiguredModelClientCapturesToolCallSignatureViaRawModelClient(t *testing.T) {
	rt := New()
	events := newPlannerEvents("svc.agent", "run-1", "sess-1")
	invocations := &modelInvocationJournal{}
	streamer := &chunkStreamer{
		chunks: []model.Chunk{
			model.TextChunk{Message: model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "working"}},
			}},
			model.ToolCallChunk{
				ToolCall: model.ToolCall{ID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`), ThoughtSignature: "sig-1"},
			},
			model.StopChunk{Reason: "tool_use"},
		},
		response: testModelResponse([]model.Message{{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "working"}},
		}}, model.ToolCall{
			ID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`), ThoughtSignature: "sig-1",
		}),
	}
	rt.mu.Lock()
	rt.models = map[string]model.Client{
		"primary": mustTestModelClient(stubModelClient{
			stream: func(context.Context, *model.Request) (model.Streamer, error) {
				return streamer, nil
			},
		}),
	}
	rt.mu.Unlock()
	agentCtx := newAgentContext(agentContextOptions{
		runtime:     rt,
		agentID:     "svc.agent",
		runID:       "run-1",
		events:      events,
		invocations: invocations,
	})

	cli, ok := agentCtx.ModelClient("primary")
	require.True(t, ok)
	request := testModelRequest("svc.lookup")
	request.Model = "gemini"
	st, err := cli.Stream(context.Background(), request)
	require.NoError(t, err)
	summary, err := planner.ConsumeStream(context.Background(), st)
	require.NoError(t, err)
	require.Empty(t, events.pending)

	transcript, err := invocations.exportModelInvocation(&planner.PlanResult{
		ToolCalls: summary.ToolCalls,
	})
	require.NoError(t, err)
	require.Equal(t, "sig-1", transcript[0].Parts[1].(model.ToolUsePart).ThoughtSignature)
}

// TestPreparePlannerActivityWiresSignatureCaptureIntoModelClients pins the
// production wiring: preparePlannerActivity constructs an invocation journal
// independently from planner events, and a model client obtained from that
// context captures provider thought signatures without planner participation.
func TestPreparePlannerActivityWiresSignatureCaptureIntoModelClients(t *testing.T) {
	streamer := &chunkStreamer{
		chunks: []model.Chunk{
			model.TextChunk{Message: model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "working"}},
			}},
			model.ToolCallChunk{
				ToolCall: model.ToolCall{ID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`), ThoughtSignature: "sig-1"},
			},
			model.StopChunk{Reason: "tool_use"},
		},
		response: testModelResponse([]model.Message{{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "working"}},
		}}, model.ToolCall{
			ID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`), ThoughtSignature: "sig-1",
		}),
	}
	rt := &Runtime{
		agents: map[agent.Ident]AgentRegistration{
			"svc.agent": {ID: "svc.agent"},
		},
		models: map[string]model.Client{
			"primary": mustTestModelClient(stubModelClient{
				stream: func(context.Context, *model.Request) (model.Streamer, error) {
					return streamer, nil
				},
			}),
		},
		logger:  telemetry.NewNoopLogger(),
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Bus:     noopHooks{},
	}

	act, err := rt.preparePlannerActivity(context.Background(), &PlanActivityInput{
		AgentID:    "svc.agent",
		RunID:      "run-1",
		RunContext: run.Context{SessionID: "sess-1", TurnID: "turn-1"},
	}, nil, nil)
	require.NoError(t, err)

	cli, ok := act.agentCtx.ModelClient("primary")
	require.True(t, ok)
	request := testModelRequest("svc.lookup")
	request.Model = "gemini"
	st, err := cli.Stream(context.Background(), request)
	require.NoError(t, err)
	summary, err := planner.ConsumeStream(context.Background(), st)
	require.NoError(t, err)
	require.Empty(t, act.events.pending)

	transcript, err := act.invocations.exportModelInvocation(&planner.PlanResult{
		ToolCalls: summary.ToolCalls,
	})
	require.NoError(t, err)
	require.Equal(t, "sig-1", transcript[0].Parts[1].(model.ToolUsePart).ThoughtSignature)
}

func TestPlannerAndCacheClientsDropResponsesReturnedWithErrors(t *testing.T) {
	providerErr := errors.New("provider failed")
	inner := mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return &model.Response{StopReason: "should-not-escape"}, providerErr
		},
	})

	plannerClient := newPlannerModelClient(inner)
	response, err := plannerClient.Complete(t.Context(), &model.Request{})
	require.Nil(t, response)
	require.ErrorIs(t, err, providerErr)

	cacheClient := newCacheConfiguredClient(inner, CachePolicy{AfterSystem: true})
	response, err = cacheClient.Complete(t.Context(), &model.Request{})
	require.Nil(t, response)
	require.ErrorIs(t, err, providerErr)
}

// rejectedResponseEvidence returns the earliest rejected provider evidence
// retained by the test journal.
func rejectedResponseEvidence(journal *modelInvocationJournal) (model.ResponseEvidence, bool) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	for _, invocationID := range journal.order {
		candidate := journal.invocations[invocationID]
		if candidate != nil && candidate.rejectedResponseEvidence != nil {
			return *candidate.rejectedResponseEvidence, true
		}
	}
	return model.ResponseEvidence{}, false
}
