package assistantapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	chat "example.com/assistant/gen/orchestrator/agents/chat"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/memory"
	memoryinmem "goa.design/goa-ai/runtime/agent/memory/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	runinmem "goa.design/goa-ai/runtime/agent/run/inmem"
	agentsruntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/agent/telemetry"
	mcpruntime "goa.design/goa-ai/runtime/mcp"
)

type (
	// RuntimeHarness wires together the generated agents, the example MCP client,
	// and an in-process workflow engine so tests (and future docs) can demonstrate
	// the full data-loop without Temporal. It provides an in-memory implementation
	// suitable for examples, unit tests, and development.
	RuntimeHarness struct {
		// runtime is the core agent runtime that executes workflows and manages
		// the agent lifecycle.
		runtime *agentsruntime.Runtime
		// engine is the in-process workflow execution engine that synchronously
		// runs workflows and activities without external dependencies.
		engine *exampleEngine
		// memory stores conversational memory events in-memory for the lifetime
		// of the harness.
		memory *memoryinmem.Store
		// stream captures or forwards streaming events emitted during workflow
		// execution.
		stream stream.Sink
	}

	// captureSink stores stream events for inspection in tests and examples.
	// It implements stream.Sink by buffering all events in memory.
	captureSink struct {
		mu     sync.Mutex
		events []stream.Event
	}

	// exampleEngine records workflows and activities so the harness can execute
	// them synchronously without Temporal. It maintains registries of workflow
	// and activity definitions and provides synchronous invocation.
	exampleEngine struct {
		mu         sync.RWMutex
		workflows  map[string]engine.WorkflowDefinition
		activities map[string]engine.ActivityDefinition
	}

	// exampleWorkflowContext routes ExecuteActivity calls back into the registered
	// activity handlers so the runtime loop behaves just like it would inside
	// Temporal. It provides a minimal in-process implementation of the workflow
	// context interface.
	exampleWorkflowContext struct {
		ctx        context.Context
		engine     *exampleEngine
		workflowID string
		runID      string
		signals    map[string]*harnessSignalChannel
	}

	// immediateFuture returns pre-computed activity results, satisfying the
	// engine.Future contract used by the runtime's executeToolCalls helper.
	// Since this is an in-process engine, futures resolve immediately.
	immediateFuture struct {
		result any
		err    error
	}

	// harnessSignalChannel implements engine.SignalChannel using a buffered
	// Go channel for pause/resume signals in the in-process engine.
	harnessSignalChannel struct {
		ch chan any
	}

	// exampleStreamingModel is a minimal model.Client implementation that
	// returns deterministic responses for examples and tests. It supports
	// streaming to demonstrate the planner stream consumption path.
	exampleStreamingModel struct{}

	// exampleStreamer implements model.Streamer by returning pre-computed
	// chunks. This allows examples and tests to demonstrate streaming without
	// calling real LLM APIs.
	exampleStreamer struct {
		chunks []model.Chunk
		index  int
		meta   map[string]any
	}
)

const chatModelID = "example.chat.streaming"

// NewRuntimeHarness constructs a runtime with in-memory stores, registers the
// generated MCP toolset helper, and wires the chat agent planner. The returned
// harness can execute workflows entirely in-process, making it ideal for
// examples and documentation snippets.
//
// The harness uses a capture sink by default; use NewRuntimeHarnessWithSink
// to provide a custom sink (e.g., Pulse for SSE streaming).
func NewRuntimeHarness(ctx context.Context) (*RuntimeHarness, error) {
	return NewRuntimeHarnessWithSink(ctx, newCaptureSink())
}

// NewRuntimeHarnessWithSink constructs a runtime using the provided stream.Sink.
// Use this to inject a Pulse-backed sink (for SSE) or a capture sink (for tests).
// Telemetry (Logger/Metrics/Tracer) defaults to noop implementations; callers can
// construct the Runtime directly with agentsruntime.New() if they need observability.
//
// The harness registers the chat agent with an example MCP caller that returns
// deterministic responses. It sets agentsruntime.Default() to the constructed
// runtime so in-process execution uses this harness instance.
func NewRuntimeHarnessWithSink(
	ctx context.Context,
	sink stream.Sink,
) (*RuntimeHarness, error) {
	eng := newExampleEngine()
	mem := memoryinmem.New()
	runs := runinmem.New()

	rt := agentsruntime.New(agentsruntime.Options{
		Engine:      eng,
		MemoryStore: mem,
		RunStore:    runs,
		Stream:      sink,
		// Logger, Metrics, Tracer: omitted, defaults to noop for examples
	})
	// Generated workflow and activities use agentsruntime.Default(); set it here
	// so in-process execution uses this harness runtime instance.
	agentsruntime.SetDefault(rt)

	if err := rt.RegisterModel(chatModelID, newStreamingModel()); err != nil {
		return nil, fmt.Errorf("register model: %w", err)
	}

	if err := chat.RegisterChatAgent(ctx, rt, chat.ChatAgentConfig{
		Planner: newChatPlanner(chatModelID),
		MCPCallers: map[string]mcpruntime.Caller{
			chat.ChatAssistantAssistantMcpToolsetID: newExampleCaller(),
		},
	}); err != nil {
		return nil, fmt.Errorf("register chat agent: %w", err)
	}

	return &RuntimeHarness{
		runtime: rt,
		engine:  eng,
		memory:  mem,
		stream:  sink,
	}, nil
}

// Run executes the registered chat workflow in-process and returns the final
// runtime output. The provided RunInput should set AgentID and RunID to keep the
// memory store deterministic. This method blocks until the workflow completes.
//
// The workflow executes synchronously in the current goroutine, invoking
// activities directly without queuing. Memory events and stream events are
// captured during execution and can be retrieved after Run returns.
func (h *RuntimeHarness) Run(
	ctx context.Context,
	input agentsruntime.RunInput,
) (agentsruntime.RunOutput, error) {
	if input.AgentID == "" {
		return agentsruntime.RunOutput{}, errors.New("AgentID is required")
	}
	if input.RunID == "" {
		return agentsruntime.RunOutput{}, errors.New("RunID is required")
	}
	def, ok := h.engine.workflow("orchestrator.chat.workflow")
	if !ok {
		return agentsruntime.RunOutput{}, errors.New("chat workflow not registered")
	}
	wfCtx := newExampleWorkflowContext(ctx, h.engine, def.Name, input.RunID)
	result, err := def.Handler(wfCtx, input)
	if err != nil {
		return agentsruntime.RunOutput{}, err
	}
	if ptr, ok := result.(*agentsruntime.RunOutput); ok && ptr != nil {
		return *ptr, nil
	}
	return agentsruntime.RunOutput{}, fmt.Errorf("unexpected workflow output %T", result)
}

// MemoryEvents returns the durable memory events recorded for the given run.
// These events represent the conversational history stored in the in-memory
// memory store, including user messages, assistant messages, tool calls, and
// tool results.
func (h *RuntimeHarness) MemoryEvents(
	ctx context.Context,
	agentID, runID string,
) ([]memory.Event, error) {
	snapshot, err := h.memory.LoadRun(ctx, agentID, runID)
	if err != nil {
		return nil, err
	}
	return snapshot.Events, nil
}

// StreamEvents returns a copy of the streaming events captured during the last run.
// Only available when the harness was constructed with a captureSink. Returns nil
// if a different sink implementation was provided.
func (h *RuntimeHarness) StreamEvents() []stream.Event {
	if c, ok := h.stream.(*captureSink); ok {
		return c.Events()
	}
	return nil
}

// Send appends the event to the internal buffer for later inspection.
// Thread-safe for concurrent sends.
func (s *captureSink) Send(ctx context.Context, event stream.Event) error {
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
	return nil
}

// Close is a no-op for the capture sink since there are no resources to release.
func (s *captureSink) Close(context.Context) error {
	return nil
}

// Events returns a copy of all captured events. The returned slice is independent
// of the internal buffer, so modifications won't affect future captures.
func (s *captureSink) Events() []stream.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]stream.Event, len(s.events))
	copy(out, s.events)
	return out
}

// RegisterWorkflow records the workflow definition for later invocation by Run().
func (e *exampleEngine) RegisterWorkflow(
	ctx context.Context,
	def engine.WorkflowDefinition,
) error {
	e.mu.Lock()
	e.workflows[def.Name] = def
	e.mu.Unlock()
	return nil
}

// RegisterActivity records the activity definition for later invocation by
// ExecuteActivity() calls from workflows.
func (e *exampleEngine) RegisterActivity(
	ctx context.Context,
	def engine.ActivityDefinition,
) error {
	e.mu.Lock()
	e.activities[def.Name] = def
	e.mu.Unlock()
	return nil
}

// StartWorkflow returns an error because the example engine executes workflows
// directly via the Run() method rather than async queue-based execution.
func (e *exampleEngine) StartWorkflow(
	context.Context,
	engine.WorkflowStartRequest,
) (engine.WorkflowHandle, error) {
	return nil, errors.New("example engine does not start workflows")
}

// Context returns the underlying context. The context is inherited from the
// Run() call that initiated the workflow.
func (w *exampleWorkflowContext) Context() context.Context {
	return w.ctx
}

// WorkflowID returns the workflow identifier assigned at construction.
func (w *exampleWorkflowContext) WorkflowID() string {
	return w.workflowID
}

// RunID returns the run identifier passed to the workflow at invocation.
func (w *exampleWorkflowContext) RunID() string {
	return w.runID
}

// ExecuteActivity invokes the registered activity handler synchronously, injecting
// the workflow context for nested agent execution support. The result is assigned
// to the provided pointer using type-specific logic.
func (w *exampleWorkflowContext) ExecuteActivity(
	ctx context.Context,
	req engine.ActivityRequest,
	result any,
) error {
	// Inject workflow context into activity handler context so runtime code
	// can retrieve it (e.g., for nested agent execution).
	ctx = engine.WithWorkflowContext(ctx, w)
	out, err := w.engine.invoke(ctx, req)
	if err != nil {
		return err
	}
	if err := assignActivityResult(result, out); err != nil {
		return fmt.Errorf("assign activity result: %w", err)
	}
	return nil
}

// ExecuteActivityAsync invokes the registered activity handler and returns an
// immediate future with the result, injecting the workflow context. Since this
// is an in-process engine, the future resolves immediately.
func (w *exampleWorkflowContext) ExecuteActivityAsync(
	ctx context.Context,
	req engine.ActivityRequest,
) (engine.Future, error) {
	// Inject workflow context into activity handler context for async as well.
	ctx = engine.WithWorkflowContext(ctx, w)
	out, err := w.engine.invoke(ctx, req)
	return &immediateFuture{result: out, err: err}, nil
}

// Logger returns a no-op logger since the example harness doesn't configure
// telemetry by default.
func (w *exampleWorkflowContext) Logger() telemetry.Logger {
	return telemetry.NewNoopLogger()
}

// Metrics returns a no-op metrics recorder since the example harness doesn't
// configure telemetry by default.
func (w *exampleWorkflowContext) Metrics() telemetry.Metrics {
	return telemetry.NewNoopMetrics()
}

// Tracer returns a no-op tracer since the example harness doesn't configure
// telemetry by default.
func (w *exampleWorkflowContext) Tracer() telemetry.Tracer {
	return telemetry.NewNoopTracer()
}

// Now returns a deterministic workflow time for the in-process harness.
func (w *exampleWorkflowContext) Now() time.Time { return time.Unix(0, 0) }

// SignalChannel returns or creates a buffered channel for the given signal name.
// Used for pause/resume workflow control in the in-process engine.
func (w *exampleWorkflowContext) SignalChannel(name string) engine.SignalChannel {
	if w.signals == nil {
		w.signals = make(map[string]*harnessSignalChannel)
	}
	ch, ok := w.signals[name]
	if !ok {
		ch = &harnessSignalChannel{ch: make(chan any, 1)}
		w.signals[name] = ch
	}
	return ch
}

// Get returns the pre-computed result or error. If the activity succeeded, assigns
// the result to the provided pointer. If the activity failed, returns the error.
func (f *immediateFuture) Get(ctx context.Context, result any) error {
	if f.err != nil {
		return f.err
	}
	if err := assignActivityResult(result, f.result); err != nil {
		return fmt.Errorf("assign activity result: %w", err)
	}
	return nil
}

// IsReady always returns true since results are pre-computed in the in-process
// engine.
func (f *immediateFuture) IsReady() bool {
	return true
}

// Receive blocks until a signal value is available or the context is canceled.
// The received value is assigned to dest using JSON round-trip conversion.
func (s *harnessSignalChannel) Receive(ctx context.Context, dest interface{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case val := <-s.ch:
		if err := assignActivityResult(dest, val); err != nil {
			return fmt.Errorf("assign signal value: %w", err)
		}
		return nil
	}
}

// ReceiveAsync attempts to receive a signal value without blocking. Returns true
// if a value was received, false otherwise. The received value is assigned to dest.
func (s *harnessSignalChannel) ReceiveAsync(dest interface{}) bool {
	select {
	case val := <-s.ch:
		if err := assignActivityResult(dest, val); err != nil {
			return false
		}
		return true
	default:
		return false
	}
}

// Complete implements model.Client.Complete by returning a deterministic response.
// This is used when streaming is not requested.
func (m *exampleStreamingModel) Complete(
	context.Context,
	model.Request,
) (model.Response, error) {
	return model.Response{
		Content:    []model.Message{{Role: "assistant", Content: "Streaming model received request."}},
		StopReason: "stop",
	}, nil
}

// Stream implements model.Client.Stream by returning a pre-computed streamer with
// deterministic chunks. This allows examples and tests to demonstrate streaming
// without calling real LLM APIs.
func (m *exampleStreamingModel) Stream(
	_ context.Context,
	req model.Request,
) (model.Streamer, error) {
	summary := streamingSummary(req.Messages)
	chunks := []model.Chunk{
		{
			Type:    model.ChunkTypeThinking,
			Message: model.Message{Role: "assistant", Content: "Thinking about the MCP findings..."},
		},
		{
			Type:    model.ChunkTypeText,
			Message: model.Message{Role: "assistant", Content: summary},
		},
		{
			Type:       model.ChunkTypeStop,
			StopReason: "stop",
		},
	}
	tokens := max(1, len(summary)/16)
	meta := map[string]any{"usage": model.TokenUsage{OutputTokens: tokens, TotalTokens: tokens}}
	return &exampleStreamer{chunks: chunks, meta: meta}, nil
}

// Recv returns the next chunk in the stream. Returns io.EOF when all chunks
// have been consumed.
func (s *exampleStreamer) Recv() (model.Chunk, error) {
	if s.index >= len(s.chunks) {
		return model.Chunk{}, io.EOF
	}
	chunk := s.chunks[s.index]
	s.index++
	return chunk, nil
}

// Close is a no-op since the example streamer holds no resources.
func (s *exampleStreamer) Close() error {
	return nil
}

// Metadata returns the usage statistics and other metadata for this stream.
func (s *exampleStreamer) Metadata() map[string]any {
	return s.meta
}

// newCaptureSink constructs a capture sink for tests and examples.
func newCaptureSink() *captureSink {
	return &captureSink{}
}

// newExampleEngine constructs an in-process workflow engine with empty registries.
func newExampleEngine() *exampleEngine {
	return &exampleEngine{
		workflows:  make(map[string]engine.WorkflowDefinition),
		activities: make(map[string]engine.ActivityDefinition),
	}
}

// newExampleWorkflowContext constructs a workflow context that routes activities
// through the example engine for synchronous in-process execution.
func newExampleWorkflowContext(
	ctx context.Context,
	eng *exampleEngine,
	workflowID, runID string,
) *exampleWorkflowContext {
	return &exampleWorkflowContext{
		ctx:        ctx,
		engine:     eng,
		workflowID: workflowID,
		runID:      runID,
	}
}

// newExampleCaller constructs a mock MCP caller for tests and examples. It returns
// deterministic responses for the "search" tool and errors for unknown tools.
func newExampleCaller() mcpruntime.Caller {
	return mcpruntime.CallerFunc(
		func(ctx context.Context, req mcpruntime.CallRequest) (mcpruntime.CallResponse, error) {
			switch req.Tool {
			case "search":
				var payload struct {
					Query string `json:"query"`
					Limit int    `json:"limit"`
				}
				_ = json.Unmarshal(req.Payload, &payload)
				if payload.Query == "" {
					payload.Query = "status update"
				}
				doc := map[string]any{
					"title":   fmt.Sprintf("Result for %s", payload.Query),
					"content": "System reports nominal operations in this mock",
				}
				body, _ := json.Marshal(map[string]any{"documents": []any{doc}})
				structured, _ := json.Marshal(map[string]any{"source": "mcp", "tool": req.Tool})
				return mcpruntime.CallResponse{Result: body, Structured: structured}, nil
			default:
				return mcpruntime.CallResponse{},
					fmt.Errorf("tool %q not implemented in example caller", req.Tool)
			}
		},
	)
}

// newStreamingModel constructs the example streaming model client.
func newStreamingModel() model.Client {
	return &exampleStreamingModel{}
}

// workflow looks up a workflow definition by name. Thread-safe for concurrent reads.
func (e *exampleEngine) workflow(name string) (engine.WorkflowDefinition, bool) {
	e.mu.RLock()
	def, ok := e.workflows[name]
	e.mu.RUnlock()
	return def, ok
}

// activity looks up an activity definition by name. Thread-safe for concurrent reads.
func (e *exampleEngine) activity(name string) (engine.ActivityDefinition, bool) {
	e.mu.RLock()
	def, ok := e.activities[name]
	e.mu.RUnlock()
	return def, ok
}

// invoke executes an activity by name and returns the result. Returns an error
// if the activity is not registered or if the handler fails.
func (e *exampleEngine) invoke(ctx context.Context, req engine.ActivityRequest) (any, error) {
	def, ok := e.activity(req.Name)
	if !ok {
		return nil, fmt.Errorf("activity %q not registered", req.Name)
	}
	return def.Handler(ctx, req.Input)
}

// streamingSummary generates a deterministic summary text from the message history
// for the example streaming model.
func streamingSummary(messages []model.Message) string {
	if len(messages) == 0 {
		return "No context available for streaming response."
	}
	last := messages[len(messages)-1].Content
	if last == "" {
		return "Streaming response ready."
	}
	return fmt.Sprintf("Streaming refinement: %s", last)
}

// max returns the larger of two integers.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// assignActivityResult type-asserts the activity output and assigns it to the
// target pointer. Supports PlanActivityOutput and ToolOutput result types.
func assignActivityResult(target any, value any) error {
	if target == nil {
		return nil
	}
	switch t := target.(type) {
	case *agentsruntime.PlanActivityOutput:
		out, ok := value.(agentsruntime.PlanActivityOutput)
		if !ok {
			return fmt.Errorf("expected PlanActivityOutput, got %T", value)
		}
		*t = out
		return nil
	case *agentsruntime.ToolOutput:
		out, ok := value.(agentsruntime.ToolOutput)
		if !ok {
			return fmt.Errorf("expected ToolOutput, got %T", value)
		}
		*t = out
		return nil
	default:
		return fmt.Errorf("unsupported activity result %T", target)
	}
}
