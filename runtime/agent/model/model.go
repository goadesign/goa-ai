// Package model defines the provider-agnostic message and streaming types used
// by planners, runtimes, and provider adapters. It models messages as typed
// parts (thinking, text, tool use/results) plus conversation roles.
package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/big"

	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// ConversationRole is the role for a message in a conversation.
	ConversationRole string

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
		Text string `json:"text"`
	}

	// ImagePart carries image bytes attached to a user message.
	//
	// Image parts are intended for multimodal models. Provider adapters fail fast
	// when images are used in unsupported roles or for unsupported model families.
	ImagePart struct {
		// Format identifies the encoding of Bytes (e.g., "png").
		Format ImageFormat `json:"format"`

		// Bytes contains the raw image bytes for the declared format.
		Bytes []byte `json:"bytes"`
	}

	// DocumentPart carries document content attached to a user message.
	//
	// Documents are intended for models that support document inputs and citation
	// generation. Exactly one of Bytes, Text, Chunks, or URI must be provided.
	DocumentPart struct {
		// Name is a short neutral identifier for the document (for example, "spec").
		Name string `json:"name"`

		// Format identifies the document format/extension (for example, "pdf", "txt", "md").
		Format DocumentFormat `json:"format"`

		// Bytes carries the raw document bytes when the document is provided as an upload.
		Bytes []byte `json:"bytes"`

		// Text carries the document content when the document is provided as a single text blob.
		Text string `json:"text"`

		// Chunks carries the document content split into logical chunks when citations
		// should reference chunk indices rather than character spans.
		Chunks []string `json:"chunks"`

		// URI locates the document externally when the document should not be
		// embedded in the request payload (for example, "s3://bucket/key.pdf").
		//
		// Provider adapters fail fast when URI schemes are not supported.
		URI string `json:"uri"`

		// Context is optional contextual information about how the document should be
		// interpreted by the model when generating citations.
		Context string `json:"context"`

		// Cite requests provider-native citations when supported.
		Cite bool `json:"cite"`
	}

	// CitationsPart is a generated content block paired with citation metadata.
	//
	// Providers may emit this part instead of a TextPart when citation generation
	// is enabled.
	CitationsPart struct {
		// Text is the generated content supported by Citations.
		Text string `json:"text"`

		// Citations reference the source documents that informed Text.
		Citations []Citation `json:"citations"`
	}

	// Citation links generated content back to a specific location in a source document.
	Citation struct {
		// Title is the source document title/identifier when available.
		Title string `json:"title"`

		// Source is a provider-specific source identifier when available.
		Source string `json:"source"`

		// Location identifies where in the source document the cited content can be found.
		Location CitationLocation `json:"location"`

		// SourceContent is the cited excerpt from the source document when provided.
		SourceContent []string `json:"source_content"`
	}

	// CitationLocation identifies where cited content can be found within a document.
	//
	// Exactly one of DocumentChar, DocumentChunk, or DocumentPage should be set when present.
	CitationLocation struct {
		// DocumentChar identifies a character span within a source document.
		DocumentChar *DocumentCharLocation `json:"document_char"`

		// DocumentChunk identifies a range of chunks within a source document.
		DocumentChunk *DocumentChunkLocation `json:"document_chunk"`

		// DocumentPage identifies a page within a source document.
		DocumentPage *DocumentPageLocation `json:"document_page"`
	}

	// DocumentCharLocation identifies a character span within a document.
	DocumentCharLocation struct {
		DocumentIndex int `json:"document_index"`
		Start         int `json:"start"`
		End           int `json:"end"`
	}

	// DocumentChunkLocation identifies a chunk range within a document.
	DocumentChunkLocation struct {
		DocumentIndex int `json:"document_index"`
		Start         int `json:"start"`
		End           int `json:"end"`
	}

	// DocumentPageLocation identifies a page number within a document.
	DocumentPageLocation struct {
		DocumentIndex int `json:"document_index"`
		Start         int `json:"start"`
		End           int `json:"end"`
	}

	// ThinkingPart represents provider-issued reasoning content.
	//
	// Providers may attach a signature or redacted payload; callers treat this
	// as opaque metadata and surface it according to UI policy.
	ThinkingPart struct {
		// Text is the provider-visible reasoning text when available.
		Text string `json:"text"`

		// Signature is the provider-issued signature for Text when present.
		Signature string `json:"signature"`

		// Redacted carries provider-issued reasoning content in redacted form
		// when plaintext Text is not available.
		Redacted []byte `json:"redacted"`

		// Index is the position of this block in the reasoning sequence.
		Index int `json:"index"`

		// Final reports whether this reasoning block is the last one for the
		// current turn.
		Final bool `json:"final"`
	}

	// ToolUsePart declares a tool invocation by the assistant.
	//
	// The planner/runtime turns these declarations into concrete tool
	// executions and correlates results via ToolResultPart.ToolUseID.
	ToolUsePart struct {
		// ID uniquely identifies this tool call within the run.
		ID string `json:"id"`

		// Name is the tool identifier requested by the model.
		Name string `json:"name"`

		// Input is the exact JSON arguments provided by the model.
		Input rawjson.Message `json:"input"`

		// ThoughtSignature is an opaque, provider-defined signature that some
		// providers (for example, Gemini 3) attach to a tool-call part to
		// authenticate the reasoning that produced it. The encoding is
		// provider-specific (the vertex adapter uses standard base64 of the raw
		// signature bytes, matching ThinkingPart.Signature). Empty means absent;
		// providers that do not use it ignore it.
		//
		// Replay obligation: when a provider round-trips a tool call it emitted,
		// runtimes and planners MUST carry this value back unchanged so the
		// provider can validate the continued reasoning chain.
		ThoughtSignature string `json:"thought_signature"`
	}

	// ToolResultPart carries a tool result provided by the user side.
	//
	// Tool results are attached to user messages so the model can read them in
	// subsequent turns.
	ToolResultPart struct {
		// ToolUseID correlates this result to a prior tool use declaration.
		ToolUseID string `json:"tool_use_id"`

		// Content is the provider-facing tool result payload.
		//
		// Contract:
		//   - Successful results carry a decoded JSON-compatible value.
		//   - Failed results carry plain error text with IsError=true.
		//   - Runtime/tooling code must not store raw JSON bytes or Go error
		//     structs here.
		//   - Values may contain nil, booleans, finite numbers, strings,
		//     string-keyed maps, slices, and arrays. Structs, pointers,
		//     functions, channels, unsafe pointers, complex numbers, uintptr
		//     values, reference cycles, more than 64 nesting levels, more than
		//     100,000 visited values, or more than 16 MiB of strings, byte
		//     slices, and map keys are rejected.
		Content any `json:"content"`

		// IsError reports whether Content represents an error from the tool.
		IsError bool `json:"is_error"`
	}

	// CacheCheckpointPart marks a cache boundary in a message. Provider adapters
	// translate this to provider-specific caching directives (for example,
	// Bedrock cachePoint) or reject the request when inline checkpoints are not
	// supported. It is complementary to CacheOptions/CachePolicy: agents can
	// combine explicit checkpoints with policy-driven AfterSystem/AfterTools
	// checkpoints to express complex caching layouts.
	CacheCheckpointPart struct{}

	// Message is a single chat message.
	//
	// Messages are ordered and grouped into a transcript passed to model
	// clients. Parts preserve structure (text, thinking, tool use, tool
	// result) rather than flattening to plain strings.
	Message struct {
		// Role identifies the speaker for this message.
		Role ConversationRole `json:"role"`

		// Parts are the ordered content blocks for the message.
		Parts []Part `json:"parts"`

		// Meta carries optional provider- or application-specific metadata
		// attached to the message. Values may contain nil, booleans, finite
		// numbers, strings, string-keyed maps, slices, and arrays. Structs,
		// pointers, functions, channels, unsafe pointers, complex numbers,
		// uintptr values, reference cycles, more than 64 nesting levels, more
		// than 100,000 visited values, or more than 16 MiB of strings, byte
		// slices, and map keys are rejected.
		Meta map[string]any `json:"meta"`

		origin *messageOrigin
	}

	messageOrigin [1]byte

	// ToolDefinition describes a tool exposed to the model.
	//
	// Definitions are derived from Goa tool specifications and include the name,
	// description, and generated input contract.
	ToolDefinition struct {
		// Name is the tool identifier as seen by the model.
		Name string

		// Description is a concise summary presented to the model to decide
		// when to call the tool.
		Description string

		// Input describes the model-facing tool payload contract.
		Input ToolInput

		// NoArguments reports that choosing the tool is the complete model
		// decision. Adapters may derive the canonical empty object instead of
		// retaining provider-authored argument text.
		NoArguments bool
	}

	// ToolInput contains the model-facing input contract for one tool.
	ToolInput struct {
		jsonSchema               rawjson.Message
		schemaWithoutRootExample rawjson.Message
		exampleJSON              rawjson.Message
		validate                 func(rawjson.Message) error
		fieldJSONTypes           map[string]string
		acceptsNoArguments       bool
	}

	// ToolInputContract is the provider-neutral raw JSON projection of a tool
	// input contract. It is used at process and service boundaries where the
	// unexported ToolInput invariants must be transported without exposing
	// generator internals or provider-specific request fields.
	ToolInputContract struct {
		// Schema is the canonical JSON Schema presented to providers that consume
		// examples embedded in schema annotations.
		Schema rawjson.Message

		// SchemaWithoutRootExample is the same schema without the root example
		// annotation. Provider adapters that expose examples separately may choose
		// this projection.
		SchemaWithoutRootExample rawjson.Message

		// ExampleJSON is the canonical authored JSON input example when the
		// generated tool spec provides one.
		ExampleJSON rawjson.Message
	}

	// ToolCall is a requested tool invocation from the model.
	//
	// Tool calls capture the tool identity, raw arguments, and the opaque call
	// identifier required for result correlation.
	ToolCall struct {
		// Name is the tool identifier requested by the model.
		Name tools.Ident

		// Payload is the provider's tool arguments as valid JSON.
		//
		// An adapter preserves the exact raw bytes when its provider exposes
		// them. When an SDK exposes only a decoded value, the adapter emits one
		// canonical JSON encoding of that value; it cannot promise the provider's
		// original number spelling or whitespace. Planners and runtimes treat the
		// bytes as opaque. The generated tool codec decides semantic validity.
		Payload rawjson.Message

		// ID is the adapter-issued opaque identifier for the tool call. Adapters
		// preserve a provider identifier when available and otherwise generate a
		// unique one. The runtime uses it to correlate tool results and preserve
		// response provenance across planner compilation.
		ID string

		// ThoughtSignature is an opaque, provider-defined signature that some
		// providers (for example, Gemini 3) attach to a tool-call part to
		// authenticate the reasoning that produced it. The encoding is
		// provider-specific (the vertex adapter uses standard base64 of the raw
		// signature bytes, matching ThinkingPart.Signature). Empty means absent;
		// providers that do not use it ignore it.
		//
		// Replay obligation: when a provider round-trips a tool call it emitted,
		// runtimes and planners MUST carry this value back unchanged so the
		// provider can validate the continued reasoning chain.
		ThoughtSignature string
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

		// ID is the adapter-issued tool call identifier used to correlate all
		// deltas and the final ToolCall payload.
		ID string

		// Delta is a raw JSON fragment emitted by the provider.
		Delta string
	}

	// Completion is the canonical structured payload emitted for a streaming
	// typed completion.
	//
	// Contract:
	//   - Provider adapters MUST populate Payload as valid JSON.
	//   - Completion is the only canonical structured payload for a typed
	//     completion stream; callers must not reconstruct the final value from
	//     completion deltas.
	Completion struct {
		// Name is the stable completion identifier associated with this payload.
		Name string

		// Payload is the canonical JSON value for the streamed completion.
		Payload rawjson.Message
	}

	// CompletionDelta is an incremental structured-output fragment streamed while
	// the provider is still constructing the final completion JSON.
	//
	// Contract:
	//   - This is a best-effort UX signal. Consumers may ignore it entirely.
	//   - The canonical completion payload remains Completion.Payload in the final
	//     ChunkTypeCompletion emitted before stream termination.
	//   - Delta is not guaranteed to be valid JSON on its own; callers must treat
	//     it as an opaque fragment suitable only for progressive previews.
	CompletionDelta struct {
		// Name is the stable completion identifier associated with this delta.
		Name string

		// Delta is the raw JSON fragment emitted by the provider.
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

	// StructuredOutput requires one canonical JSON assistant value that conforms
	// to Schema.
	//
	// Provider adapters fail fast when structured output is requested but not
	// supported instead of silently degrading to free-form text. An adapter may
	// still choose among multiple provider-enforced wire mechanisms (for
	// example, a forced tool call in place of a broken native response-format
	// feature) as long as the response it returns is indistinguishable from the
	// caller's point of view: a single canonical JSON payload conforming to
	// Schema.
	StructuredOutput struct {
		// Schema is the required canonical JSON Schema for the assistant response.
		// The validated client compiles it before provider work and applies it to
		// the final response even when the provider also enforces the schema.
		Schema []byte

		// SchemaWithoutRootExample is Schema without its root example annotation.
		// Adapters with a provider-native example field use this projection so the
		// example is not sent twice.
		SchemaWithoutRootExample []byte

		// ExampleJSON is the canonical authored example for the structured response.
		// Adapters forward it through provider-native output or synthetic-tool
		// example fields when supported.
		ExampleJSON rawjson.Message

		// Name is the required nonempty provider-facing schema identifier.
		Name string

		// Description explains the purpose of the structured output to the
		// model. Adapters that must express the schema as a tool (see above)
		// use this as the tool description.
		Description string
	}

	// TokenUsage tracks token counts and model attribution for a single model
	// invocation. The counts are additive; the identity fields (Model,
	// ModelClass) describe the source of the delta and are not aggregated by
	// addTokenUsage.
	TokenUsage struct {
		// Model is the provider-resolved model identifier that produced this
		// usage (e.g., "us.anthropic.claude-sonnet-4-20250514-v1:0"). Set by
		// the model adapter; empty when the adapter does not report it. Provider
		// responses and usage chunks limit this value to 512 bytes.
		Model string

		// ModelClass is the logical model family that was requested (e.g.,
		// "default", "high-reasoning", "small"). Set by the model adapter
		// from the original request.
		ModelClass ModelClass

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

	// TokenCount reports preflight input-token usage for a model request.
	//
	// Exact is true when the provider counted the request with the same
	// tokenization path used for inference. Exact is false for explicit local
	// estimators that approximate provider cost without a native provider call.
	TokenCount struct {
		// Model is the provider-resolved model identifier that would receive this
		// request. It is empty when an estimator cannot resolve the concrete model.
		Model string

		// ModelClass is the logical model family requested by the caller.
		ModelClass ModelClass

		// InputTokens is the number of input tokens counted or estimated.
		InputTokens int

		// Exact reports whether InputTokens came from provider-native counting.
		Exact bool
	}

	// TokenCounter is an optional raw provider capability for preflight token
	// counting. NewClient preserves this capability on the validated client and
	// validates the request before forwarding it. Provider adapters that can
	// count tokens natively should implement it and set TokenCount.Exact to true.
	// Local estimators should implement the same contract with Exact=false so
	// callers can distinguish approximation from provider-authoritative counts.
	//
	// Counts measure the exact request that inference would receive. This includes
	// prior reasoning blocks, tools, structured output, and thinking settings when
	// the provider's counting endpoint accepts those fields.
	TokenCounter interface {
		CountTokens(ctx context.Context, req *Request) (TokenCount, error)
	}

	// TokenEstimator provides an explicit local fallback for clients that cannot
	// count tokens natively. It measures the provider-visible request surface by
	// byte size and converts it with a conservative character-per-token ratio.
	TokenEstimator struct {
		// CharactersPerToken is the approximate byte-to-token conversion ratio.
		// When zero, the estimator uses three characters per token.
		CharactersPerToken int

		// OverheadTokens adds a fixed allowance for provider framing and request
		// fields that are not represented directly in message text.
		// When zero, the estimator uses 500 tokens.
		OverheadTokens int

		// MinimumTokens is the minimum non-zero estimate returned for tiny
		// requests. When zero, the estimator uses 500 tokens.
		MinimumTokens int
	}

	// Request captures inputs for a model invocation.
	Request struct {
		// Model is the provider-specific model identifier when specified.
		Model string

		// ModelClass selects a model family when Model is not specified.
		ModelClass ModelClass

		// PromptRefs records the exact prompts and versions used to build this
		// request. Runtime components and observers use this for provenance and
		// auditing; provider adapters must treat it as metadata and must not
		// translate it to provider wire payloads.
		PromptRefs []prompt.PromptRef

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

		// StructuredOutput constrains the final assistant JSON when supported.
		StructuredOutput *StructuredOutput

		// Cache configures prompt caching behavior. Nil means no caching.
		Cache *CacheOptions

		// completionValidate applies the generated typed completion contract.
		// Provider adapters ignore this in-process validation hook.
		completionValidate func(*Response, *Completion) error

		// preparedContract lets the exact request copy owned by model.Client
		// carry its compiled contract through the raw provider call.
		preparedContract *preparedRequestContract
	}

	// Response is the result of a non-streaming invocation.
	//
	// Content carries the complete ordered provider response, including tool-use
	// parts. Successful responses contain at least one assistant message and a
	// non-empty provider stop reason. OutputLimited reports the one provider stop
	// condition that runtimes must not accept as completed work. Usage mirrors
	// provider metadata.
	Response struct {
		// Content is the ordered list of assistant messages produced.
		Content []Message

		// Usage reports token consumption for the request.
		Usage TokenUsage

		// StopReason records why generation stopped (provider-specific).
		StopReason string

		// OutputLimited reports that the provider stopped because generated
		// output reached its configured token or context limit.
		OutputLimited bool
	}

	// Chunk is a closed progressive presentation event from the model. Concrete
	// variants make invalid field combinations unrepresentable.
	Chunk interface {
		Kind() string
		isChunk()
	}

	// TextChunk carries incremental assistant text.
	TextChunk struct {
		// Message contains one or more ordered TextPart values.
		Message Message
	}

	// ThinkingChunk carries one incremental or finalized reasoning block.
	ThinkingChunk struct {
		// Message contains one or more ordered ThinkingPart values.
		Message Message
	}

	// ToolCallChunk carries one finalized tool invocation.
	ToolCallChunk struct {
		// ToolCall is the provider-authored invocation.
		ToolCall ToolCall
	}

	// ToolCallDeltaChunk carries one progressive tool payload fragment.
	ToolCallDeltaChunk struct {
		// Delta is the provider-authored fragment.
		Delta ToolCallDelta
	}

	// CompletionChunk carries the canonical structured-output payload.
	CompletionChunk struct {
		// Completion is the decoded completion envelope.
		Completion Completion
	}

	// CompletionDeltaChunk carries one structured-output preview fragment.
	CompletionDeltaChunk struct {
		// Delta is the progressive completion fragment.
		Delta CompletionDelta
	}

	// UsageChunk carries one token-usage delta.
	UsageChunk struct {
		// Usage is the provider-attributed token delta.
		Usage TokenUsage
	}

	// StopChunk reports why provider generation stopped.
	StopChunk struct {
		// Reason is the provider-specific stop reason.
		Reason string

		// OutputLimited reports that the provider stopped because generated
		// output reached its configured token or context limit.
		OutputLimited bool
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

	// Provider translates canonical requests into raw provider operations. It is
	// deliberately callable for provider adapters, middleware, and gateways.
	// Its responses and streams have not crossed the canonical output validation
	// boundary. APIs that require canonical model output accept an opaque Client
	// returned by NewClient instead. Every operation must stop promptly after
	// ctx is canceled. A returned Streamer must also unblock Recv and permit
	// Close after that cancellation so activity shutdown cannot retain provider
	// work.
	Provider interface {
		// Complete returns one raw provider response without exposing incremental
		// chunks and returns promptly when ctx is canceled. The provider adapter
		// owns the wire transport used to assemble that response.
		Complete(ctx context.Context, req *Request) (*Response, error)

		// Stream performs a raw streaming provider invocation when supported.
		// Implementations return a non-nil stream exactly when err is nil and
		// make its Recv return promptly when ctx is canceled.
		Stream(ctx context.Context, req *Request) (Streamer, error)
	}

	// Client is an opaque validated model client. Only package model constructs
	// implementations, so every response and stream crosses the same canonical
	// validation boundary before consumer code can observe it.
	Client interface {
		// Complete returns one validated model response without exposing
		// incremental chunks.
		Complete(ctx context.Context, req *Request) (*Response, error)

		// Stream performs a validated streaming model invocation.
		Stream(ctx context.Context, req *Request) (*ValidatedStream, error)

		// CountTokens validates the provider input before invoking the optional
		// raw TokenCounter capability. It returns
		// ErrTokenCountingUnsupported when the provider has no native counter.
		CountTokens(ctx context.Context, req *Request) (TokenCount, error)

		validatedModelClient()
	}

	// ResponseEvidence is bounded evidence captured from the exact complete
	// provider response before ownership copying or validation.
	ResponseEvidence struct {
		// Present reports whether the provider returned a complete response.
		Present bool
		// Version identifies the stable encoding covered by SHA256. It is empty
		// when the response was absent or could not be encoded.
		Version string
		// SHA256 is the lowercase hexadecimal digest of the bounded response
		// encoding. It is empty when the response could not be encoded.
		SHA256 string
		// Size is the number of bytes covered by SHA256.
		Size int64
	}

	// StreamObservation contains safe copies and bounded evidence from one
	// validated stream receive operation.
	StreamObservation struct {
		// Chunk is a validated copy returned to the stream caller.
		Chunk Chunk
		// RejectedUsageDelta contains valid counts from one rejected usage chunk.
		RejectedUsageDelta *TokenUsage
		// RejectedUsageTotal contains the invocation total copied from a rejected
		// complete response.
		RejectedUsageTotal *TokenUsage
		// Response is a safe copy of the complete response at EOF or rejection.
		Response *Response
		// ResponseEvidence identifies the raw complete response before copying.
		ResponseEvidence ResponseEvidence
		// Err is the receive result observed by the stream caller.
		Err error
	}

	// Streamer delivers incremental model output.
	//
	// Callers must drain the stream until Recv returns the literal io.EOF value
	// or another terminal error, read Response after clean EOF, then call Close.
	// Wrapping io.EOF changes it into a provider failure.
	// Recv, Response, and Close must not be called concurrently.
	// A provider transfers ownership of each returned Chunk before Recv returns
	// and of the complete Response before returning io.EOF. It must not mutate
	// either value afterward.
	Streamer interface {
		// Recv returns the next streaming chunk or an error.
		Recv() (Chunk, error)

		// Response returns the canonical provider response after Recv reports
		// io.EOF. It returns nil before clean stream completion.
		Response() *Response

		// Close releases any resources associated with the stream.
		Close() error
	}
)

// CountTokens estimates req's input-token usage with Exact=false. It is intended
// for explicit fallback paths such as rate limiting or non-native providers, not
// for provider-specific billing or hard context-window guarantees.
func (e TokenEstimator) CountTokens(ctx context.Context, req *Request) (TokenCount, error) {
	if err := ctx.Err(); err != nil {
		return TokenCount{}, err
	}
	return TokenCount{
		Model:       reqModel(req),
		ModelClass:  reqModelClass(req),
		InputTokens: e.estimate(req),
		Exact:       false,
	}, nil
}

func (e TokenEstimator) estimate(req *Request) int {
	charCount := requestCharacterCount(req)
	if charCount <= 0 {
		return e.minimumTokens()
	}
	tokens := max(charCount/e.charactersPerToken(), 1)
	return tokens + e.overheadTokens()
}

func (e TokenEstimator) charactersPerToken() int {
	if e.CharactersPerToken <= 0 {
		return 3
	}
	return e.CharactersPerToken
}

func (e TokenEstimator) overheadTokens() int {
	if e.OverheadTokens <= 0 {
		return 500
	}
	return e.OverheadTokens
}

func (e TokenEstimator) minimumTokens() int {
	if e.MinimumTokens <= 0 {
		return 500
	}
	return e.MinimumTokens
}

const (
	jsonObjectType = "object"

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

	// ChunkTypeCompletionDelta identifies a chunk carrying an incremental typed
	// completion JSON fragment.
	//
	// Naming note: this is a *delta* because fragments are not guaranteed to be
	// valid JSON boundaries. It exists solely for progressive previews and is
	// safe to ignore; the canonical completion payload is emitted as
	// ChunkTypeCompletion.
	ChunkTypeCompletionDelta = "completion_delta"

	// ChunkTypeCompletion identifies a chunk carrying the canonical typed
	// completion payload for a structured-output stream.
	ChunkTypeCompletion = "completion"

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

// Kind identifies a text presentation event.
func (TextChunk) Kind() string {
	return ChunkTypeText
}

func (TextChunk) isChunk() {
}

// Kind identifies a thinking presentation event.
func (ThinkingChunk) Kind() string {
	return ChunkTypeThinking
}

func (ThinkingChunk) isChunk() {
}

// Kind identifies a finalized tool-call event.
func (ToolCallChunk) Kind() string {
	return ChunkTypeToolCall
}

func (ToolCallChunk) isChunk() {
}

// Kind identifies a progressive tool-call event.
func (ToolCallDeltaChunk) Kind() string {
	return ChunkTypeToolCallDelta
}

func (ToolCallDeltaChunk) isChunk() {
}

// Kind identifies a canonical structured-completion event.
func (CompletionChunk) Kind() string {
	return ChunkTypeCompletion
}

func (CompletionChunk) isChunk() {
}

// Kind identifies a progressive structured-completion event.
func (CompletionDeltaChunk) Kind() string {
	return ChunkTypeCompletionDelta
}

func (CompletionDeltaChunk) isChunk() {
}

// Kind identifies a token-usage event.
func (UsageChunk) Kind() string {
	return ChunkTypeUsage
}

func (UsageChunk) isChunk() {
}

// Kind identifies a stop event.
func (StopChunk) Kind() string {
	return ChunkTypeStop
}

func (StopChunk) isChunk() {
}

// ErrStreamingUnsupported indicates the provider does not support streaming.
var ErrStreamingUnsupported = errors.New("model: streaming not supported")

// ErrStructuredOutputUnsupported indicates the provider does not support
// provider-enforced structured output for the requested model API.
var ErrStructuredOutputUnsupported = errors.New("model: structured output not supported")

// ErrRateLimited indicates the provider rejected the request due to rate
// limiting after exhausting any configured retries. Callers must not retry
// in a tight loop and should treat this as a transient infrastructure
// failure that is safe to surface to higher layers.
var ErrRateLimited = errors.New("model: rate limited")

// ErrEmptyStream indicates the provider terminated a streaming response
// without ever starting an assistant message, so no output was produced.
// Providers intermittently do this when a model emits an empty completion
// (observed on Bedrock as messageStop with no prior messageStart). Adapters
// classify the condition with NewEmptyStreamError instead of fabricating an
// empty response; callers detect it with errors.Is and may retry the request
// a bounded number of times before surfacing the failure.
var ErrEmptyStream = errors.New("model: provider returned an empty stream")

// ToolCalls derives model tool invocations from their ordered ToolUsePart
// entries. Response.Content remains the single source of provider output.
func (r *Response) ToolCalls() []ToolCall {
	var calls []ToolCall
	for _, message := range r.Content {
		for _, part := range message.Parts {
			use, ok := part.(ToolUsePart)
			if !ok {
				continue
			}
			calls = append(calls, ToolCall{
				Name:             tools.Ident(use.Name),
				Payload:          append(rawjson.Message(nil), use.Input...),
				ID:               use.ID,
				ThoughtSignature: use.ThoughtSignature,
			})
		}
	}
	return calls
}

// ToolDefinitionFromSpec converts a generated Goa tool specification into the
// model-facing contract advertised to providers and retains its payload decoder
// for response validation. Generated schema bytes and codecs are compile-time
// artifacts; invalid JSON or a missing decoder is an invariant violation and
// panics.
func ToolDefinitionFromSpec(spec tools.ToolSpec) *ToolDefinition {
	if spec.Payload.Codec.FromJSON == nil {
		panic(fmt.Sprintf("model: generated tool %q payload decoder is required", spec.Name))
	}
	input := advertisedToolInputFromSpec(spec.Payload)
	input.validate = func(payload rawjson.Message) error {
		_, err := spec.Payload.Codec.FromJSON(payload)
		return err
	}
	input.fieldJSONTypes = maps.Clone(spec.Payload.FieldJSONTypes)
	input.acceptsNoArguments = typeSpecDeclaresNoArguments(spec.Payload)
	return &ToolDefinition{
		Name:        spec.Name.String(),
		Description: spec.Description,
		Input:       input,
	}
}

// typeSpecDeclaresNoArguments reads generated field metadata instead of
// reparsing its schema. A model-facing empty object has only the root marker.
func typeSpecDeclaresNoArguments(spec tools.TypeSpec) bool {
	return len(spec.FieldJSONTypes) == 1 && spec.FieldJSONTypes["$payload"] == jsonObjectType
}

// AdvertisedToolInputFromSchema builds the model-facing contract for a
// caller-authored tool. It compiles the schema immediately and validates every
// returned payload before model output is exposed. Generated tools should use
// ToolDefinitionFromSpec so their generated decoder remains the exact contract.
func AdvertisedToolInputFromSchema(schema rawjson.Message) (ToolInput, error) {
	validated, err := validateContractJSON("caller-authored", "input schema", schema)
	if err != nil {
		return ToolInput{}, err
	}
	validate, err := compileToolSchemaValidator(validated)
	if err != nil {
		return ToolInput{}, fmt.Errorf("model: caller-authored input schema: %w", err)
	}
	return ToolInput{
		jsonSchema:         validated,
		validate:           validate,
		acceptsNoArguments: schemaDeclaresNoArguments(validated),
	}, nil
}

// advertisedToolInputFromSpec projects generated documents for
// ToolDefinitionFromSpec, which attaches the generated decoder before exposing
// the completed definition.
func advertisedToolInputFromSpec(spec tools.TypeSpec) ToolInput {
	return ToolInput{
		jsonSchema:               validateGeneratedJSON(spec.Name, "payload schema", spec.Schema),
		schemaWithoutRootExample: validateGeneratedJSON(spec.Name, "schema without root example", spec.SchemaWithoutRootExample),
		exampleJSON:              validateGeneratedJSON(spec.Name, "example JSON", spec.ExampleJSON),
	}
}

// ToolInputFromContract reconstructs a ToolInput from a provider-neutral
// transport contract. Boundary code uses this after decoding JSON from another
// process; generated-code callers should prefer ToolDefinitionFromSpec.
func ToolInputFromContract(toolName string, contract ToolInputContract) (ToolInput, error) {
	if contract.Schema == nil {
		return ToolInput{}, fmt.Errorf("model: tool %q input schema is required", toolName)
	}
	if len(bytes.TrimSpace(contract.ExampleJSON)) > 0 && contract.SchemaWithoutRootExample == nil {
		return ToolInput{}, fmt.Errorf("model: tool %q example JSON requires schema without root example", toolName)
	}
	schema, err := validateContractJSON(toolName, "input schema", contract.Schema)
	if err != nil {
		return ToolInput{}, err
	}
	schemaWithoutRootExample, err := validateContractJSON(toolName, "schema without root example", contract.SchemaWithoutRootExample)
	if err != nil {
		return ToolInput{}, err
	}
	if len(schemaWithoutRootExample) > 0 {
		if _, err := compileToolSchemaValidator(schemaWithoutRootExample); err != nil {
			return ToolInput{}, fmt.Errorf("model: tool %q schema without root example: %w", toolName, err)
		}
		if err := validateSchemaWithoutRootExample(schema, schemaWithoutRootExample); err != nil {
			return ToolInput{}, fmt.Errorf("model: tool %q schema without root example: %w", toolName, err)
		}
	}
	exampleJSON, err := validateContractJSON(toolName, "example JSON", contract.ExampleJSON)
	if err != nil {
		return ToolInput{}, err
	}
	validate, err := compileToolSchemaValidator(schema)
	if err != nil {
		return ToolInput{}, fmt.Errorf("model: tool %q input schema: %w", toolName, err)
	}
	return ToolInput{
		jsonSchema:               schema,
		schemaWithoutRootExample: schemaWithoutRootExample,
		exampleJSON:              exampleJSON,
		validate:                 validate,
		acceptsNoArguments:       schemaDeclaresNoArguments(schema),
	}, nil
}

// schemaDeclaresNoArguments reports whether an external schema declares an
// object with no named model-authored fields.
func schemaDeclaresNoArguments(schema rawjson.Message) bool {
	var root toolSchemaArguments
	if err := decodeToolSchemaRoot(schema, &root); err != nil {
		return false
	}
	var declared string
	if err := json.Unmarshal(root.Type, &declared); err != nil || declared != jsonObjectType {
		return false
	}
	if len(root.Properties) == 0 {
		return true
	}
	var fields map[string]json.RawMessage
	return json.Unmarshal(root.Properties, &fields) == nil && fields != nil && len(fields) == 0
}

// validateSchemaWithoutRootExample proves that the alternate provider schema
// differs only by removing root example annotations. It compares raw top-level
// members and never turns nested schema documents into generic Go values.
func validateSchemaWithoutRootExample(schema, alternate rawjson.Message) error {
	canonicalObject, err := decodeSchemaRootMembers(schema)
	if err != nil {
		return err
	}
	alternateObject, err := decodeSchemaRootMembers(alternate)
	if err != nil {
		return err
	}
	delete(canonicalObject, "example")
	delete(canonicalObject, "examples")
	if !maps.EqualFunc(canonicalObject, alternateObject, equalRawJSON) {
		return errors.New("alternate schema changes fields other than root examples")
	}
	return nil
}

// decodeSchemaRootMembers retains each top-level schema member as raw JSON so
// root-only contract checks do not decode or rewrite nested schema values.
func decodeSchemaRootMembers(schema rawjson.Message) (map[string]json.RawMessage, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(schema, &members); err != nil {
		return nil, err
	}
	if members == nil {
		return nil, errors.New("schema projection must be an object")
	}
	return members, nil
}

// equalRawJSON compares two retained JSON values without exposing a generic
// decoded schema. Objects ignore member order, arrays preserve element order,
// scalar values compare by meaning, and malformed input returns false.
func equalRawJSON(left, right json.RawMessage) bool {
	left = bytes.TrimSpace(left)
	right = bytes.TrimSpace(right)
	if !json.Valid(left) || !json.Valid(right) || len(left) == 0 || len(right) == 0 {
		return false
	}
	kind := rawJSONKind(left[0])
	if kind != rawJSONKind(right[0]) {
		return false
	}
	switch kind {
	case '{':
		var leftObject, rightObject map[string]json.RawMessage
		if json.Unmarshal(left, &leftObject) != nil || json.Unmarshal(right, &rightObject) != nil {
			return false
		}
		return maps.EqualFunc(leftObject, rightObject, equalRawJSON)
	case '[':
		var leftArray, rightArray []json.RawMessage
		if json.Unmarshal(left, &leftArray) != nil || json.Unmarshal(right, &rightArray) != nil {
			return false
		}
		if len(leftArray) != len(rightArray) {
			return false
		}
		for index := range leftArray {
			if !equalRawJSON(leftArray[index], rightArray[index]) {
				return false
			}
		}
		return true
	case '"':
		var leftString, rightString string
		if json.Unmarshal(left, &leftString) != nil || json.Unmarshal(right, &rightString) != nil {
			return false
		}
		return leftString == rightString
	case 'b', 'n':
		return bytes.Equal(left, right)
	default:
		var leftNumber, rightNumber big.Rat
		if _, ok := leftNumber.SetString(string(left)); !ok {
			return false
		}
		if _, ok := rightNumber.SetString(string(right)); !ok {
			return false
		}
		return leftNumber.Cmp(&rightNumber) == 0
	}
}

// rawJSONKind groups every legal number spelling and both boolean values into
// their JSON value kinds so semantic comparison does not depend on first bytes.
func rawJSONKind(first byte) byte {
	switch first {
	case '{', '[', '"', 'n':
		return first
	case 't', 'f':
		return 'b'
	default:
		return '#'
	}
}

// AcceptsEmptyObject reports whether the tool's exact payload validator accepts
// the canonical empty JSON object. Provider adapters use this when a model
// selects a tool but sends no argument bytes; required-field tools remain
// invalid, while no-field and all-optional payloads retain their generated
// defaults.
func (definition *ToolDefinition) AcceptsEmptyObject() bool {
	return definition.Input.validate(rawjson.Message(`{}`)) == nil
}

// Contract returns the provider-neutral transport projection of the tool input.
// The returned values are raw generated JSON documents owned by the caller.
func (in ToolInput) Contract() ToolInputContract {
	return ToolInputContract{
		Schema:                   cloneRawJSON(in.jsonSchema),
		SchemaWithoutRootExample: cloneRawJSON(in.schemaWithoutRootExample),
		ExampleJSON:              cloneRawJSON(in.exampleJSON),
	}
}

func reqModel(req *Request) string {
	if req == nil {
		return ""
	}
	return req.Model
}

func reqModelClass(req *Request) ModelClass {
	if req == nil {
		return ""
	}
	return req.ModelClass
}

func requestCharacterCount(req *Request) int {
	if req == nil {
		return 0
	}
	count := 0
	for _, msg := range req.Messages {
		count += messageCharacterCount(msg)
	}
	for _, tool := range req.Tools {
		if tool == nil {
			continue
		}
		count += len(tool.Name)
		count += len(tool.Description)
		// Providers send one projection of the input contract per request:
		// either the annotated schema, or the schema without its root example
		// plus the separate example. Charge the larger of the two so the
		// estimate stays conservative without summing renderings no provider
		// combines.
		input := tool.Input.Contract()
		annotated := len(input.Schema)
		split := len(input.SchemaWithoutRootExample) + len(input.ExampleJSON)
		count += max(annotated, split)
	}
	if req.ToolChoice != nil {
		count += len(req.ToolChoice.Mode)
		count += len(req.ToolChoice.Name)
	}
	if req.StructuredOutput != nil {
		count += len(req.StructuredOutput.Name)
		count += len(req.StructuredOutput.Description)
		annotated := len(req.StructuredOutput.Schema)
		split := len(req.StructuredOutput.SchemaWithoutRootExample) + len(req.StructuredOutput.ExampleJSON)
		count += max(annotated, split)
	}
	return count
}

func messageCharacterCount(msg *Message) int {
	if msg == nil {
		return 0
	}
	count := len(msg.Role)
	for _, part := range msg.Parts {
		count += partCharacterCount(part)
	}
	return count
}

func partCharacterCount(part Part) int {
	switch v := part.(type) {
	case TextPart:
		return len(v.Text)
	case ImagePart:
		return len(v.Bytes) + len(v.Format)
	case DocumentPart:
		count := len(v.Name) + len(v.Format) + len(v.Bytes) + len(v.Text) + len(v.URI) + len(v.Context)
		for _, chunk := range v.Chunks {
			count += len(chunk)
		}
		return count
	case CitationsPart:
		count := len(v.Text)
		for _, citation := range v.Citations {
			count += len(citation.Title) + len(citation.Source)
			for _, text := range citation.SourceContent {
				count += len(text)
			}
		}
		return count
	case ThinkingPart:
		return len(v.Text) + len(v.Signature) + len(v.Redacted)
	case ToolUsePart:
		return len(v.ID) + len(v.Name) + encodedCharacterCount(v.Input)
	case ToolResultPart:
		return len(v.ToolUseID) + encodedCharacterCount(v.Content)
	case CacheCheckpointPart:
		return 0
	default:
		return 0
	}
}

func encodedCharacterCount(value any) int {
	switch v := value.(type) {
	case nil:
		return 0
	case string:
		return len(v)
	case []byte:
		return len(v)
	case rawjson.Message:
		return len(v)
	case json.RawMessage:
		return len(v)
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return len(fmt.Sprint(v))
		}
		return len(data)
	}
}

func (TextPart) isPart() {}

func (ImagePart) isPart() {}

func (DocumentPart) isPart() {}

func (CitationsPart) isPart() {}

func (ThinkingPart) isPart() {}

func (ToolUsePart) isPart() {}

func (ToolResultPart) isPart() {}

func (CacheCheckpointPart) isPart() {}

func validateGeneratedJSON(name, label string, data rawjson.Message) rawjson.Message {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if !json.Valid(data) {
		panic(fmt.Errorf("model: invalid generated %s for %s", label, name))
	}
	return cloneRawJSON(data)
}

func validateContractJSON(toolName, label string, data rawjson.Message) (rawjson.Message, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("model: invalid %s for tool %q", label, toolName)
	}
	return cloneRawJSON(data), nil
}

func cloneRawJSON(data rawjson.Message) rawjson.Message {
	if data == nil {
		return nil
	}
	out := make(rawjson.Message, len(data))
	copy(out, data)
	return out
}
