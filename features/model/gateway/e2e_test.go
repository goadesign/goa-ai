package gateway

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

// --- Test helpers ---

type seqStreamer struct {
	chunks   []model.Chunk
	idx      int
	meta     map[string]any
	response *model.Response
}

func (s *seqStreamer) Recv() (model.Chunk, error) {
	if s.idx >= len(s.chunks) {
		return nil, io.EOF
	}
	c := s.chunks[s.idx]
	s.idx++
	return c, nil
}
func (s *seqStreamer) Close() error              { return nil }
func (s *seqStreamer) Response() *model.Response { return s.response }
func (s *seqStreamer) Metadata() map[string]any  { return s.meta }

type captureProvider struct {
	lastReq atomic.Value // model.Request
}

func (p *captureProvider) Complete(_ context.Context, req *model.Request) (*model.Response, error) {
	p.lastReq.Store(*req)
	return &model.Response{
		Content:    []model.Message{{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}},
		StopReason: "done",
	}, nil
}
func (p *captureProvider) Stream(_ context.Context, req *model.Request) (model.Streamer, error) {
	p.lastReq.Store(*req)
	return &seqStreamer{
		chunks: []model.Chunk{
			model.TextChunk{Message: model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "hello"}}}},
			model.ToolCallChunk{
				ToolCall: model.ToolCall{
					Name:    "emit_tool",
					Payload: rawjson.Message([]byte(`{"k":"v"}`)),
					ID:      "call-1",
				},
			},
			model.UsageChunk{Usage: model.TokenUsage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3}},
			model.StopChunk{Reason: "stop_sequence"},
		},
		response: &model.Response{
			Content: []model.Message{{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{
					model.TextPart{Text: "hello"},
					model.ToolUsePart{
						Name:  "emit_tool",
						Input: rawjson.Message([]byte(`{"k":"v"}`)),
						ID:    "call-1",
					},
				},
			}},
			Usage:      model.TokenUsage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3},
			StopReason: "stop_sequence",
		},
	}, nil
}

// stream wrapper turning server.Stream into model.Streamer
type serverStreamWrapper struct {
	ch       chan model.Chunk
	done     chan struct{}
	response *model.Response
	err      error
}

func (w *serverStreamWrapper) Recv() (model.Chunk, error) {
	c, ok := <-w.ch
	if !ok {
		<-w.done
		if w.err != nil {
			return nil, w.err
		}
		return nil, io.EOF
	}
	return c, nil
}
func (w *serverStreamWrapper) Close() error              { return nil }
func (w *serverStreamWrapper) Response() *model.Response { return w.response }
func (w *serverStreamWrapper) Metadata() map[string]any  { return nil }

// --- Tests ---

func TestE2E_UnaryComplete_WithMiddleware(t *testing.T) {
	prov := &captureProvider{}
	var unaryCount int32
	// middleware increments count and bumps temperature
	bumpTemp := func(next UnaryHandler) UnaryHandler {
		return func(ctx context.Context, req *model.Request) (*model.Response, error) {
			atomic.AddInt32(&unaryCount, 1)
			req.Temperature = 0.42
			return next(ctx, req)
		}
	}
	srv, err := NewServer(WithProvider(prov), WithUnary(bumpTemp))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	// remote client backed by server handlers
	completeFn := func(ctx context.Context, req *model.Request) (*model.Response, error) {
		return srv.Complete(ctx, req)
	}
	streamFn := func(ctx context.Context, req *model.Request) (model.Streamer, error) {
		wrapper := &serverStreamWrapper{ch: make(chan model.Chunk, 8), done: make(chan struct{})}
		go func() {
			wrapper.response, wrapper.err = srv.Stream(ctx, req, func(c model.Chunk) error { wrapper.ch <- c; return nil })
			close(wrapper.ch)
			close(wrapper.done)
		}()
		return wrapper, nil
	}
	client := NewRemoteClient(completeFn, streamFn)

	// call complete
	resp, err := client.Complete(context.Background(), &model.Request{Model: "m", Messages: []*model.Message{{Role: "user", Parts: []model.Part{model.TextPart{Text: "hi"}}}}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(resp.Content) == 0 {
		t.Fatalf("unexpected response: %#v", resp)
	}
	// Expect first message to contain a single text part "ok"
	ok := false
	if len(resp.Content[0].Parts) > 0 {
		if tp, ok2 := resp.Content[0].Parts[0].(model.TextPart); ok2 && tp.Text == "ok" {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if atomic.LoadInt32(&unaryCount) != 1 {
		t.Fatal("unary middleware did not run")
	}
	// verify provider saw temperature change
	if v, _ := prov.lastReq.Load().(model.Request); v.Temperature != 0.42 {
		t.Fatalf("middleware did not modify request, got %+v", v)
	}
}

func TestE2E_Stream_WithMiddleware(t *testing.T) {
	prov := &captureProvider{}
	var streamCount int32
	countMW := func(next StreamHandler) StreamHandler {
		return func(ctx context.Context, req *model.Request, send func(model.Chunk) error) (*model.Response, error) {
			atomic.AddInt32(&streamCount, 1)
			return next(ctx, req, send)
		}
	}
	srv, err := NewServer(WithProvider(prov), WithStream(countMW))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	streamFn := func(ctx context.Context, req *model.Request) (model.Streamer, error) {
		wrapper := &serverStreamWrapper{ch: make(chan model.Chunk, 8), done: make(chan struct{})}
		go func() {
			wrapper.response, wrapper.err = srv.Stream(ctx, req, func(c model.Chunk) error { wrapper.ch <- c; return nil })
			close(wrapper.ch)
			close(wrapper.done)
		}()
		return wrapper, nil
	}
	client := NewRemoteClient(nil, streamFn)

	st, err := client.Stream(context.Background(), &model.Request{Model: "m", Messages: []*model.Message{{Role: "user", Parts: []model.Part{model.TextPart{Text: "hi"}}}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			t.Errorf("stream close: %v", cerr)
		}
	}()

	// Expect all presentation chunks in order.
	expectTypes := []string{
		model.ChunkTypeText,
		model.ChunkTypeToolCall,
		model.ChunkTypeUsage,
		model.ChunkTypeStop,
	}
	for i, et := range expectTypes {
		ch, rerr := st.Recv()
		if rerr != nil {
			t.Fatalf("recv %d: %v", i, rerr)
		}
		if ch.Kind() != et {
			t.Fatalf("chunk %d type = %s, want %s", i, ch.Kind(), et)
		}
	}
	// then EOF
	if _, rerr := st.Recv(); !errors.Is(rerr, io.EOF) {
		t.Fatalf("stream end = %v, want EOF", rerr)
	}
	if st.Response() == nil {
		t.Fatal("canonical response was not transported through gateway")
	}
	if atomic.LoadInt32(&streamCount) != 1 {
		t.Fatal("stream middleware did not run")
	}
}
