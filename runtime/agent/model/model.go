// Package model defines the provider-agnostic message and streaming types used
// by planners, runtimes, and provider adapters. It models messages as typed
// parts (thinking, text, tool use/results) plus conversation roles.
package model

import (
	"context"
	"encoding/json"
	"errors"

	"goa.design/goa-ai/runtime/agent/tools"
)

// ConversationRole is the role for a message in a conversation.
type ConversationRole string

type (
	// Part is a marker interface implemented by all message parts. Concrete
	// implementations capture user-visible text, provider-issued thinking, and
	// tool call/result content in a strongly typed form.
	Part interface {
		isPart()
	}

	// ImageFormat identifies the on-wire format of an image part.
	//
	// Provider adapters may support only a subset of formats. Callers should
	// normalize uploads to one of the supported formats before constructing an
	// ImagePart.
	ImageFormat string

	// DocumentFormat identifies the on-wire format (extension) of a document part.
	//
	// Provider adapters may support only a subset of formats. Callers should
	// normalize uploads to one of the supported formats before constructing a
	// DocumentPart.
	DocumentFormat string

	// TextPart is a plain text content block in a message.
	//
	// Text is emitted as-is to the UI or consumer when the message is rendered.
	TextPart struct {
		// Text is the human-readable content for this part.
		Text string
	}

	// ImagePart carries image bytes attached to a user message.
	//
	// Image parts are intended for multimodal models. Provider adapters fail fast
	// when images are used in unsupported roles or for unsupported model families.
	ImagePart struct {
		// Format identifies the encoding of Bytes (e.g., "png").
		Format ImageFormat

		// Bytes contains the raw image bytes for the declared format.
		Bytes []byte
	}

	// DocumentPart carries document content attached to a user message.
	//
	// Documents are intended for models that support document inputs and citation
	// generation. Exactly one of Bytes, Text, Chunks, or URI must be provided.
	DocumentPart struct {
		// Name is a short neutral identifier for the document (for example, "spec").
		Name string

		// Format identifies the document format/extension (for example, "pdf", "txt", "md").
		Format DocumentFormat

		// Bytes carries the raw document bytes when the document is provided as an upload.
		Bytes []byte

		// Text carries the document content when the document is provided as a single text blob.
		Text string

		// Chunks carries the document content split into logical chunks when citations
		// should reference chunk indices rather than character spans.
		Chunks []string

		// URI locates the document externally when the document should not be
		// embedded in the request payload (for example, "s3://bucket/key.pdf").
		//
		// Provider adapters fail fast when URI schemes are not supported.
		URI string

		// Context is optional contextual information about how the document should be
		// interpreted by the model when generating citations.
		Context string

		// Cite requests provider-native citations when supported.
		Cite bool
	}

	// CitationsPart is a generated content block paired with citation metadata.
	//
	// Providers may emit this part instead of a TextPart when citation generation
	// is enabled.
	CitationsPart struct {
		// Text is the generated content supported by Citations.
		Text string

		// Citations reference the source documents that informed Text.
		Citations []Citation
	}

	// Citation links generated content back to a specific location in a source document.
	Citation struct {
		// Title is the source document title/identifier when available.
		Title string

		// Source is a provider-specific source identifier when available.
		Source string

		// Location identifies where in the source document the cited content can be found.
		Location CitationLocation

		// SourceContent is the cited excerpt from the source document when provided.
		SourceContent []string
	}

	// CitationLocation identifies where cited content can be found within a document.
	//
	// Exactly one of DocumentChar, DocumentChunk, or DocumentPage should be set when present.
	CitationLocation struct {
		// DocumentChar identifies a character span within a source document.
		DocumentChar *DocumentCharLocation

		// DocumentChunk identifies a range of chunks within a source document.
		DocumentChunk *DocumentChunkLocation

		// DocumentPage identifies a page within a source document.
		DocumentPage *DocumentPageLocation
	}

	// DocumentCharLocation identifies a character span within a document.
	DocumentCharLocation struct {
		DocumentIndex int
		Start         int
		End           int
	}

	// DocumentChunkLocation identifies a chunk range within a document.
	DocumentChunkLocation struct {
		DocumentIndex int
		Start         int
		End           int
	}

	// DocumentPageLocation identifies a page number within a document.
	DocumentPageLocation struct {
		DocumentIndex int
		Start         int
		End           int
	}

	// ThinkingPart represents provider-issued reasoning content.
	//
	// Providers may attach a signature or redacted payload; callers treat this
	// as opaque metadata and surface it according to UI policy.
	ThinkingPart struct {
		// Text is the provider-visible reasoning text when available.
		Text string

		// Signature is the provider-issued signature for Text when present.
		Signature string

		// Redacted carries provider-issued reasoning content in redacted form
		// when plaintext Text is not available.
		Redacted []byte

		// Index is the position of this block in the reasoning sequence.
		Index int

		// Final reports whether this reasoning block is the last one for the
		// current turn.
		Final bool
	}

	// ToolUsePart declares a tool invocation by the assistant.
	//
	// The planner/runtime turns these declarations into concrete tool
	// executions and correlates results via ToolResultPart.ToolUseID.
	ToolUsePart struct {
		// ID uniquely identifies this tool call within the run.
		ID string

		// Name is the tool identifier requested by the model.
		Name string

		// Input is the JSON-compatible arguments object provided by the model.
		Input any
	}

	// ToolResultPart carries a tool result provided by the user side.
	//
	// Tool results are attached to user messages so the model can read them in
	// subsequent turns.
	ToolResultPart struct {
		// ToolUseID correlates this result to a prior tool use declaration.
		ToolUseID string

		// Content is the result payload, typically a JSON-compatible value or
		// string.
		Content any

		// IsError reports whether Content represents an error from the tool.
		IsError bool
	}

	// CacheCheckpointPart marks a cache boundary in a message. Provider adapters
	// translate this to provider-specific caching directives (e.g., Bedrock
	// cachePoint). Providers that do not support caching ignore this part. It is
	// complementary to CacheOptions/CachePolicy: agents can combine explicit
	// CacheCheckpointPart instances with policy-driven AfterSystem/AfterTools
	// checkpoints to express complex caching layouts.
	CacheCheckpointPart struct{}

	// Message is a single chat message.
	//
	// Messages are ordered and grouped into a transcript passed to model
	// clients. Parts preserve structure (text, thinking, tool use, tool
	// result) rather than flattening to plain strings.
	Message struct {
		// Role identifies the speaker for this message.
		Role ConversationRole

		// Parts are the ordered content blocks for the message.
		Parts []Part

		// Meta carries optional provider- or application-specific metadata
		// attached to the message.
		Meta map[string]any
	}

	// ToolDefinition describes a tool exposed to the model.
	//
	// Definitions are derived from Goa tool specifications and include the
	// name, description, and JSON Schema input.
	ToolDefinition struct {
		// Name is the tool identifier as seen by the model.
		Name string

		// Description is a concise summary presented to the model to decide
		// when to call the tool.
		Description string

		// InputSchema is a JSON Schema describing the tool input payload.
		InputSchema any
	}

	// ToolCall is a requested tool invocation from the model.
	//
	// Tool calls capture the tool identity, raw arguments, and an optional
	// provider-issued call identifier.
	ToolCall struct {
		// Name is the tool identifier requested by the model.
		Name tools.Ident

		// Payload is the canonical JSON arguments supplied by the model.
		//
		// Provider adapters MUST populate this as a canonical json.RawMessage;
		// planners and runtimes treat it as opaque JSON and rely on codecs for
		// any schema-aware decoding.
		Payload json.RawMessage

		// ID is an optional provider-issued identifier for the tool call.
		ID string
	}

	// ToolCallDelta is an incremental tool-call payload fragment streamed by
	// providers while they are still constructing the full tool input JSON.
	//
	// Contract:
	//   - This is a best-effort UX signal. Consumers may ignore it entirely.
	//   - The canonical tool payload remains ToolCall.Payload in the final
	//     ChunkTypeToolCall emitted once the provider closes the tool block.
	//   - Delta is not guaranteed to be valid JSON on its own; callers must treat
	//     it as an opaque fragment suitable only for progressive UI previews.
	ToolCallDelta struct {
		// Name is the canonical tool identifier for this delta stream.
		//
		// Provider adapters MUST populate Name for every emitted delta so
		// downstream consumers can render tool-specific previews deterministically.
		Name tools.Ident

		// ID is the provider-issued tool call identifier used to correlate all
		// deltas and the final ToolCall payload.
		ID string

		// Delta is a raw JSON fragment emitted by the provider.
		Delta string
	}

	// ToolChoiceMode controls how the model uses tools for a request.
	//
	// Not all providers support all modes. Provider adapters fail fast when a
	// mode is not supported rather than silently degrading behavior.
	ToolChoiceMode string

	// ToolChoice configures optional tool-use behavior for a Request.
	//
	// When ToolChoice is nil, providers use their default tool behavior
	// (typically auto-selection). When non-nil, providers apply the requested
	// mode or fail fast if the mode is not supported.
	ToolChoice struct {
		// Mode selects the desired tool behavior for the request.
		Mode ToolChoiceMode

		// Name identifies the tool to request when Mode is ToolChoiceModeTool.
		// It must match the Name of one of the tool definitions in Request.Tools.
		Name string
	}

	// TokenUsage tracks token counts for a model call.
	TokenUsage struct {
		// InputTokens is the number of tokens consumed by inputs.
		InputTokens int

		// OutputTokens is the number of tokens produced by outputs.
		OutputTokens int

		// TotalTokens is the total number of tokens consumed by the call.
		TotalTokens int

		// CacheReadTokens is tokens read from cache (reduced cost).
		CacheReadTokens int

		// CacheWriteTokens is tokens written to cache.
		CacheWriteTokens int
	}

	// Request captures inputs for a model invocation.
	Request struct {
		// RunID identifies the logical run for this request when available.
		RunID string

		// Model is the provider-specific model identifier when specified.
		Model string

		// ModelClass selects a model family when Model is not specified.
		ModelClass ModelClass

		// Messages is the ordered transcript provided to the model.
		Messages []*Message

		// Temperature controls sampling when supported by the provider.
		Temperature float32

		// Tools lists the tool definitions available to the model.
		Tools []*ToolDefinition

		// ToolChoice optionally constrains how the model uses tools.
		ToolChoice *ToolChoice

		// MaxTokens caps the number of output tokens when supported.
		MaxTokens int

		// Stream requests streaming responses when true and supported.
		Stream bool

		// Thinking configures provider-specific reasoning behavior.
		Thinking *ThinkingOptions

		// Cache configures prompt caching behavior. Nil means no caching.
		Cache *CacheOptions
	}

	// Response is the result of a non-streaming invocation.
	//
	// Content carries assistant messages; ToolCalls holds any tool invocations
	// requested by the model; Usage and StopReason mirror provider metadata.
	Response struct {
		// Content is the ordered list of assistant messages produced.
		Content []Message

		// ToolCalls lists tool invocations requested by the model.
		ToolCalls []ToolCall

		// Usage reports token consumption for the request.
		Usage TokenUsage

		// StopReason records why generation stopped (provider-specific).
		StopReason string
	}

	// Chunk is a streaming event from the model.
	//
	// Chunks are classified by Type and may carry partial messages, tool calls,
	// usage deltas, or a final stop reason.
	Chunk struct {
		// Type identifies the kind of streaming event.
		Type string

		// Message carries incremental assistant content for text or thinking
		// chunks when present.
		Message *Message

		// Thinking carries incremental reasoning text for providers that surface
		// it out-of-band from Message.
		Thinking string

		// ToolCall carries a single tool invocation when Type is ChunkTypeToolCall.
		ToolCall *ToolCall

		// ToolCallDelta carries an incremental tool-call payload fragment when Type
		// is ChunkTypeToolCallDelta. It is strictly optional and may be ignored.
		ToolCallDelta *ToolCallDelta

		// UsageDelta reports incremental token usage when available.
		UsageDelta *TokenUsage

		// StopReason records why streaming stopped when Type is ChunkTypeStop.
		StopReason string
	}

	// ThinkingOptions configures provider thinking behavior.
	ThinkingOptions struct {
		// Enable turns provider thinking features on when supported.
		Enable bool

		// Interleaved requests interleaved thinking and assistant content when
		// supported.
		Interleaved bool

		// BudgetTokens caps the number of thinking tokens when supported.
		BudgetTokens int
	}

	// CacheOptions configures prompt caching behavior for a request. Provider
	// adapters translate these flags to provider-specific caching directives.
	// Providers that do not support caching ignore these options. When Cache is
	// nil on a Request, the runtime may populate it from the agent RunPolicy
	// (CachePolicy) so callers do not need to thread CacheOptions through every
	// call site. Explicit Request.Cache values always take precedence.
	CacheOptions struct {
		// AfterSystem places a checkpoint after all system messages.
		AfterSystem bool

		// AfterTools places a checkpoint after tool definitions. Not all
		// providers support tool-level checkpoints (e.g., Nova does not).
		AfterTools bool
	}

	// ModelClass identifies the model family.
	//
	// Providers map these classes to concrete model identifiers.
	ModelClass string

	// Client is the provider-agnostic model client.
	//
	// Implementations translate Requests into provider calls and adapt
	// Responses and Chunks back into the generic types used by planners.
	Client interface {
		// Complete performs a non-streaming model invocation.
		Complete(ctx context.Context, req *Request) (*Response, error)

		// Stream performs a streaming model invocation when supported.
		Stream(ctx context.Context, req *Request) (Streamer, error)
	}

	// Streamer delivers incremental model output.
	//
	// Callers must drain the stream until Recv returns io.EOF or another
	// terminal error, then call Close.
	Streamer interface {
		// Recv returns the next streaming chunk or an error.
		Recv() (Chunk, error)

		// Close releases any resources associated with the stream.
		Close() error

		// Metadata carries provider-specific metadata collected during the call.
		Metadata() map[string]any
	}
)

const (
	// ConversationRoleSystem is the role for system messages.
	ConversationRoleSystem ConversationRole = "system"

	// ConversationRoleUser is the role for user messages.
	ConversationRoleUser ConversationRole = "user"

	// ConversationRoleAssistant is the role for assistant messages.
	ConversationRoleAssistant ConversationRole = "assistant"
)

const (
	// ToolChoiceModeAuto lets the provider decide whether to call tools or
	// respond with text. This is the default when ToolChoice is nil.
	ToolChoiceModeAuto ToolChoiceMode = "auto"

	// ToolChoiceModeNone disables tool use for the request when supported by
	// the provider.
	ToolChoiceModeNone ToolChoiceMode = "none"

	// ToolChoiceModeAny forces the model to request at least one tool when
	// supported by the provider.
	ToolChoiceModeAny ToolChoiceMode = "any"

	// ToolChoiceModeTool forces the model to request the specific tool
	// identified by ToolChoice.Name when supported by the provider.
	ToolChoiceModeTool ToolChoiceMode = "tool"
)

const (
	// ChunkTypeText identifies a chunk carrying assistant text.
	ChunkTypeText = "text"

	// ChunkTypeToolCall identifies a chunk carrying a tool invocation.
	ChunkTypeToolCall = "tool_call"

	// ChunkTypeToolCallDelta identifies a chunk carrying an incremental tool-call
	// input JSON fragment.
	//
	// Naming note: this is a *delta* because fragments are not guaranteed to be
	// valid JSON boundaries. It exists solely for progressive UI previews and is
	// safe to ignore; the canonical tool payload is still emitted as
	// ChunkTypeToolCall.
	ChunkTypeToolCallDelta = "tool_call_delta"

	// ChunkTypeThinking identifies a chunk carrying thinking content.
	ChunkTypeThinking = "thinking"

	// ChunkTypeUsage identifies a chunk carrying a usage delta.
	ChunkTypeUsage = "usage"

	// ChunkTypeStop identifies the terminal chunk carrying a stop reason.
	ChunkTypeStop = "stop"
)

const (
	// ImageFormatPNG identifies a PNG-encoded image.
	ImageFormatPNG ImageFormat = "png"

	// ImageFormatJPEG identifies a JPEG-encoded image.
	ImageFormatJPEG ImageFormat = "jpeg"

	// ImageFormatGIF identifies a GIF-encoded image.
	ImageFormatGIF ImageFormat = "gif"

	// ImageFormatWEBP identifies a WebP-encoded image.
	ImageFormatWEBP ImageFormat = "webp"
)

const (
	// DocumentFormatPDF identifies a PDF document.
	DocumentFormatPDF DocumentFormat = "pdf"

	// DocumentFormatCSV identifies a CSV document.
	DocumentFormatCSV DocumentFormat = "csv"

	// DocumentFormatDOC identifies a legacy Microsoft Word document.
	DocumentFormatDOC DocumentFormat = "doc"

	// DocumentFormatDOCX identifies a Microsoft Word document.
	DocumentFormatDOCX DocumentFormat = "docx"

	// DocumentFormatXLS identifies a legacy Microsoft Excel document.
	DocumentFormatXLS DocumentFormat = "xls"

	// DocumentFormatXLSX identifies a Microsoft Excel document.
	DocumentFormatXLSX DocumentFormat = "xlsx"

	// DocumentFormatHTML identifies an HTML document.
	DocumentFormatHTML DocumentFormat = "html"

	// DocumentFormatTXT identifies a plain text document.
	DocumentFormatTXT DocumentFormat = "txt"

	// DocumentFormatMD identifies a Markdown document.
	DocumentFormatMD DocumentFormat = "md"
)

const (
	// ModelClassHighReasoning selects a high-reasoning model family.
	ModelClassHighReasoning ModelClass = "high-reasoning"

	// ModelClassDefault selects the default model family.
	ModelClassDefault ModelClass = "default"

	// ModelClassSmall selects a small/cheap model family.
	ModelClassSmall ModelClass = "small"
)

// ErrStreamingUnsupported indicates the provider does not support streaming.
var ErrStreamingUnsupported = errors.New("model: streaming not supported")

// ErrRateLimited indicates the provider rejected the request due to rate
// limiting after exhausting any configured retries. Callers must not retry
// in a tight loop and should treat this as a transient infrastructure
// failure that is safe to surface to higher layers.
var ErrRateLimited = errors.New("model: rate limited")

func (TextPart) isPart() {}

func (ImagePart) isPart() {}

func (DocumentPart) isPart() {}

func (CitationsPart) isPart() {}

func (ThinkingPart) isPart() {}

func (ToolUsePart) isPart() {}

func (ToolResultPart) isPart() {}

func (CacheCheckpointPart) isPart() {}
