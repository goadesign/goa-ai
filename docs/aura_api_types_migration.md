# AURA API Types Migration Plan

This plan identifies specific opportunities to replace AURA's ad-hoc types with goa-ai's standardized API types, reducing duplication and ensuring consistency across services.

## Overview

goa-ai provides a complete set of standardized types for agent interactions:
- **Run types**: `AgentRunPayload`, `AgentRunResult`, `AgentRunChunk`
- **Message types**: `AgentMessage`
- **Tool types**: `AgentToolEvent`, `AgentToolError`, `AgentRetryHint`, `AgentToolTelemetry`
- **Planner types**: `AgentPlannerAnnotation`
- **Streaming types**: `AgentToolCallChunk`, `AgentToolResultChunk`, `AgentRunStatusChunk`

These types are available in the DSL package (`goa.design/goa-ai/dsl`) and include generated conversions to/from runtime types via `ConvertTo/CreateFrom` helpers.

## Key Principles

1. **API Boundary Types**: Use goa-ai types for all service API contracts that expose agent functionality
2. **Runtime Types**: Use `runtime/agent/*` types internally (planner, runtime, hooks)
3. **Conversion Layer**: Use generated `ConvertTo/CreateFrom` helpers at API boundaries
4. **Execution Envelope**: Keep server-only execution metadata separate from tool results (data-only)
5. **Streaming Events**: Use standard streaming chunk types for SSE/progress updates

## Target Services

### Primary Targets
1. **orchestrator** - Main agent orchestration service
2. **chat-agent** - Chat agent service
3. **atlas-data-agent** - Atlas Data Agent service
4. **front** - Frontend service consuming agent APIs
5. **inference-engine** - Model inference service (may have message/telemetry types)

### Secondary Targets
6. **remediation-planning-agent** - Remediation planning agent
7. **diagnostics-agent** - Diagnostics agent
8. **session** - Session management (may reference run types)

## Type Replacement Opportunities

### 1. Orchestrator Service (`services/orchestrator`)

#### Current State Analysis

**Likely Custom Types (to be verified):**
- `RunRequest` / `RunPayload` - custom payload for run/start/resume endpoints
- `RunResponse` / `RunResult` - custom result type for completed runs
- `ChatEvent` / `StreamEvent` - custom streaming event types for Pulse/SSE
- `SessionEvent` - custom event type for session persistence
- Custom message types for conversation history
- Custom error types for tool failures

**File Locations:**
- Design: `services/orchestrator/design/orchestrator.go` (or `services/orchestrator/design/design.go`)
- Implementation: `services/orchestrator/service.go` or `services/orchestrator/orchestrator.go`
- Events: `services/orchestrator/events/*` (if exists)

#### Specific Replacements

**1. Run Endpoint Payload (`services/orchestrator/design/orchestrator.go`):**
```go
// BEFORE (example):
Method("run", func() {
    Payload(func() {
        Attribute("session_id", String)
        Attribute("messages", ArrayOf(ChatMessage))
        Attribute("run_id", String)
        // ... other custom fields
    })
})

// AFTER:
Method("run", func() {
    Payload(AgentRunPayload)  // Contains: agent_id, run_id, session_id, turn_id, messages, labels, metadata
})
```

**2. Run Endpoint Result:**
```go
// BEFORE (example):
Method("run", func() {
    Result(func() {
        Attribute("final_message", ChatMessage)
        Attribute("tool_results", ArrayOf(ToolExecutionResult))
        Attribute("planner_notes", ArrayOf(PlannerNote))
    })
})

// AFTER (non-streaming):
Method("run", func() {
    Result(AgentRunResult)  // Contains: agent_id, run_id, final, tool_events, notes
})

// AFTER (streaming):
Method("run", func() {
    StreamingResult(AgentRunChunk)  // Contains: type, message, tool_call, tool_result, status
    JSONRPC(func() {
        ServerSentEvents(func() {})
    })
})
```

**3. Start/Resume Endpoints:**
```go
// These can also use AgentRunPayload:
Method("start", func() {
    Payload(AgentRunPayload)
    Result(String)  // workflow handle ID
})

Method("resume", func() {
    Payload(func() {
        Attribute("run_id", String, "Run to resume")
        Attribute("messages", ArrayOf(AgentMessage), "Additional messages")
        // Could extend AgentRunPayload or use minimal resume-specific type
    })
})
```

**4. Pulse ChatEvent Replacement:**
```go
// BEFORE: Custom ChatEvent type for Pulse streaming
type ChatEvent struct {
    Type      string      // "message", "tool_call", "tool_result"
    Message   *ChatMessage
    ToolCall  *ToolCallNotification
    ToolResult *ToolResultNotification
}

// AFTER: Use AgentRunChunk directly
// The runtime hook bus emits events that map to AgentRunChunk:
// - AssistantMessage → AgentRunChunk{type: "message", message: "..."}
// - ToolCallScheduledEvent → AgentRunChunk{type: "tool_call", tool_call: {...}}
// - ToolResultReceivedEvent → AgentRunChunk{type: "tool_result", tool_result: {...}}
```

**5. SessionEvent Replacement:**
```go
// BEFORE: Custom SessionEvent type
type SessionEvent struct {
    Type      string
    RunID     string
    SessionID string
    Message   *ChatMessage
    ToolResult *ToolExecutionResult
}

// AFTER: Use AgentMessage and AgentToolEvent directly
// Session service can persist these standard types
// Use AgentRunPayload fields (session_id, run_id, turn_id) for correlation
```

**Actions:**
- [ ] Review `services/orchestrator/design/orchestrator.go`
- [ ] Identify all custom payload/result types
- [ ] Replace `run` method payload with `AgentRunPayload`
- [ ] Replace `run` method result with `AgentRunResult` (non-streaming) or `AgentRunChunk` (streaming)
- [ ] Replace `start` method payload with `AgentRunPayload` (or minimal variant)
- [ ] Replace custom ChatEvent types with `AgentRunChunk` references
- [ ] Update Pulse subscriber to emit `AgentRunChunk` shapes
- [ ] Update service implementation to use generated `ConvertTo/CreateFrom` helpers:
  ```go
  func (s *svc) Run(ctx context.Context, p *orchestrator.AgentRunPayload, stream orchestrator.RunServerStream) error {
      // Convert Goa type → apitypes (generated)
      apiInput := p.ConvertToRunInput()
      
      // Convert apitypes → runtime types
      runtimeInput, err := apitypes.ToRuntimeRunInput(apiInput)
      if err != nil {
          return err
      }
      
      // Execute with runtime
      out, err := s.runtime.Run(ctx, runtimeInput)
      if err != nil {
          return err
      }
      
      // Convert runtime → apitypes
      apiOutput := apitypes.FromRuntimeRunOutput(out)
      
      // Convert apitypes → Goa type (generated)
      result := new(orchestrator.AgentRunResult)
      result.CreateFromRunOutput(apiOutput)
      
      return stream.SendAndClose(ctx, result)
  }
  ```
- [ ] Update tests to use standard types
- [ ] Remove custom type definitions

**Expected Benefits:**
- Consistent API shape across all agent endpoints
- Automatic conversions to runtime types (no manual mapping)
- Less code to maintain (remove custom types)
- Better documentation through standardized types
- Type safety via generated conversions

---

### 2. Chat Agent Service (`services/chat-agent`)

#### Current State Analysis

**Likely Custom Types (based on migration docs):**
- `ChatMessage` / `ConversationMessage` - custom message type in planner/prompts
- `BRTool` / `ToolSet` - custom tool definition types (should use `model.ToolDefinition`)
- `ADAToolResult` - execution envelope type (server-only, should remain separate)
- Custom tool result types in planner outputs
- Custom error/retry types if chat-agent exposes error handling

**File Locations:**
- Design: `services/chat-agent/design/agents.go`, `services/chat-agent/design/design.go`
- Planner: `services/chat-agent/planner/*.go` (when migrated)
- Prompts: `services/chat-agent/prompts/builder.go`
- Service: `services/chat-agent/service.go`

#### Specific Replacements

**1. Planner Message Types (`services/chat-agent/planner/*.go`):**
```go
// BEFORE: Custom ChatMessage type
type ChatMessage struct {
    Role    string
    Content string
    Meta    map[string]any
}

// AFTER: Use planner.AgentMessage (runtime type) internally
// For API boundaries, use AgentMessage from DSL
import "goa.design/goa-ai/runtime/agent/planner"

func PlanStart(ctx context.Context, input planner.PlanInput) (planner.PlanResult, error) {
    messages := make([]planner.AgentMessage, len(input.Messages))
    for i, msg := range input.Messages {
        messages[i] = planner.AgentMessage{
            Role:    msg.Role,
            Content: msg.Content,
            Meta:    msg.Meta,
        }
    }
    // ... planner logic
}
```

**2. Tool Definition Types (`services/chat-agent/prompts/builder.go`):**
```go
// BEFORE: Converting to BRTool (AURA legacy type)
func toolList() []*gentypes.BRTool {
    tools := make([]*gentypes.BRTool, 0)
    for _, s := range chatspec.Specs {
        tools = append(tools, &gentypes.BRTool{
            Name:        s.Name,
            Description: s.Description,
            InputSchema: gentypes.JSON(string(s.Payload.Schema)),
        })
    }
    return tools
}

// AFTER: Use model.ToolDefinition (goa-ai type)
// Create: services/chat-agent/tools/config_from_specs.go
import (
    "goa.design/goa-ai/runtime/agent/model"
    chatspec "github.com/crossnokaye/aura/gen/chat_agent/agents/chat/tool_specs"
)

func ToolDefinitionsFromSpecs() []model.ToolDefinition {
    defs := make([]model.ToolDefinition, 0, len(chatspec.Specs))
    for _, s := range chatspec.Specs {
        var schema any
        if len(s.Payload.Schema) > 0 {
            json.Unmarshal(s.Payload.Schema, &schema)
        } else {
            schema = map[string]any{"type": "object"}
        }
        defs = append(defs, model.ToolDefinition{
            Name:        s.Name,
            Description: s.Description,
            InputSchema: schema,
        })
    }
    return defs
}
```

**3. Planner Result Types (`services/chat-agent/planner/*.go`):**
```go
// BEFORE: Custom tool result types
type ToolExecutionResult struct {
    Name    string
    Result  any
    Error   *ExecutionError
    Summary string
}

// AFTER: Use planner.ToolResult (runtime type)
import "goa.design/goa-ai/runtime/agent/planner"

func PlanResume(ctx context.Context, input planner.PlanInput) (planner.PlanResult, error) {
    // Planner returns planner.ToolResult which includes:
    // - Name, Result, Error, RetryHint, Telemetry
    return planner.PlanResult{
        ToolCalls: []planner.ToolRequest{...},
        FinalResponse: &planner.FinalResponse{
            Message: planner.AgentMessage{...},
        },
        Notes: []planner.PlannerAnnotation{...},
    }, nil
}
```

**4. Streaming Output (`services/chat-agent/planner/*.go`):**
```go
// BEFORE: Custom streaming types
type PlannerStreamEvent struct {
    Type    string
    Content string
    ToolCall *ToolCallNotification
}

// AFTER: Use PlannerContext for data and PlannerEvents for streaming
import (
    "goa.design/goa-ai/runtime/agent/planner"
    "goa.design/goa-ai/runtime/agent/model"
)

func PlanResume(ctx context.Context, input planner.PlanResumeInput) (planner.PlanResult, error) {
    // Obtain a streaming model client
    m, _ := input.Agent.ModelClient("bedrock")
    req := model.Request{ /* ... set system, messages, tools ... */ }
    req.Stream = true

    strm, err := m.Stream(ctx, req)
    if err != nil {
        return planner.PlanResult{}, err
    }
    // Drain provider stream, emit events via input.Events, and build a summary
    sum, err := planner.ConsumeStream(ctx, strm, input.Events)
    if err != nil {
        return planner.PlanResult{}, err
    }
    // Turn stream summary into a plan decision
    if len(sum.ToolCalls) > 0 {
        return planner.PlanResult{ToolCalls: sum.ToolCalls}, nil
    }
    return planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: planner.AgentMessage{Role: "assistant", Content: sum.Text}}}, nil
}
```

**5. Error Handling (`services/chat-agent/planner/*.go`):**
```go
// BEFORE: Custom error types
type PlannerError struct {
    Message string
    Cause   *PlannerError
}

// AFTER: Use planner.ToolError (runtime type)
import "goa.design/goa-ai/runtime/agent/planner"

func PlanResume(ctx context.Context, input planner.PlanInput) (planner.PlanResult, error) {
    // Return tool errors wrapped in ToolResult
    return planner.PlanResult{
        ToolCalls: []planner.ToolRequest{...},
        ToolResults: []planner.ToolResult{
            {
                Name: "tool.name",
                Error: &planner.ToolError{
                    Message: "Tool execution failed",
                    Cause:   &planner.ToolError{Message: "Underlying cause"},
                },
            },
        },
    }, nil
}
```

**Actions:**
- [ ] Review `services/chat-agent/design/agents.go` - verify agent definition
- [ ] Review `services/chat-agent/prompts/builder.go` - replace BRTool with model.ToolDefinition
- [ ] Create `services/chat-agent/tools/config_from_specs.go` helper
- [ ] Update planner (when migrated) to use `planner.AgentMessage`, `planner.ToolResult`, `planner.PlannerAnnotation`
- [ ] Remove custom ChatMessage type if planner uses it
- [ ] Update tests to use standard types
- [ ] Regenerate code: `goa gen github.com/crossnokaye/aura/services/chat-agent/design`

**Expected Benefits:**
- Planner works directly with runtime types (no conversions)
- Consistent tool definitions across all agents
- Standardized error handling
- Better integration with goa-ai runtime
- Type safety throughout the planner flow

---

### 3. Atlas Data Agent Service (`services/atlas-data-agent`)

#### Current State Analysis

**Likely Custom Types (based on migration docs):**
- `ADAToolResult` - execution envelope (server-only, contains code, summary, evidence, atlas_calls, duration, error, facts, data)
- `ToolSet` wrapper around `[]*gentypes.BRTool` - should use `[]model.ToolDefinition`
- Custom tool result types in executors
- Custom error types for tool failures

**File Locations:**
- Design: `services/atlas-data-agent/design/agents.go`
- Service: `services/atlas-data-agent/service.go`
- Executors: `services/atlas-data-agent/tools/exec/*.go` or `services/atlas-data-agent/executors/*.go`
- Prompts: `services/atlas-data-agent/prompts/*.go` (if exists)

#### Specific Replacements

**1. Tool Definition Types (`services/atlas-data-agent/service.go`):**
```go
// BEFORE: buildADAToolSet() returns ToolSet wrapper
func buildADAToolSet() ToolSet {
    tools := make([]*gentypes.BRTool, 0)
    for _, s := range adaspec.Specs {
        tools = append(tools, &gentypes.BRTool{
            Name:        s.Name,
            Description: s.Description,
            InputSchema: gentypes.JSON(string(s.Payload.Schema)),
        })
    }
    return ToolSet{Tools: tools}
}

// AFTER: Create services/atlas-data-agent/tools/config_from_specs.go
import (
    "goa.design/goa-ai/runtime/agent/model"
    adaspec "github.com/crossnokaye/aura/gen/atlas_data_agent/agents/atlas_data_agent/atlas_read/tool_specs"
)

func ToolDefinitionsFromSpecs() []model.ToolDefinition {
    defs := make([]model.ToolDefinition, 0, len(adaspec.Specs))
    for _, s := range adaspec.Specs {
        var schema any
        if len(s.Payload.Schema) > 0 {
            json.Unmarshal(s.Payload.Schema, &schema)
        } else {
            schema = map[string]any{"type": "object"}
        }
        defs = append(defs, model.ToolDefinition{
            Name:        s.Name,
            Description: s.Description,
            InputSchema: schema,
        })
    }
    return defs
}
```

**2. Execution Envelope (Keep Separate):**
```go
// ADAToolResult should remain as server-only execution envelope
// It's NOT a tool result - it's metadata for observability/storage
type ADAToolResult struct {
    Code      string                 // HTTP status code
    Summary   string                 // Human-readable summary
    Data      json.RawMessage        // Tool result data (data-only)
    Evidence  []EvidenceRef          // Server-only evidence refs
    AtlasCalls []AtlasCall           // Server-only AD call tracking
    Duration  time.Duration          // Server-only execution time
    Error     *Error                 // Server-only error details
    Facts     []Fact                 // Server-only facts
}

// Tool result (data-only) should be extracted from ADAToolResult.Data
// This is what planners/LLMs receive via planner.ToolResult
```

**3. Planner Types (when ADA planner migrates):**
```go
// BEFORE: Custom planner types
type ADAPlanResult struct {
    ToolCalls []ToolCallRequest
    Final     *Response
    Notes     []ReasoningStep
}

// AFTER: Use planner.PlanResult (runtime type)
import "goa.design/goa-ai/runtime/agent/planner"

func PlanStart(ctx context.Context, input planner.PlanInput) (planner.PlanResult, error) {
    // ADA planner produces ToolCalls only (no AD RPC inside planner)
    return planner.PlanResult{
        ToolCalls: []planner.ToolRequest{
            {
                ToolName:   "atlas_data_agent.atlas.read.get_alarms",
                Payload:    map[string]any{"site_id": "acme"},
                ToolCallID: "call-123",
            },
        },
    }, nil
}
```

**Actions:**
- [ ] Review `services/atlas-data-agent/design/agents.go` - verify agent definition
- [ ] Review `services/atlas-data-agent/service.go` - replace ToolSet/BRTool with model.ToolDefinition
- [ ] Create `services/atlas-data-agent/tools/config_from_specs.go` helper
- [ ] Verify `ADAToolResult` is server-only execution envelope (not tool result)
- [ ] Update planner (when migrated) to use `planner.PlanResult`, `planner.ToolRequest`
- [ ] Ensure executors return data-only tool results (extract from ADAToolResult.Data)
- [ ] Update tests
- [ ] Regenerate code: `goa gen github.com/crossnokaye/aura/services/atlas-data-agent/design`

**Expected Benefits:**
- Consistent tool definitions with chat-agent
- Clear separation: execution envelope vs tool result (data-only)
- Planner works with standard types
- Better integration with goa-ai runtime

---

### 4. Front Service (`services/front`)

#### Current State
- Likely defines custom types for consuming orchestrator API
- Custom streaming event types
- Custom message types for UI rendering

#### Replacements

**For consuming orchestrator API:**
- Use generated orchestrator types (which should now use goa-ai types)
- Direct use of `AgentRunChunk` for streaming
- Direct use of `AgentMessage` for conversation display

**Actions:**
- [ ] Review `services/front/design/*.go` and implementation
- [ ] Update client code to use generated orchestrator types
- [ ] Replace custom event types with `AgentRunChunk` variants
- [ ] Update UI rendering to use standard message/tool event shapes

**Expected Benefits:**
- Type safety when consuming orchestrator API
- Consistent UI rendering logic
- Easier to test with standard types

---

### 5. Inference Engine Service (`services/inference-engine`)

#### Current State Analysis

**Likely Custom Types (based on migration docs and AU-3 status):**
- `BRTool` / `ToolSet` - custom tool definition types (should use `model.ToolDefinition`)
- Custom message types for model input/output (`LLMMessage`, `ChatMessage`, etc.)
  - `LLMRequest` / `ModelResponse` - custom request/response types
- Custom telemetry types (`ModelTelemetry`, `ExecutionMetrics`)
- Custom error types for model failures

**File Locations:**
- Service: `services/inference-engine/service.go` or `services/inference-engine/inference.go`
- Model client: `services/inference-engine/client/*.go` or `services/inference-engine/bedrock/*.go`
- Types: `services/inference-engine/types/*.go` or `services/inference-engine/model/*.go`

#### Specific Replacements

**1. Tool Definition Types (`services/inference-engine/service.go`):**
```go
// BEFORE: Using BRTool/ToolSet
type LLMRequest struct {
    Messages []ChatMessage
    Tools    []*gentypes.BRTool  // or ToolSet
    // ...
}

// AFTER: Use model.ToolDefinition (goa-ai runtime type)
import "goa.design/goa-ai/runtime/agent/model"

type LLMRequest struct {
    Messages []model.Message
    Tools    []model.ToolDefinition
    // ...
}

// Convert from tool_specs.Specs (done in planner/prompts layer)
// Planners should call ToolDefinitionsFromSpecs() helpers
```

**2. Message Types (`services/inference-engine/service.go`):**
```go
// BEFORE: Custom ChatMessage type
type ChatMessage struct {
    Role    string
    Content string
    Meta    map[string]any
}

// AFTER: Use model.Message (goa-ai runtime type) OR planner.AgentMessage
import "goa.design/goa-ai/runtime/agent/model"

// For model client interface:
type LLMRequest struct {
    Messages []model.Message  // model.Message mirrors planner.AgentMessage
    // ...
}

// Internal conversion from planner.AgentMessage:
func toModelMessages(msgs []planner.AgentMessage) []model.Message {
    result := make([]model.Message, len(msgs))
    for i, msg := range msgs {
        result[i] = model.Message{
            Role:    msg.Role,
            Content: msg.Content,
        }
    }
    return result
}
```

**3. Telemetry Types (`services/inference-engine/service.go`):**
```go
// BEFORE: Custom ModelTelemetry type
type ModelTelemetry struct {
    DurationMs int64
    TokensUsed int
    Model      string
}

// AFTER: Use telemetry.ToolTelemetry (runtime type) or AgentToolTelemetry (API type)
import "goa.design/goa-ai/runtime/agent/telemetry"

// Capture telemetry from model response
func (s *service) CallModel(ctx context.Context, req LLMRequest) (ModelResponse, error) {
    start := time.Now()
    resp, err := s.client.Chat(ctx, req)
    
    // Capture telemetry
    tel := &telemetry.ToolTelemetry{
        DurationMs: time.Since(start).Milliseconds(),
        TokensUsed: resp.Usage.TotalTokens,
        Model:      req.Model,
        Extra:      map[string]any{"request_id": resp.RequestID},
    }
    
    // Return telemetry in response or attach to context
    return ModelResponse{
        Message: resp.Message,
        Telemetry: tel,
    }, err
}
```

**4. Error Types (`services/inference-engine/service.go`):**
```go
// BEFORE: Custom ModelError type
type ModelError struct {
    Code    string
    Message string
    Cause   error
}

// AFTER: Use planner.ToolError (runtime type) for tool failures
import "goa.design/goa-ai/runtime/agent/planner"

// Return errors wrapped in ToolResult
func (s *service) CallModel(ctx context.Context, req LLMRequest) (ModelResponse, error) {
    resp, err := s.client.Chat(ctx, req)
    if err != nil {
        // Wrap in planner.ToolError if needed for tool context
        return ModelResponse{}, &planner.ToolError{
            Message: fmt.Sprintf("Model call failed: %v", err),
            Cause:    planner.ToolErrorFromError(err),
        }
    }
    return resp, nil
}
```

**5. Model Client Interface (`services/inference-engine/client/*.go`):**
```go
// BEFORE: Custom inference engine abstraction
type InferenceEngine interface {
    Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}

// AFTER: Migrate to goa-ai model.Client interface
import "goa.design/goa-ai/runtime/agent/model"

// Use model.Client directly instead of custom abstraction
type Service struct {
    client model.Client  // Bedrock or OpenAI client
}

func (s *Service) Chat(ctx context.Context, req model.Request) (*model.Response, error) {
    return s.client.Chat(ctx, req)
}
```

**6. Tool Result Sanitization (`services/inference-engine/service.go`):**
```go
// BEFORE: Manual sanitization logic scattered
func sanitizeToolResultForModel(tr *ToolExecutionResult) string {
    // Manual JSON scrubbing
}

// AFTER: Use runtime helpers for sanitization
// Tool results from executors should already be data-only
// Runtime handles sanitization for model prompts automatically
// Focus on extracting summary from execution envelope (ADAToolResult)
```

**Actions:**
- [ ] Review `services/inference-engine/service.go` - identify all custom types
- [ ] Replace `BRTool`/`ToolSet` with `model.ToolDefinition`
- [ ] Replace custom message types with `model.Message` or `planner.AgentMessage`
- [ ] Replace custom telemetry types with `telemetry.ToolTelemetry`
- [ ] Replace custom error types with `planner.ToolError` where appropriate
- [ ] Migrate to `model.Client` interface (from `services/inference-engine` abstraction)
- [ ] Update model client implementations (Bedrock/OpenAI) to use `model.Client`
- [ ] Remove manual tool result sanitization (runtime handles it)
- [ ] Update tests
- [ ] Verify model calls work with standard types

**Expected Benefits:**
- Consistent message format across all services
- Standardized telemetry capture
- Better integration with goa-ai runtime
- Removes custom inference engine abstraction layer
- Automatic tool result sanitization

---

### 6. Session Service (`services/session`)

#### Current State Analysis

**Likely Custom Types:**
- `SessionEvent` - custom event type for session persistence
- Custom message storage types
- Custom run metadata types
- Custom types for session timeline/audit

**File Locations:**
- Service: `services/session/service.go`
- Storage: `services/session/store/*.go` or `services/session/persistence/*.go`
- Events: `services/session/events/*.go` (if exists)

#### Specific Replacements

**1. Session Event Types (`services/session/service.go`):**
```go
// BEFORE: Custom SessionEvent type
type SessionEvent struct {
    Type      string
    RunID     string
    SessionID string
    TurnID    string
    Message   *ChatMessage
    ToolResult *ToolExecutionResult
    Timestamp time.Time
}

// AFTER: Use AgentMessage and AgentToolEvent directly
import (
    orchestrator "github.com/crossnokaye/aura/gen/orchestrator"
)

type SessionEvent struct {
    Type      string
    RunID     string
    SessionID string
    TurnID    string
    Message   *orchestrator.AgentMessage      // Use standard type
    ToolEvent *orchestrator.AgentToolEvent    // Use standard type
    Note      *orchestrator.AgentPlannerAnnotation  // Use standard type
    Timestamp time.Time
}

// Persist using standard types
func (s *service) PersistEvent(ctx context.Context, event SessionEvent) error {
    doc := SessionEventDoc{
        Type:      event.Type,
        RunID:     event.RunID,
        SessionID: event.SessionID,
        TurnID:    event.TurnID,
        // Store standard types directly
        Message:   event.Message,
        ToolEvent: event.ToolEvent,
        Note:      event.Note,
        Timestamp: event.Timestamp,
    }
    return s.store.Save(ctx, doc)
}
```

**2. Message Storage (`services/session/store/*.go`):**
```go
// BEFORE: Custom message storage type
type StoredMessage struct {
    Role    string
    Content string
    Meta    map[string]any
    // Storage-specific fields
    ID        string
    SessionID string
    Timestamp time.Time
}

// AFTER: Use AgentMessage + storage fields
import orchestrator "github.com/crossnokaye/aura/gen/orchestrator"

type StoredMessage struct {
    orchestrator.AgentMessage  // Embed standard message
    ID        string            // Storage-specific
    SessionID string            // Storage-specific
    Timestamp time.Time         // Storage-specific
}
```

**3. Run Metadata (`services/session/service.go`):**
```go
// BEFORE: Custom run metadata type
type RunMetadata struct {
    RunID     string
    SessionID string
    TurnID    string
    AgentID   string
    Status    string
}

// AFTER: Use AgentRunPayload fields + session-specific fields
import orchestrator "github.com/crossnokaye/aura/gen/orchestrator"

type RunMetadata struct {
    // Extract from AgentRunPayload
    RunID     string  // From AgentRunPayload.RunID
    SessionID string  // From AgentRunPayload.SessionID
    TurnID    string  // From AgentRunPayload.TurnID
    AgentID   string  // From AgentRunPayload.AgentID
    // Session-specific
    Status    string
    CreatedAt time.Time
    UpdatedAt time.Time
}

// Build from AgentRunPayload
func metadataFromPayload(payload *orchestrator.AgentRunPayload) RunMetadata {
    return RunMetadata{
        RunID:     *payload.RunID,
        SessionID: *payload.SessionID,
        TurnID:    getString(payload.TurnID),
        AgentID:   getString(payload.AgentID),
        Status:    "active",
        CreatedAt: time.Now(),
    }
}
```

**Actions:**
- [ ] Review `services/session/service.go` - identify custom event types
- [ ] Replace `SessionEvent` message/tool fields with `AgentMessage`/`AgentToolEvent`
- [ ] Replace custom message storage types with `AgentMessage` + storage fields
- [ ] Use `AgentRunPayload` fields (session_id, run_id, turn_id) for correlation
- [ ] Update session persistence to store standard types
- [ ] Update session retrieval to return standard types
- [ ] Update tests
- [ ] Verify session timeline uses standard types

**Expected Benefits:**
- Consistent session/run identifier handling
- Standardized message storage format
- Easier to query/correlate with orchestrator events
- Type safety across session and orchestrator

---

## Migration Strategy

### Phase 1: Inventory and Analysis
1. **Audit all AURA service designs**
   - List all custom types in each service
   - Map each to goa-ai equivalent (if exists)
   - Identify gaps or special requirements

2. **Create replacement matrix**
   - Document which types can be directly replaced
   - Document which types need adaptation
   - Document which types should remain custom

### Phase 2: DSL Updates (Low Risk)
1. **Update orchestrator design**
   - Replace payload/result types with goa-ai types
   - Regenerate code
   - Verify compilation

2. **Update agent service designs**
   - Replace agent endpoint types
   - Regenerate code
   - Verify compilation

### Phase 3: Implementation Updates (Medium Risk)
1. **Update service implementations**
   - Use generated `ConvertTo/CreateFrom` helpers
   - Remove manual conversion code
   - Update tests

2. **Update client code**
   - Update front service to use new types
   - Update other consumers
   - Verify end-to-end flows

### Phase 4: Cleanup (Low Risk)
1. **Remove dead code**
   - Delete custom type definitions
   - Remove conversion helpers
   - Update documentation

2. **Verify consistency**
   - Run integration tests
   - Verify all services compile
   - Check for any remaining ad-hoc types

## Detailed Type Mapping

### Run Types

| AURA Custom Type | goa-ai Replacement | Notes |
|-----------------|-------------------|-------|
| `RunRequest` / `RunPayload` | `AgentRunPayload` | Contains messages, session_id, run_id, turn_id, labels, metadata |
| `RunResponse` / `RunResult` | `AgentRunResult` | Contains final message, tool_events, notes |
| `RunStreamChunk` / `StreamEvent` | `AgentRunChunk` | Streaming progress updates |
| Custom start/resume payloads | `AgentRunPayload` | Can reuse same type |

### Message Types

| AURA Custom Type | goa-ai Replacement | Notes |
|-----------------|-------------------|-------|
| `ChatMessage` / `ConversationMessage` | `AgentMessage` | role, content, meta fields |
| `SystemMessage` / `UserMessage` | `AgentMessage` | Use role field to distinguish |
| Custom message metadata | `AgentMessage.meta` | Store in meta map |

### Tool Types

| AURA Custom Type | goa-ai Replacement | Notes |
|-----------------|-------------------|-------|
| `ToolResult` / `ToolExecutionResult` | `AgentToolEvent` | name, result, error, retry_hint, telemetry |
| `ToolError` / `ExecutionError` | `AgentToolError` | message, cause chain |
| `RetryGuidance` / `RetryHint` | `AgentRetryHint` | reason, tool, missing_fields, etc. |
| `ToolTelemetry` / `ExecutionMetrics` | `AgentToolTelemetry` | duration_ms, tokens_used, model, extra |

### Planner Types

| AURA Custom Type | goa-ai Replacement | Notes |
|-----------------|-------------------|-------|
| `PlannerNote` / `ReasoningStep` | `AgentPlannerAnnotation` | text, labels |
| Custom annotation types | `AgentPlannerAnnotation` | Use labels field for categorization |

### Streaming Types

| AURA Custom Type | goa-ai Replacement | Notes |
|-----------------|-------------------|-------|
| `ToolCallNotification` | `AgentToolCallChunk` | id, name, payload |
| `ToolResultNotification` | `AgentToolResultChunk` | id, result, error |
| `StatusUpdate` / `RunStatus` | `AgentRunStatusChunk` | state, message |

## Summary of Type Replacements by Service

### Quick Reference Table

| Service | Custom Types to Replace | goa-ai Replacement | Priority |
|---------|------------------------|-------------------|----------|
| **orchestrator** | `RunRequest`, `RunPayload`, `RunResponse`, `RunResult`, `ChatEvent`, `SessionEvent` | `AgentRunPayload`, `AgentRunResult`, `AgentRunChunk` | High |
| **chat-agent** | `ChatMessage`, `BRTool`, `ToolSet`, `ToolExecutionResult`, `PlannerError` | `planner.AgentMessage`, `model.ToolDefinition`, `planner.ToolResult`, `planner.ToolError` | High |
| **atlas-data-agent** | `ToolSet`, `BRTool`, `ADAPlanResult` | `model.ToolDefinition`, `planner.PlanResult` | High |
| **inference-engine** | `BRTool`, `ToolSet`, `ChatMessage`, `ModelTelemetry`, `ModelError`, `InferenceEngine` interface | `model.ToolDefinition`, `model.Message`, `telemetry.ToolTelemetry`, `planner.ToolError`, `model.Client` | High |
| **front** | `ChatEvent`, `UIMessage`, `UIToolResult`, custom event types | `AgentRunChunk`, `AgentMessage`, `AgentToolEvent` | Medium |
| **session** | `SessionEvent`, `StoredMessage`, `RunMetadata` | `AgentMessage`, `AgentToolEvent`, `AgentRunPayload` fields | Medium |

**Note:** `ADAToolResult` in atlas-data-agent should remain as server-only execution envelope (not replaced).

## Implementation Priority

1. **Phase 1: Core Agent Services** (High Priority)
   - orchestrator (exposes agent APIs)
   - chat-agent (planner types)
   - atlas-data-agent (planner types)
   - inference-engine (model types)

2. **Phase 2: Supporting Services** (Medium Priority)
   - front (consumes orchestrator API)
   - session (persists agent events)

3. **Phase 3: Additional Agents** (Lower Priority)
   - remediation-planning-agent
   - diagnostics-agent
   - knowledge-agent

### Orchestrator Service
- [ ] Review `services/orchestrator/design/orchestrator.go`
- [ ] Replace custom run payload type with `AgentRunPayload`
- [ ] Replace custom run result type with `AgentRunResult`
- [ ] Replace custom streaming type with `AgentRunChunk`
- [ ] Regenerate: `goa gen github.com/crossnokaye/aura/services/orchestrator/design`
- [ ] Update service implementation to use `ConvertTo/CreateFrom`
- [ ] Update tests
- [ ] Verify compilation and runtime behavior

### Chat Agent Service
- [ ] Review `services/chat-agent/design/*.go`
- [ ] Replace custom message types with `AgentMessage` references
- [ ] Replace custom tool result types with `AgentToolEvent` references
- [ ] Regenerate code
- [ ] Update planner implementation
- [ ] Update tests

### Atlas Data Agent Service
- [ ] Review `services/atlas-data-agent/design/*.go`
- [ ] Replace agent endpoint types with goa-ai types
- [ ] Update executors to work with standard types
- [ ] Regenerate code
- [ ] Update tests

### Front Service
- [ ] Review `services/front/design/*.go` and implementation
- [ ] Update to use generated orchestrator types
- [ ] Replace custom event types with `AgentRunChunk`
- [ ] Update UI rendering code
- [ ] Update tests

### Inference Engine Service
- [ ] Review `services/inference-engine/design/*.go`
- [ ] Identify API contract types vs internal types
- [ ] Replace API types with goa-ai equivalents
- [ ] Update model client code
- [ ] Update tests

### Session Service
- [ ] Review `services/session/design/*.go`
- [ ] Identify opportunities to reuse agent types
- [ ] Update types if needed
- [ ] Regenerate code
- [ ] Update tests

## Verification Steps

### Compilation
- [ ] All services compile without errors
- [ ] No type mismatches in generated code
- [ ] Import paths resolve correctly

### Runtime
- [ ] Run a chat turn end-to-end
- [ ] Verify streaming events use correct types
- [ ] Verify error handling works correctly
- [ ] Verify telemetry capture works

### Integration
- [ ] Front service can consume orchestrator API
- [ ] All agent services can invoke tools correctly
- [ ] Session persistence works with new types
- [ ] SSE streaming works correctly

### Tests
- [ ] All unit tests pass
- [ ] Integration tests pass
- [ ] End-to-end tests pass

## Benefits Summary

1. **Consistency**: All agent services use the same types
2. **Less Code**: Remove duplicate type definitions
3. **Type Safety**: Generated conversions ensure correctness
4. **Documentation**: Standard types are well-documented
5. **Maintainability**: Changes to agent types propagate automatically
6. **Interoperability**: Easier integration between services

## Risks and Mitigations

### Risk: Breaking Changes
- **Mitigation**: Update one service at a time, verify tests pass before moving to next

### Risk: Loss of Custom Fields
- **Mitigation**: Use `metadata` or `meta` fields for service-specific extensions

### Risk: Performance Impact
- **Mitigation**: Generated conversions are efficient; profile if needed

### Risk: Migration Complexity
- **Mitigation**: Follow phased approach; maintain backward compatibility during transition if needed

## Next Steps

1. Execute Phase 1 (Inventory) - create detailed type mapping for each service
2. Execute Phase 2 (DSL Updates) - start with orchestrator service
3. Execute Phase 3 (Implementation) - update service code incrementally
4. Execute Phase 4 (Cleanup) - remove old types and verify consistency

## References

- goa-ai DSL types: `goa.design/goa-ai/dsl` package
- goa-ai runtime types: `goa.design/goa-ai/runtime/agent/*` packages
- Generated conversions: `gen/*/convert.go` files
- Example usage: `example/complete/design/orchestrator.go`
