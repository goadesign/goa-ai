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

func TestGRPCGatewayPreservesExplicitZeroVsUnreportedTokenUsage(t *testing.T) {
	var reportUsage bool
	provider := &grpcCaptureProvider{
		onComplete: func(_ context.Context, _ *model.Request) (*model.Response, error) {
			return &model.Response{
				Content: []model.Message{{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "ok"}},
				}},
				Usage: model.TokenUsage{
					Model:      "gpt-4o",
					ModelClass: model.ModelClassDefault,
				},
				UsageReported: reportUsage,
				StopReason:    "end_turn",
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
	unreported, err := client.Complete(t.Context(), &model.Request{Model: "gpt-4o"})
	require.NoError(t, err)
	require.False(t, unreported.HasUsage())

	reportUsage = true
	reportedZero, err := client.Complete(t.Context(), &model.Request{Model: "gpt-4o"})
	require.NoError(t, err)
	require.True(t, reportedZero.HasUsage())
	require.Zero(t, reportedZero.Usage.TotalTokens)

	// Verify UsageReconciledAdaptiveRateLimiter settles explicit 0-token usage
	// while keeping the reservation when usage was unreported.
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
	provider := &grpcCaptureProvider{
		onComplete: func(_ context.Context, _ *model.Request) (*model.Response, error) {
			return nil, completeErr
		},
		onCount: func(_ context.Context, req *model.Request) (model.TokenCount, error) {
			return model.TokenCount{
				Model:       req.Model,
				ModelClass:  req.ModelClass,
				InputTokens: 77,
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

	count, err := client.CountTokens(t.Context(), &model.Request{
		Model:      "claude-sonnet-4-20250514",
		ModelClass: model.ModelClassDefault,
	})
	require.NoError(t, err)
	require.Equal(t, 77, count.InputTokens)
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

	// 2. ProviderError wrapping ErrRateLimited and multi-level wrapped plain error without message duplication.
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

	// 3. Context deadline propagation.
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

func TestGRPCStreamerTerminalFrameTransitions(t *testing.T) {
	terminalFrame := &genmodelgateway.StreamFrame{
		Kind: streamFrameKindResponse,
		Response: &genmodelgateway.ModelResponse{
			Content: []*genmodelgateway.ModelMessage{{
				Role: string(model.ConversationRoleAssistant),
				Parts: []*genmodelgateway.MessagePart{{
					Kind: partKindText,
					Text: "done",
				}},
			}},
			Usage:         &genmodelgateway.TokenUsage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5},
			UsageReported: true,
			StopReason:    "end_turn",
		},
	}
	chunkFrame := &genmodelgateway.StreamFrame{
		Kind: streamFrameKindChunk,
		Chunk: &genmodelgateway.StreamChunk{
			Kind: model.ChunkTypeText,
			Message: &genmodelgateway.ModelMessage{
				Role: string(model.ConversationRoleAssistant),
				Parts: []*genmodelgateway.MessagePart{{
					Kind: partKindText,
					Text: "trailing",
				}},
			},
		},
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

	t.Run("terminal_to_chunk", func(t *testing.T) {
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
	})

	t.Run("terminal_to_terminal", func(t *testing.T) {
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
	})

	t.Run("terminal_to_transport_error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		transportBoom := errors.New("broken stream trailer")
		streamer := &grpcStreamer{
			ctx:    ctx,
			cancel: cancel,
			stream: &scriptedStreamClientStream{
				frames: []*genmodelgateway.StreamFrame{terminalFrame, nil},
				errs:   []error{nil, transportBoom},
			},
		}
		chunk, err := streamer.Recv()
		require.Nil(t, chunk)
		require.ErrorIs(t, err, transportBoom)
		require.Nil(t, streamer.Response())
	})
}
