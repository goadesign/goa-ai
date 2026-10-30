package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	genmodelgateway "goa.design/goa-ai/features/model/gateway/gen/model_gateway"
	"goa.design/goa-ai/features/model/middleware"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type (
	grpcCaptureProvider struct {
		onComplete func(context.Context, *model.Request) (*model.Response, error)
		onStream   func(context.Context, *model.Request) (model.Streamer, error)
		onCount    func(context.Context, *model.Request) (model.TokenCount, error)
	}
)

func (p *grpcCaptureProvider) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	if p.onComplete != nil {
		return p.onComplete(ctx, req)
	}
	return &model.Response{}, nil
}

func (p *grpcCaptureProvider) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	if p.onStream != nil {
		return p.onStream(ctx, req)
	}
	return &stubStreamer{recvErr: io.EOF}, nil
}

func (p *grpcCaptureProvider) CountTokens(ctx context.Context, req *model.Request) (model.TokenCount, error) {
	if p.onCount != nil {
		return p.onCount(ctx, req)
	}
	return model.TokenCount{}, model.ErrTokenCountingUnsupported
}

func startBufconnGRPCGateway(t *testing.T, srv *Server) *grpc.ClientConn {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	RegisterGRPCServer(grpcServer, srv)
	go func() {
		if err := grpcServer.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) && !errors.Is(err, net.ErrClosed) {
			t.Errorf("grpc server: %v", err)
		}
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		require.NoError(t, listener.Close())
	})

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, conn.Close())
	})
	return conn
}

func testLookupToolDefinition(t *testing.T) *model.ToolDefinition {
	t.Helper()
	input, err := model.ToolInputFromContract("svc.lookup", model.ToolInputContract{
		Schema: rawjson.Message(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
	})
	require.NoError(t, err)
	return &model.ToolDefinition{
		Name:        "svc.lookup",
		Description: "Look up an order",
		Input:       input,
	}
}

func TestGRPCGatewayCompleteRoundTripPreservesRequestPartsAndUsageState(t *testing.T) {
	var captured *model.Request
	provider := &grpcCaptureProvider{
		onComplete: func(_ context.Context, req *model.Request) (*model.Response, error) {
			captured = req
			return &model.Response{
				Content: []model.Message{{
					Role: model.ConversationRoleAssistant,
					Parts: []model.Part{
						model.ThinkingPart{
							Text:      "deliberating",
							Signature: "sig-1",
							Index:     0,
							Final:     true,
						},
						model.TextPart{Text: "final answer"},
						model.CitationsPart{
							Text: "cited text",
							Citations: []model.Citation{{
								Title:         "Doc 1",
								SourceContent: []string{"excerpt"},
							}},
						},
						model.ToolUsePart{
							ID:    "tc-1",
							Name:  "svc.lookup",
							Input: rawjson.Message(`{"id":"42"}`),
						},
					},
					Meta: map[string]any{"turn": "1"},
				}},
				Usage: model.TokenUsage{
					Model:            "claude-sonnet-4-20250514",
					ModelClass:       model.ModelClassHighReasoning,
					InputTokens:      120,
					OutputTokens:     30,
					TotalTokens:      150,
					CacheReadTokens:  10,
					CacheWriteTokens: 20,
				},
				UsageReported: true,
				StopReason:    "tool_use",
			}, nil
		},
	}
	srv, err := NewServer(WithProvider(provider))
	require.NoError(t, err)
	conn := startBufconnGRPCGateway(t, srv)
	client, err := NewGRPCClient(conn)
	require.NoError(t, err)

	req := &model.Request{
		Model:      "claude-sonnet-4-20250514",
		ModelClass: model.ModelClassHighReasoning,
		PromptRefs: []prompt.PromptRef{{
			ID:      prompt.Ident("orders.system"),
			Version: "v2",
		}},
		Messages: []*model.Message{
			{
				Role: model.ConversationRoleSystem,
				Parts: []model.Part{
					model.TextPart{Text: "system prompt"},
					model.CacheCheckpointPart{},
				},
			},
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{
					model.TextPart{Text: "inspect files"},
					model.ImagePart{
						Format: model.ImageFormatPNG,
						Bytes:  []byte{0x89, 0x50, 0x4e, 0x47},
					},
					model.DocumentPart{
						Name:   "spec",
						Format: model.DocumentFormatTXT,
						Text:   "spec text",
					},
				},
			},
			{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{
					model.ToolUsePart{
						ID:    "tc-0",
						Name:  "svc.lookup",
						Input: rawjson.Message(`{"id":"41"}`),
					},
				},
			},
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{
					model.ToolResultPart{
						ToolUseID: "tc-0",
						Content:   map[string]any{"status": "ok"},
					},
				},
			},
		},
		Tools: []*model.ToolDefinition{testLookupToolDefinition(t)},
		ToolChoice: &model.ToolChoice{
			Mode: model.ToolChoiceModeTool,
			Name: "svc.lookup",
		},
		MaxTokens:   2048,
		Temperature: 0.25,
		Thinking: &model.ThinkingOptions{
			Enable:       true,
			Interleaved:  true,
			BudgetTokens: 1024,
		},
		Cache: &model.CacheOptions{
			AfterSystem: true,
			AfterTools:  true,
		},
	}

	resp, err := client.Complete(t.Context(), req)
	require.NoError(t, err)
	require.NotNil(t, captured)
	require.Equal(t, req.PromptRefs, captured.PromptRefs)
	require.Len(t, captured.Messages, 4)
	require.Equal(t, req.ToolChoice, captured.ToolChoice)
	require.Equal(t, req.Thinking, captured.Thinking)
	require.Equal(t, req.Cache, captured.Cache)
	require.True(t, resp.HasUsage())
	require.Equal(t, 150, resp.Usage.TotalTokens)
	require.Equal(t, "tool_use", resp.StopReason)
	require.Len(t, resp.ToolCalls(), 1)
	require.Equal(t, tools.Ident("svc.lookup"), resp.ToolCalls()[0].Name)
}

func TestGRPCGatewayPreservesExplicitZeroIncompleteAndUnreportedTokenUsage(t *testing.T) {
	var reportUsage bool
	var incompleteUsage bool
	var usageTokens model.TokenUsage
	provider := &grpcCaptureProvider{
		onComplete: func(_ context.Context, _ *model.Request) (*model.Response, error) {
			u := usageTokens
			u.Model = "gpt-4o"
			u.ModelClass = model.ModelClassDefault
			return &model.Response{
				Content: []model.Message{{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "ok"}},
				}},
				Usage:           u,
				UsageReported:   reportUsage,
				UsageIncomplete: incompleteUsage,
				StopReason:      "end_turn",
			}, nil
		},
		onCount: func(_ context.Context, req *model.Request) (model.TokenCount, error) {
			return model.TokenCount{
				Model:       req.Model,
				ModelClass:  req.ModelClass,
				InputTokens: 50,
				Exact:       true,
			}, nil
		},
	}
	srv, err := NewServer(WithProvider(provider))
	require.NoError(t, err)
	conn := startBufconnGRPCGateway(t, srv)
	client, err := NewGRPCClient(conn)
	require.NoError(t, err)

	reportUsage = false
	incompleteUsage = false
	usageTokens = model.TokenUsage{}
	unreported, err := client.Complete(t.Context(), &model.Request{Model: "gpt-4o"})
	require.NoError(t, err)
	require.False(t, unreported.HasUsage())
	require.False(t, unreported.HasObservedUsage())

	reportUsage = true
	incompleteUsage = false
	reportedZero, err := client.Complete(t.Context(), &model.Request{Model: "gpt-4o"})
	require.NoError(t, err)
	require.True(t, reportedZero.HasUsage())
	require.True(t, reportedZero.HasObservedUsage())
	require.Zero(t, reportedZero.Usage.TotalTokens)

	reportUsage = false
	incompleteUsage = true
	usageTokens = model.TokenUsage{InputTokens: 120}
	partial, err := client.Complete(t.Context(), &model.Request{Model: "gpt-4o"})
	require.NoError(t, err)
	require.False(t, partial.HasUsage())
	require.True(t, partial.UsageIncomplete)
	require.True(t, partial.HasObservedUsage())
	require.Equal(t, 120, partial.Usage.InputTokens)

	limiter, err := middleware.NewUsageReconciledAdaptiveRateLimiter(
		t.Context(),
		nil,
		"",
		100000,
		100000,
	)
	require.NoError(t, err)
	wrapped, err := limiter.Middleware()(client)
	require.NoError(t, err)
	estimateReq := &model.Request{
		Model: "gpt-4o",
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "12345678901234567890"}},
		}},
		MaxTokens: 200,
	}

	reportUsage = true
	incompleteUsage = false
	usageTokens = model.TokenUsage{}
	_, err = wrapped.Complete(t.Context(), estimateReq)
	require.NoError(t, err)
}

func TestGRPCGatewayStreamRoundTripAndClientValidation(t *testing.T) {
	provider := &grpcCaptureProvider{
		onStream: func(_ context.Context, _ *model.Request) (model.Streamer, error) {
			return &stubStreamer{
				chunks: []model.Chunk{
					model.ThinkingChunk{
						Message: model.Message{
							Role: model.ConversationRoleAssistant,
							Parts: []model.Part{model.ThinkingPart{
								Text:      "step 1",
								Signature: "sig",
								Index:     0,
								Final:     true,
							}},
						},
					},
					model.CompletionDeltaChunk{
						Delta: model.CompletionDelta{
							Name:  "answer",
							Delta: `{"answer":`,
						},
					},
					model.CompletionChunk{
						Completion: model.Completion{
							Name:    "answer",
							Payload: rawjson.Message(`{"answer":"valid"}`),
						},
					},
					model.UsageChunk{
						Usage: model.TokenUsage{
							Model:        "gpt-4o",
							InputTokens:  10,
							OutputTokens: 5,
							TotalTokens:  15,
						},
					},
					model.StopChunk{Reason: "end_turn"},
				},
				recvErr: io.EOF,
				response: &model.Response{
					Content: []model.Message{{
						Role: model.ConversationRoleAssistant,
						Parts: []model.Part{
							model.ThinkingPart{
								Text:      "step 1",
								Signature: "sig",
								Index:     0,
								Final:     true,
							},
							model.TextPart{Text: `{"answer":"valid"}`},
						},
					}},
					Usage: model.TokenUsage{
						Model:        "gpt-4o",
						InputTokens:  10,
						OutputTokens: 5,
						TotalTokens:  15,
					},
					UsageReported: true,
					StopReason:    "end_turn",
				},
			}, nil
		},
	}
	srv, err := NewServer(WithProvider(provider))
	require.NoError(t, err)
	conn := startBufconnGRPCGateway(t, srv)
	client, err := NewGRPCClient(conn)
	require.NoError(t, err)

	req := structuredGatewayRequest(t)
	streamer, err := client.Stream(t.Context(), req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, streamer.Close())
	}()

	var chunks []model.Chunk
	for {
		chunk, recvErr := streamer.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		require.NoError(t, recvErr)
		chunks = append(chunks, chunk)
	}
	require.Len(t, chunks, 5)
	resp := streamer.Response()
	require.NotNil(t, resp)
	require.True(t, resp.HasUsage())
	require.Equal(t, 15, resp.Usage.TotalTokens)
}

func TestGRPCGatewayCountTokensAndStructuredErrors(t *testing.T) {
	contract, err := model.NewRequestContract(&model.Request{
		Tools: []*model.ToolDefinition{testLookupToolDefinition(t)},
	})
	require.NoError(t, err)
	validationErr := contract.RejectProviderOutput(
		model.OutputValidationToolArguments,
		&model.TokenUsage{InputTokens: 7, OutputTokens: 3, TotalTokens: 10},
		model.NewMalformedToolArgumentsError("svc.lookup", errors.New("missing id field")),
	)

	var completeErr error
	var countTokensVal int
	provider := &grpcCaptureProvider{
		onComplete: func(_ context.Context, _ *model.Request) (*model.Response, error) {
			return nil, completeErr
		},
		onCount: func(_ context.Context, req *model.Request) (model.TokenCount, error) {
			return model.TokenCount{
				Model:       req.Model,
				ModelClass:  req.ModelClass,
				InputTokens: countTokensVal,
				Exact:       true,
			}, nil
		},
	}
	srv, err := NewServer(WithProvider(provider))
	require.NoError(t, err)
	conn := startBufconnGRPCGateway(t, srv)
	client, err := NewGRPCClient(conn)
	require.NoError(t, err)
	rawProvider, err := newGRPCProvider(conn)
	require.NoError(t, err)

	// Verify 64-bit token counts (> math.MaxInt32) round-trip without truncation.
	const largeTokenCount = 4_294_967_297
	countTokensVal = largeTokenCount
	count, err := client.CountTokens(t.Context(), &model.Request{
		Model:      "claude-sonnet-4-20250514",
		ModelClass: model.ModelClassDefault,
	})
	require.NoError(t, err)
	require.Equal(t, largeTokenCount, count.InputTokens)
	require.True(t, count.Exact)

	// 1. OutputValidationError round-trip with RecoveryCorrection and Usage.
	completeErr = validationErr
	_, err = rawProvider.Complete(t.Context(), &model.Request{Model: "m"})
	var gotValidation *model.OutputValidationError
	require.ErrorAs(t, err, &gotValidation)
	require.Equal(t, model.OutputValidationToolArguments, gotValidation.Kind())
	require.NotNil(t, gotValidation.Usage())
	require.Equal(t, 10, gotValidation.Usage().TotalTokens)
	require.NotEmpty(t, gotValidation.RecoveryCorrection())
	toolName, _, ok := model.MalformedToolArgumentsToolName(err)
	require.True(t, ok)
	require.Equal(t, tools.Ident("svc.lookup"), toolName)

	// 2. Shared OutputValidationError node identity in errors.Join.
	completeErr = errors.Join(validationErr, validationErr)
	_, err = rawProvider.Complete(t.Context(), &model.Request{Model: "m"})
	exactVal, exactOK := exactGatewayValidation(err)
	require.True(t, exactOK, "joined identical OutputValidationError leaves must decode to the same pointer")
	require.NotNil(t, exactVal)

	// 3. RequestValidationError and capacity sentinels preserve typed identity.
	completeErr = fmt.Errorf("request too large: %w", model.ErrRequestByteCapacity)
	_, err = rawProvider.Complete(t.Context(), &model.Request{Model: "m"})
	require.ErrorIs(t, err, model.ErrRequestByteCapacity)
	var gotReqVal *model.RequestValidationError
	require.ErrorAs(t, err, &gotReqVal)

	completeErr = fmt.Errorf("image source overflow: %w", model.ErrImageSourceCapacity)
	_, err = rawProvider.Complete(t.Context(), &model.Request{Model: "m"})
	require.ErrorIs(t, err, model.ErrImageSourceCapacity)

	completeErr = model.NewRequestValidationError(fmt.Errorf("image source overflow: %w", model.ErrImageSourceCapacity))
	_, err = rawProvider.Complete(t.Context(), &model.Request{Model: "m"})
	require.ErrorIs(t, err, model.ErrImageSourceCapacity)
	gotReqVal = nil
	require.ErrorAs(t, err, &gotReqVal)

	completeErr = model.NewRequestValidationError(errors.New("custom request validation cause"))
	_, err = rawProvider.Complete(t.Context(), &model.Request{Model: "m"})
	gotReqVal = nil
	require.ErrorAs(t, err, &gotReqVal)
	require.EqualError(t, gotReqVal, "custom request validation cause")

	// 4. ProviderError wrapping ErrRateLimited and multi-level wrapped plain error without message duplication.
	wrappedPlain := fmt.Errorf("outer step: %w", fmt.Errorf("middle step: %w", errors.New("root cause")))
	completeErr = wrappedPlain
	_, err = rawProvider.Complete(t.Context(), &model.Request{Model: "m"})
	require.EqualError(t, err, "outer step: middle step: root cause")

	completeErr = model.NewProviderError(
		"bedrock",
		"converse",
		429,
		"throttling",
		"ThrottlingException",
		"rate exceeded",
		"",
		true,
		model.ErrRateLimited,
	)
	_, err = rawProvider.Complete(t.Context(), &model.Request{Model: "m"})
	require.ErrorIs(t, err, model.ErrRateLimited)
	var gotProviderErr *model.ProviderError
	require.ErrorAs(t, err, &gotProviderErr)
	require.Equal(t, "bedrock", gotProviderErr.Provider())
	require.Equal(t, 429, gotProviderErr.HTTPStatus())
	require.True(t, gotProviderErr.Retryable())

	// 5. Context deadline propagation.
	completeErr = nil
	provider.onComplete = func(ctx context.Context, _ *model.Request) (*model.Response, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err = rawProvider.Complete(ctx, &model.Request{Model: "m"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

type scriptedStreamClientStream struct {
	frames []*genmodelgateway.StreamFrame
	errs   []error
	idx    int
}

func (s *scriptedStreamClientStream) Recv() (*genmodelgateway.StreamFrame, error) {
	return s.RecvWithContext(context.Background())
}

func (s *scriptedStreamClientStream) RecvWithContext(context.Context) (*genmodelgateway.StreamFrame, error) {
	if s.idx >= len(s.frames) && s.idx >= len(s.errs) {
		return nil, io.EOF
	}
	var frame *genmodelgateway.StreamFrame
	if s.idx < len(s.frames) {
		frame = s.frames[s.idx]
	}
	var err error
	if s.idx < len(s.errs) {
		err = s.errs[s.idx]
	}
	s.idx++
	return frame, err
}

func TestGRPCStreamerTerminalFrameTransitionsAndUsageRetention(t *testing.T) {
	terminalFrame := &genmodelgateway.StreamFrame{
		Payload: genmodelgateway.NewStreamFramePayloadResponse(&genmodelgateway.ModelResponse{
			Content: []*genmodelgateway.ModelMessage{{
				Role: string(model.ConversationRoleAssistant),
				Parts: []*genmodelgateway.MessagePart{{
					Payload: genmodelgateway.NewMessagePartPayloadText(&genmodelgateway.TextPartDetail{
						Text: "done",
					}),
				}},
			}},
			Usage:         &genmodelgateway.TokenUsage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5},
			UsageReported: true,
			StopReason:    "end_turn",
		}),
	}
	zeroUsageTerminalFrame := &genmodelgateway.StreamFrame{
		Payload: genmodelgateway.NewStreamFramePayloadResponse(&genmodelgateway.ModelResponse{
			Content: []*genmodelgateway.ModelMessage{{
				Role: string(model.ConversationRoleAssistant),
				Parts: []*genmodelgateway.MessagePart{{
					Payload: genmodelgateway.NewMessagePartPayloadText(&genmodelgateway.TextPartDetail{
						Text: "cached",
					}),
				}},
			}},
			Usage:         &genmodelgateway.TokenUsage{Model: "gpt-4o"},
			UsageReported: true,
			StopReason:    "end_turn",
		}),
	}
	chunkFrame := &genmodelgateway.StreamFrame{
		Payload: genmodelgateway.NewStreamFramePayloadChunk(&genmodelgateway.StreamChunk{
			Payload: genmodelgateway.NewStreamChunkPayloadText(&genmodelgateway.ModelMessage{
				Role: string(model.ConversationRoleAssistant),
				Parts: []*genmodelgateway.MessagePart{{
					Payload: genmodelgateway.NewMessagePartPayloadText(&genmodelgateway.TextPartDetail{
						Text: "trailing",
					}),
				}},
			}),
		}),
	}

	t.Run("terminal_to_eof", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		streamer := &grpcStreamer{
			ctx:    ctx,
			cancel: cancel,
			stream: &scriptedStreamClientStream{
				frames: []*genmodelgateway.StreamFrame{terminalFrame, nil},
				errs:   []error{nil, io.EOF},
			},
		}
		chunk, err := streamer.Recv()
		require.Nil(t, chunk)
		require.ErrorIs(t, err, io.EOF)
		require.NotNil(t, streamer.Response())
		require.Equal(t, 5, streamer.Response().Usage.TotalTokens)
	})

	t.Run("terminal_to_chunk_retains_usage", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		streamer := &grpcStreamer{
			ctx:    ctx,
			cancel: cancel,
			stream: &scriptedStreamClientStream{
				frames: []*genmodelgateway.StreamFrame{terminalFrame, chunkFrame},
				errs:   []error{nil, nil},
			},
		}
		chunk, err := streamer.Recv()
		require.Nil(t, chunk)
		require.ErrorContains(t, err, "unexpected frame after terminal response")
		require.Nil(t, streamer.Response())
		retained := model.UsageFromError(err)
		require.NotNil(t, retained)
		require.Equal(t, 5, retained.TotalTokens)
	})

	t.Run("terminal_to_terminal_retains_usage", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		streamer := &grpcStreamer{
			ctx:    ctx,
			cancel: cancel,
			stream: &scriptedStreamClientStream{
				frames: []*genmodelgateway.StreamFrame{terminalFrame, terminalFrame},
				errs:   []error{nil, nil},
			},
		}
		chunk, err := streamer.Recv()
		require.Nil(t, chunk)
		require.ErrorContains(t, err, "unexpected frame after terminal response")
		require.Nil(t, streamer.Response())
		retained := model.UsageFromError(err)
		require.NotNil(t, retained)
		require.Equal(t, 5, retained.TotalTokens)
	})

	t.Run("terminal_to_transport_error_retains_usage_and_reported_zero", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		transportBoom := errors.New("broken stream trailer")
		streamer := &grpcStreamer{
			ctx:    ctx,
			cancel: cancel,
			stream: &scriptedStreamClientStream{
				frames: []*genmodelgateway.StreamFrame{zeroUsageTerminalFrame, nil},
				errs:   []error{nil, transportBoom},
			},
		}
		chunk, err := streamer.Recv()
		require.Nil(t, chunk)
		require.ErrorIs(t, err, transportBoom)
		require.Nil(t, streamer.Response())
		retained := model.UsageFromError(err)
		require.NotNil(t, retained)
		require.Equal(t, "gpt-4o", retained.Model)
		require.Zero(t, retained.TotalTokens)
	})

	t.Run("provider_close_error_after_terminal_retains_usage", func(t *testing.T) {
		closeBoom := errors.New("provider stream close failed")
		provider := &grpcCaptureProvider{
			onStream: func(_ context.Context, _ *model.Request) (model.Streamer, error) {
				return &stubStreamer{
					recvErr:  io.EOF,
					closeErr: closeBoom,
					response: &model.Response{
						Content: []model.Message{{
							Role:  model.ConversationRoleAssistant,
							Parts: []model.Part{model.TextPart{Text: "done"}},
						}},
						Usage: model.TokenUsage{
							Model:        "gpt-4o",
							InputTokens:  12,
							OutputTokens: 4,
							TotalTokens:  16,
						},
						UsageReported: true,
						StopReason:    "end_turn",
					},
				}, nil
			},
		}
		srv, err := NewServer(WithProvider(provider))
		require.NoError(t, err)
		conn := startBufconnGRPCGateway(t, srv)
		rawProvider, err := newGRPCProvider(conn)
		require.NoError(t, err)
		st, err := rawProvider.Stream(t.Context(), &model.Request{Model: "gpt-4o"})
		require.NoError(t, err)
		defer func() { require.NoError(t, st.Close()) }()

		_, recvErr := st.Recv()
		require.ErrorContains(t, recvErr, "provider stream close failed")
		require.Nil(t, st.Response())
		retained := model.UsageFromError(recvErr)
		require.NotNil(t, retained)
		require.Equal(t, 16, retained.TotalTokens)
	})
}

func TestGRPCErrorGraphSharedDAGAndMalformedWireErrors(t *testing.T) {
	t.Run("shared_dag_with_backward_reference_preserves_exact_validation_identity", func(t *testing.T) {
		v, err := model.RestoreOutputValidationError(
			model.OutputValidationToolArguments,
			model.NewMalformedToolArgumentsError("svc.lookup", errors.New("unexpected EOF")),
			model.ResponseEvidence{},
			&model.TokenUsage{Model: "gpt-4o", InputTokens: 10, OutputTokens: 0, TotalTokens: 10},
		)
		require.NoError(t, err)
		incompleteV := model.RetainIncompleteUsage(v, *v.Usage())
		//nolint:errorlint // Verify RetainIncompleteUsage preserves exact root type.
		exactV, ok := incompleteV.(*model.OutputValidationError)
		require.True(t, ok)

		// Join exactV directly with a wrapper around exactV so node 2 references node 1 (1 < 2).
		joined := errors.Join(exactV, fmt.Errorf("context: %w", exactV))
		wire := toGenError(joined)
		require.NotNil(t, wire)

		decoded := fromGenError(wire)
		require.Error(t, decoded)
		require.True(t, isExactGatewayValidation(decoded))
		require.True(t, model.UsageIncompleteFromError(decoded))
		var outErr *model.OutputValidationError
		require.ErrorAs(t, decoded, &outErr)
		require.True(t, outErr.UsageIncomplete())
		require.Equal(t, 10, outErr.Usage().InputTokens)
	})

	t.Run("incomplete_retained_usage_round_trips_on_provider_error", func(t *testing.T) {
		provErr := model.NewProviderError(
			"openai",
			"responses.create",
			502,
			model.ProviderErrorKindUnavailable,
			"bad_gateway",
			"upstream failed on round 2",
			"req-99",
			true,
			nil,
		)
		withIncomplete := model.RetainIncompleteUsage(provErr, model.TokenUsage{
			Model:       "gpt-4o",
			InputTokens: 12,
			TotalTokens: 12,
		})
		wire := toGenError(withIncomplete)
		require.NotNil(t, wire)
		require.True(t, wire.RetainedUsageIncomplete)

		decoded := fromGenError(wire)
		require.Error(t, decoded)
		require.True(t, model.UsageIncompleteFromError(decoded))
		require.Equal(t, 12, model.UsageFromError(decoded).InputTokens)
	})

	t.Run("malformed_and_cyclic_wire_error_graphs_return_non_nil_errors", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			wire *genmodelgateway.GatewayError
			want string
		}{
			{
				name: "empty_nodes",
				wire: &genmodelgateway.GatewayError{},
				want: "empty error node list",
			},
			{
				name: "self_loop_joined",
				wire: &genmodelgateway.GatewayError{
					Nodes: []*genmodelgateway.GatewayErrorNode{{
						Kind:         errorKindJoined,
						CauseIndex:   -1,
						ChildIndices: []int64{0},
					}},
				},
				want: "cycle detected",
			},
			{
				name: "two_node_cycle",
				wire: &genmodelgateway.GatewayError{
					Nodes: []*genmodelgateway.GatewayErrorNode{
						{Kind: errorKindPlain, Message: "a", CauseIndex: 1},
						{Kind: errorKindPlain, Message: "b", CauseIndex: 0},
					},
				},
				want: "cycle detected",
			},
			{
				name: "empty_joined_children",
				wire: &genmodelgateway.GatewayError{
					Nodes: []*genmodelgateway.GatewayErrorNode{{
						Kind:       errorKindJoined,
						CauseIndex: -1,
					}},
				},
				want: "no children",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				err := fromGenError(tc.wire)
				require.ErrorContains(t, err, tc.want)
			})
		}
	})
}
