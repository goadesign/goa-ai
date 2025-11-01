// Package model provides interfaces for LLM clients used by planners.
// It defines a provider-agnostic abstraction over chat completion APIs
// (OpenAI, Bedrock, Anthropic, etc.) so planners can invoke models without
// coupling to specific SDKs. Implementations translate these normalized types
// into provider-specific formats.
package model

import (
	"context"
	"errors"
)

type (
	// Client defines the contract planners use to invoke LLM calls. Implementations
	// wrap provider SDKs (OpenAI, Bedrock, etc.) and translate Request/Response
	// to provider-specific formats. Clients should be thread-safe and reusable
	// across multiple planner invocations.
	Client interface {
		// Complete sends a chat completion request to the model provider and returns
		// the generated response. Returns an error if the model is unavailable, quota
		// is exceeded, or the request is malformed. Implementations should handle
		// retries and rate limiting according to provider best practices.
		Complete(ctx context.Context, req Request) (Response, error)

		// Stream sends a chat completion request and returns a Streamer that yields
		// incremental chunks (text, tool requests, usage deltas). Implementations
		// should translate provider-specific streaming APIs into Chunk events. The
		// returned Streamer must be closed by callers. Providers that do not support
		// streaming should return ErrStreamingUnsupported.
		Stream(ctx context.Context, req Request) (Streamer, error)
	}

	// Streamer delivers incremental model output. Successive calls to Recv return
	// Chunk values until io.EOF. Implementations must be safe to call from a single
	// goroutine and release any underlying resources when Close is invoked.
	Streamer interface {
		// Recv returns the next chunk from the stream.
		Recv() (Chunk, error)
		// Close closes the stream.
		Close() error
		// Metadata returns the metadata for the stream.
		Metadata() map[string]any
	}

	// Request captures the normalized parameters for a model invocation. Fields map
	// to common provider parameters (OpenAI, Anthropic, Bedrock) but may not be
	// supported by all backends. Implementations should document unsupported fields
	// and either return errors or apply sensible defaults.
	Request struct {
		// Model identifies the target model using the provider-specific identifier
		// (e.g., "gpt-4", "claude-3-5-sonnet-20241022", "anthropic.claude-v2").
		Model string

		// Messages is the ordered chat history provided to the model, including
		// system prompts, user inputs, and prior assistant responses. The order
		// matters for context window and model understanding.
		Messages []Message

		// Temperature controls sampling temperature (typically 0.0 to 2.0). Lower
		// values produce more deterministic outputs; higher values increase randomness.
		// Zero means greedy decoding (always pick the most likely token).
		Temperature float32

		// Tools describes the tool schemas exposed to the model for function calling.
		// Empty if the model should not invoke tools. Not all models support tool
		// calling; implementations should return an error or ignore unsupported tools.
		Tools []ToolDefinition

		// MaxTokens caps the number of completion tokens the model can generate.
		// Zero typically means use the provider's default (which may vary by model).
		// Some providers treat this as a soft limit, others as a hard cap.
		MaxTokens int

		// Stream indicates whether the caller prefers streaming output. When true,
		// planners should call Client.Stream to receive incremental chunks. Providers
		// may ignore this flag if streaming is unsupported.
		Stream bool

		// Thinking configures provider-specific “thinking” modes for models that
		// support reflective chains (e.g., Bedrock Claude sensemaking). Nil disables
		// thinking and uses provider defaults.
		Thinking *ThinkingOptions
	}

	// Response wraps the generated content and any tool call suggestions from the
	// model provider. Planners use this to extract assistant messages and tool
	// invocations for the next turn.
	Response struct {
		// Content contains the assistant messages returned by the model. Typically
		// a single message, but streaming or multi-turn models may return multiple.
		// Empty if the model only requested tool calls without generating text.
		Content []Message

		// ToolCalls lists any tool invocations requested by the model. Empty if the
		// model produced a final text response. Planners execute these tools and
		// feed results back in subsequent turns.
		ToolCalls []ToolCall

		// Usage reports token usage when available. Some providers don't report this
		// for streaming responses or certain models; check InputTokens > 0 to confirm
		// availability.
		Usage TokenUsage

		// StopReason explains why the model stopped generating. Common values:
		// "stop" (natural end), "length" (hit max tokens), "tool_calls" (requested tools),
		// "content_filter" (content policy violation). Provider-specific; may be empty.
		StopReason string
	}

	// Message mirrors an LLM chat message with role and content. Messages form the
	// conversation history sent to and received from the model.
	Message struct {
		// Role indicates the message role: "user" (end-user input), "assistant"
		// (model response), "system" (instruction/context), or provider-specific roles
		// like "tool" (for tool results in some APIs).
		Role string

		// Content is the message text. For user/system messages, this is the input
		// prompt. For assistant messages, this is the generated text. May be empty
		// if the message is a tool call request or tool result with no text.
		Content string

		// Meta carries provider-specific metadata like message IDs, timestamps,
		// or additional structured content. Planners typically ignore this; it's
		// preserved for debugging or provider-specific features.
		Meta map[string]any
	}

	// ToolDefinition describes a tool schema passed to model providers for function
	// calling. The model uses the name and description to decide when to invoke the
	// tool, and the schema to generate valid arguments.
	ToolDefinition struct {
		// Name is the identifier presented to the model (e.g., "search", "calculate").
		// Should be concise and descriptive. Some providers restrict allowed characters
		// (alphanumeric + underscores) or length.
		Name string

		// Description documents the tool for prompting purposes. The model uses this
		// to understand when and how to invoke the tool. Should be clear and include
		// usage examples or constraints.
		Description string

		// InputSchema is the JSON Schema (draft 7+) describing the tool's input
		// parameters. Typically a map[string]any representing a JSON Schema object
		// with "type": "object", "properties", and "required" fields. The model
		// generates tool arguments conforming to this schema.
		InputSchema any
	}

	// ToolCall captures a tool invocation requested by the model provider during
	// function calling. Planners extract these from Response.ToolCalls and execute
	// the corresponding tools.
	ToolCall struct {
		// Name identifies which tool should be invoked (must match a ToolDefinition.Name
		// from the request).
		Name string

		// Payload carries the JSON arguments requested by the model, typically as a
		// map[string]any or struct. The shape conforms to the InputSchema provided
		// in the tool definition. Planners deserialize this into tool-specific types.
		Payload any
	}

	// Chunk represents a streaming event emitted by the model. Only one of Message,
	// ToolCall, or Usage will be populated depending on Type.
	Chunk struct {
		Type       ChunkType
		Message    Message
		ToolCall   ToolCall
		UsageDelta TokenUsage
		StopReason string
	}

	// ChunkType identifies the chunk payload type.
	ChunkType string

	// ThinkingOptions toggles provider-specific thinking modes.
	ThinkingOptions struct {
		Enable        bool
		BudgetTokens  int
		DisableReason string
	}

	// TokenUsage records prompt/completion token counts when provided by the model
	// provider. Useful for cost tracking and quota management. All fields are zero
	// if the provider doesn't report usage (e.g., some streaming responses).
	TokenUsage struct {
		// InputTokens counts tokens consumed by the input prompt and message history.
		// Includes system prompts, user messages, and prior assistant responses.
		InputTokens int

		// OutputTokens counts tokens produced by the model in this completion.
		// Includes generated text and tool call arguments.
		OutputTokens int

		// TotalTokens reports the aggregate tokens consumed (InputTokens + OutputTokens).
		// Some providers compute this differently (e.g., including overhead), so prefer
		// this field when available instead of summing Input + Output.
		TotalTokens int
	}
)

// Chunk type constants.
const (
	ChunkTypeText     ChunkType = "text"
	ChunkTypeToolCall ChunkType = "tool_call"
	ChunkTypeThinking ChunkType = "thinking"
	ChunkTypeUsage    ChunkType = "usage"
	ChunkTypeStop     ChunkType = "stop"
)

// ErrStreamingUnsupported indicates the model provider does not implement
// streaming for the requested model/parameters.
var ErrStreamingUnsupported = errors.New("model: streaming not supported")
