package model

import (
	"context"
	"errors"

	"goa.design/goa-ai/runtime/agent/tools"
)

// ConversationRole is the role for a message in a conversation.
type ConversationRole string

const (
	ConversationRoleSystem    ConversationRole = "system"
	ConversationRoleUser      ConversationRole = "user"
	ConversationRoleAssistant ConversationRole = "assistant"
)

// Part is a marker interface implemented by all message parts.
type Part interface{ isPart() }

// TextPart is a plain text content block.
type TextPart struct {
	Text string
}

func (TextPart) isPart() {}

// ThinkingPart represents provider-issued reasoning content.
type ThinkingPart struct {
	Text      string
	Signature string
	Redacted  []byte
	Index     int
	Final     bool
}

func (ThinkingPart) isPart() {}

// ToolUsePart declares a tool invocation by the assistant.
type ToolUsePart struct {
	ID    string
	Name  string
	Input any
}

func (ToolUsePart) isPart() {}

// ToolResultPart carries a tool result provided by the user side to the model.
type ToolResultPart struct {
	ToolUseID string
	Content   any
	IsError   bool
}

func (ToolResultPart) isPart() {}

// Message is a single chat message.
type Message struct {
	Role  ConversationRole
	Parts []Part
	Meta  map[string]any
}

// ToolDefinition describes a tool exposed to the model.
type ToolDefinition struct {
	Name        string
	Description string
	InputSchema any
}

// ToolCall is a requested tool invocation from the model.
type ToolCall struct {
	Name    tools.Ident
	Payload any
	ID      string
}

// TokenUsage tracks token counts.
type TokenUsage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

// ResponseFormat constrains output formatting.
type ResponseFormat struct {
	JSONSchema []byte
	SchemaName string
	JSONOnly   bool
}

// Request captures inputs for a model invocation.
type Request struct {
	RunID          string
	Model          string
	ModelClass     ModelClass
	Messages       []*Message
	Temperature    float32
	Tools          []*ToolDefinition
	MaxTokens      int
	Stream         bool
	Thinking       *ThinkingOptions
	ResponseFormat *ResponseFormat
}

// Response is the result of a non-streaming invocation.
type Response struct {
	Content    []Message
	ToolCalls  []ToolCall
	Usage      TokenUsage
	StopReason string
}

// Chunk is a streaming event from the model.
type Chunk struct {
	Type       string
	Message    *Message
	Thinking   string
	ToolCall   *ToolCall
	UsageDelta *TokenUsage
	StopReason string
}

const (
	ChunkTypeText     = "text"
	ChunkTypeToolCall = "tool_call"
	ChunkTypeThinking = "thinking"
	ChunkTypeUsage    = "usage"
	ChunkTypeStop     = "stop"
)

// ThinkingOptions configures provider thinking behavior.
type ThinkingOptions struct {
	Enable       bool
	Interleaved  bool
	BudgetTokens int
}

// ModelClass identifies the model family.
type ModelClass string

const (
	ModelClassHighReasoning ModelClass = "high-reasoning"
	ModelClassDefault       ModelClass = "default"
	ModelClassSmall         ModelClass = "small"
)

// Client is the provider-agnostic model client.
type Client interface {
	Complete(ctx context.Context, req Request) (Response, error)
	Stream(ctx context.Context, req Request) (Streamer, error)
}

// Streamer delivers incremental model output.
type Streamer interface {
	Recv() (Chunk, error)
	Close() error
	Metadata() map[string]any
}

// ErrStreamingUnsupported indicates the provider does not support streaming.
var ErrStreamingUnsupported = errors.New("model: streaming not supported")

