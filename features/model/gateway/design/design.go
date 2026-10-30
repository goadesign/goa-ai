// Package design defines the typed gRPC transport contract for the goa-ai
// model gateway service.
package design

import (
	. "goa.design/goa/v3/dsl"
)

var _ = API("model_gateway", func() {
	Title("Model Gateway API")
	Description("Typed gRPC transport for unary completions, streaming completions, and exact token counting.")
	Version("1.0")
})

// PromptRef records an exact prompt identifier and version used to build a request.
var PromptRef = Type("PromptRef", func() {
	Description("Exact prompt identifier and version used to construct a model request.")
	Field(1, "id", String, "Stable prompt identifier.")
	Field(2, "version", String, "Content-derived or configured prompt version.")
	Required("id", "version")
})

// SearchDocument carries precomputed search terms for a tool definition.
var SearchDocument = Type("SearchDocument", func() {
	Description("Precomputed search document for a tool definition.")
	Field(1, "length", Int, "Total term count.")
	Field(2, "terms", MapOf(String, Int), "Term frequencies.")
	Required("length")
})

// ToolInputContract carries the raw JSON schema and example documents for a tool.
var ToolInputContract = Type("ToolInputContract", func() {
	Description("Provider-neutral raw JSON projection of a tool input contract.")
	Field(1, "schema", Bytes, "Canonical JSON Schema bytes.")
	Field(2, "schema_without_root_example", Bytes, "Schema without root example annotation.")
	Field(3, "example_json", Bytes, "Canonical authored example JSON.")
})

// ToolDefinition describes one tool exposed to the model.
var ToolDefinition = Type("ToolDefinition", func() {
	Description("Tool definition advertised to the model.")
	Field(1, "name", String, "Canonical tool identifier.")
	Field(2, "search", SearchDocument, "Precomputed search document.")
	Field(3, "deferred", Boolean, "Whether the tool is loaded via tool search.")
	Field(4, "description", String, "Concise tool summary.")
	Field(5, "input", ToolInputContract, "Tool input schema contract.")
	Field(6, "no_arguments", Boolean, "Whether choosing the tool requires no arguments.")
	Required("name", "search", "deferred", "description", "input", "no_arguments")
})

// ToolChoice constrains how the model selects tools.
var ToolChoice = Type("ToolChoice", func() {
	Description("Optional tool selection constraint.")
	Field(1, "mode", String, "Tool choice mode.")
	Field(2, "name", String, "Target tool name when mode is tool.")
	Required("mode", "name")
})

// ThinkingOptions configures provider reasoning behavior.
var ThinkingOptions = Type("ThinkingOptions", func() {
	Description("Provider reasoning configuration.")
	Field(1, "enable", Boolean, "Whether reasoning is enabled.")
	Field(2, "interleaved", Boolean, "Whether reasoning is interleaved with assistant content.")
	Field(3, "budget_tokens", Int, "Reasoning token budget.")
	Required("enable", "interleaved", "budget_tokens")
})

// StructuredOutput constrains the final assistant JSON response.
var StructuredOutput = Type("StructuredOutput", func() {
	Description("Provider-enforced structured output schema contract.")
	Field(1, "schema", Bytes, "Canonical JSON Schema bytes.")
	Field(2, "schema_without_root_example", Bytes, "Schema without root example annotation.")
	Field(3, "example_json", Bytes, "Canonical authored example JSON.")
	Field(4, "name", String, "Provider-facing schema identifier.")
	Field(5, "description", String, "Schema purpose description.")
	Required("name", "description")
})

// CacheOptions configures prompt caching checkpoints.
var CacheOptions = Type("CacheOptions", func() {
	Description("Prompt caching checkpoint configuration.")
	Field(1, "after_system", Boolean, "Place checkpoint after system messages.")
	Field(2, "after_tools", Boolean, "Place checkpoint after tool definitions.")
	Required("after_system", "after_tools")
})

// DocumentCharLocation locates a character span inside a cited document.
var DocumentCharLocation = Type("DocumentCharLocation", func() {
	Description("Character range inside a cited document.")
	Field(1, "document_index", Int, "Zero-based document index.")
	Field(2, "start", Int, "Inclusive start character index.")
	Field(3, "end", Int, "Exclusive end character index.")
	Required("document_index", "start", "end")
})

// DocumentPageLocation locates a page span inside a cited document.
var DocumentPageLocation = Type("DocumentPageLocation", func() {
	Description("Page range inside a cited document.")
	Field(1, "document_index", Int, "Zero-based document index.")
	Field(2, "start", Int, "Inclusive start page number.")
	Field(3, "end", Int, "Inclusive end page number.")
	Required("document_index", "start", "end")
})

// DocumentChunkLocation locates a chunk span inside a cited document.
var DocumentChunkLocation = Type("DocumentChunkLocation", func() {
	Description("Chunk range inside a cited document.")
	Field(1, "document_index", Int, "Zero-based document index.")
	Field(2, "start", Int, "Inclusive start chunk index.")
	Field(3, "end", Int, "Exclusive end chunk index.")
	Required("document_index", "start", "end")
})

// CitationLocation locates cited content inside a source document.
var CitationLocation = Type("CitationLocation", func() {
	Description("Location of a citation within its source.")
	Field(1, "document_char", DocumentCharLocation, "Character span location.")
	Field(2, "document_page", DocumentPageLocation, "Page span location.")
	Field(3, "document_chunk", DocumentChunkLocation, "Chunk span location.")
})

// Citation describes one provider citation attached to assistant text.
var Citation = Type("Citation", func() {
	Description("Provider citation metadata.")
	Field(1, "title", String, "Human-readable source title.")
	Field(2, "source", String, "Provider-neutral source identifier or URI.")
	Field(3, "location", CitationLocation, "Span location within the source.")
	Field(4, "source_content", ArrayOf(String), "Quoted excerpts from the source.")
	Required("title", "source")
})

// MessagePart represents one concrete model.Part variant in a message.
var MessagePart = Type("MessagePart", func() {
	Description("Discriminated union of model.Part variants.")
	Field(1, "kind", String, "Concrete part kind discriminator.")
	Field(2, "text", String, "Text content for text, document, citations, or thinking parts.")
	Field(3, "signature", String, "Provider thinking signature.")
	Field(4, "redacted", Bytes, "Redacted provider thinking bytes.")
	Field(5, "index", Int, "Reasoning block sequence index.")
	Field(6, "final", Boolean, "Whether a thinking part is finalized.")
	Field(7, "image_format", String, "Image encoding format.")
	Field(8, "bytes", Bytes, "Raw image or document bytes.")
	Field(9, "source_kind", String, "Retained generated image descriptor kind.")
	Field(10, "data", Bytes, "Raw JSON image source descriptor.")
	Field(11, "name", String, "Document or tool identifier.")
	Field(12, "document_format", String, "Document format or extension.")
	Field(13, "chunks", ArrayOf(String), "Ordered document text chunks.")
	Field(14, "uri", String, "External document URI.")
	Field(15, "context", String, "Document citation context.")
	Field(16, "cite", Boolean, "Whether document citations are requested.")
	Field(17, "citations", ArrayOf(Citation), "Attached citation metadata.")
	Field(18, "id", String, "Tool call identifier.")
	Field(19, "input", Bytes, "Raw JSON tool arguments.")
	Field(20, "thought_signature", String, "Provider reasoning signature on a tool call.")
	Field(21, "tool_use_id", String, "Correlated tool use identifier on a tool result.")
	Field(22, "content", Bytes, "JSON-encoded tool result content value.")
	Field(23, "content_present", Boolean, "Whether tool result content was non-nil.")
	Field(24, "blocks_json", Bytes, "MCP-encoded tool content blocks.")
	Field(25, "is_error", Boolean, "Whether the tool result represents an error.")
	Required(
		"kind", "text", "signature", "index", "final", "image_format",
		"source_kind", "name", "document_format", "uri", "context", "cite",
		"id", "thought_signature", "tool_use_id", "content_present", "is_error",
	)
})

// ModelMessage represents one conversation message in a transcript or response.
var ModelMessage = Type("ModelMessage", func() {
	Description("Single conversation message.")
	Field(1, "role", String, "Speaker role.")
	Field(2, "parts", ArrayOf(MessagePart), "Ordered message content blocks.")
	Field(3, "meta_json", Bytes, "JSON-encoded dynamic message metadata map.")
	Required("role")
})

// ModelRequest is the typed wire representation of model.Request.
var ModelRequest = Type("ModelRequest", func() {
	Description("Canonical model invocation request.")
	Field(1, "model", String, "Provider-specific model identifier.")
	Field(2, "model_class", String, "Logical model family.")
	Field(3, "prompt_refs", ArrayOf(PromptRef), "Prompt references used to build the request.")
	Field(4, "messages", ArrayOf(ModelMessage), "Ordered conversation transcript.")
	Field(5, "temperature", Float32, "Sampling temperature.")
	Field(6, "tools", ArrayOf(ToolDefinition), "Advertised tool definitions.")
	Field(7, "tool_choice", ToolChoice, "Optional tool selection constraint.")
	Field(8, "max_tokens", Int, "Maximum output tokens.")
	Field(9, "stream", Boolean, "Whether streaming was requested.")
	Field(10, "thinking", ThinkingOptions, "Optional reasoning configuration.")
	Field(11, "structured_output", StructuredOutput, "Optional structured output contract.")
	Field(12, "cache", CacheOptions, "Optional prompt caching configuration.")
	Required("model", "model_class", "temperature", "max_tokens", "stream")
})

// TokenUsage reports token consumption for a model invocation.
var TokenUsage = Type("TokenUsage", func() {
	Description("Token usage counters and model attribution.")
	Field(1, "model", String, "Resolved provider model identifier.")
	Field(2, "model_class", String, "Logical model family.")
	Field(3, "input_tokens", Int, "Prompt/input tokens consumed.")
	Field(4, "output_tokens", Int, "Completion/output tokens generated.")
	Field(5, "total_tokens", Int, "Total tokens consumed.")
	Field(6, "cache_read_tokens", Int, "Tokens read from prompt cache.")
	Field(7, "cache_write_tokens", Int, "Tokens written to prompt cache.")
	Required("model", "model_class", "input_tokens", "output_tokens", "total_tokens", "cache_read_tokens", "cache_write_tokens")
})

// ModelResponse is the typed wire representation of model.Response.
var ModelResponse = Type("ModelResponse", func() {
	Description("Provider completion response.")
	Field(1, "content", ArrayOf(ModelMessage), "Ordered assistant messages.")
	Field(2, "usage", TokenUsage, "Token consumption metadata.")
	Field(3, "usage_reported", Boolean, "Whether the provider explicitly reported token usage.")
	Field(4, "stop_reason", String, "Provider stop reason.")
	Field(5, "output_limited", Boolean, "Whether generation stopped at its token or context limit.")
	Required("usage", "usage_reported", "stop_reason", "output_limited")
})

// ToolCall represents one finalized tool invocation chunk payload.
var ToolCall = Type("ToolCall", func() {
	Description("Finalized tool call invocation.")
	Field(1, "name", String, "Canonical tool identifier.")
	Field(2, "payload", Bytes, "Raw JSON tool arguments.")
	Field(3, "id", String, "Tool call identifier.")
	Field(4, "thought_signature", String, "Provider reasoning signature.")
	Required("name", "id", "thought_signature")
})

// ToolCallDelta represents one incremental tool argument fragment.
var ToolCallDelta = Type("ToolCallDelta", func() {
	Description("Incremental tool call argument fragment.")
	Field(1, "name", String, "Canonical tool identifier.")
	Field(2, "id", String, "Tool call identifier.")
	Field(3, "delta", String, "Raw JSON fragment.")
	Required("name", "id", "delta")
})

// Completion represents one finalized structured output completion payload.
var Completion = Type("Completion", func() {
	Description("Finalized structured completion payload.")
	Field(1, "name", String, "Completion schema name.")
	Field(2, "payload", Bytes, "Canonical JSON completion payload.")
	Required("name")
})

// CompletionDelta represents one incremental structured output fragment.
var CompletionDelta = Type("CompletionDelta", func() {
	Description("Incremental structured completion fragment.")
	Field(1, "name", String, "Completion schema name.")
	Field(2, "delta", String, "Raw JSON fragment.")
	Required("name", "delta")
})

// StreamChunk represents one incremental model.Chunk event.
var StreamChunk = Type("StreamChunk", func() {
	Description("Discriminated union of model.Chunk variants.")
	Field(1, "kind", String, "Concrete chunk kind discriminator.")
	Field(2, "message", ModelMessage, "Message payload for text or thinking chunks.")
	Field(3, "tool_call", ToolCall, "Tool invocation for tool_call chunks.")
	Field(4, "tool_call_delta", ToolCallDelta, "Argument fragment for tool_call_delta chunks.")
	Field(5, "completion", Completion, "Structured payload for completion chunks.")
	Field(6, "completion_delta", CompletionDelta, "Fragment for completion_delta chunks.")
	Field(7, "usage", TokenUsage, "Token delta for usage chunks.")
	Field(8, "stop_reason", String, "Stop reason for stop chunks.")
	Field(9, "output_limited", Boolean, "Output-limited flag for stop chunks.")
	Required("kind", "stop_reason", "output_limited")
})

// TokenCount represents one provider token count result.
var TokenCount = Type("TokenCount", func() {
	Description("Provider input token count.")
	Field(1, "model", String, "Resolved provider model identifier.")
	Field(2, "model_class", String, "Logical model family.")
	Field(3, "input_tokens", Int, "Counted input tokens.")
	Field(4, "exact", Boolean, "Whether the count is provider-exact.")
	Required("model", "model_class", "input_tokens", "exact")
})

// ProviderErrorDetail encodes model.ProviderError fields.
var ProviderErrorDetail = Type("ProviderErrorDetail", func() {
	Description("Structured provider error details.")
	Field(1, "provider", String, "Provider identifier.")
	Field(2, "operation", String, "Provider operation name.")
	Field(3, "http_status", Int, "HTTP status code when available.")
	Field(4, "kind", String, "Coarse-grained provider error classification.")
	Field(5, "code", String, "Provider-specific error code.")
	Field(6, "message", String, "Provider error message.")
	Field(7, "request_id", String, "Provider request identifier.")
	Field(8, "retryable", Boolean, "Whether retrying the call may succeed.")
	Required("provider", "operation", "http_status", "kind", "code", "message", "request_id", "retryable")
})

// ResponseEvidence encodes model.ResponseEvidence fields.
var ResponseEvidence = Type("ResponseEvidence", func() {
	Description("Bounded fingerprint of a rejected provider response.")
	Field(1, "present", Boolean, "Whether a complete response was present.")
	Field(2, "version", String, "Stable digest encoding version.")
	Field(3, "sha256", String, "Lowercase hex SHA-256 digest.")
	Field(4, "size", Int64, "Encoded byte size.")
	Required("present", "version", "sha256", "size")
})

// ValidationErrorDetail encodes model.OutputValidationError fields.
var ValidationErrorDetail = Type("ValidationErrorDetail", func() {
	Description("Structured output validation error details.")
	Field(1, "kind", String, "Closed output-validation category.")
	Field(2, "evidence", ResponseEvidence, "Bounded response fingerprint.")
	Field(3, "usage", TokenUsage, "Validated scalar token usage.")
	Field(4, "recovery_correction", String, "Framework-authored recovery guidance.")
	Required("kind", "evidence", "recovery_correction")
})

// GatewayErrorNode encodes one node in a structured error tree.
var GatewayErrorNode = Type("GatewayErrorNode", func() {
	Description("Single node in an encoded error tree.")
	Field(1, "kind", String, "Error node kind discriminator.")
	Field(2, "message", String, "Plain error text.")
	Field(3, "sentinel", String, "Known sentinel error code.")
	Field(4, "provider", ProviderErrorDetail, "Structured provider error details.")
	Field(5, "validation", ValidationErrorDetail, "Output validation error details.")
	Field(6, "unadvertised_tool_name", String, "Rejected unadvertised tool name.")
	Field(7, "malformed_tool_name", String, "Canonical tool name for malformed arguments.")
	Field(8, "cause_index", Int, "Index into GatewayError.nodes of the wrapped cause (-1 when none).")
	Field(9, "child_indices", ArrayOf(Int), "Indices into GatewayError.nodes of joined child errors.")
	Required("kind", "message", "sentinel", "unadvertised_tool_name", "malformed_tool_name", "cause_index")
})

// GatewayError encodes a structured error tree and optional retained usage.
var GatewayError = Type("GatewayError", func() {
	Description("Structured gateway error tree preserving provider, validation, sentinel, and retained-usage contracts.")
	Field(1, "nodes", ArrayOf(GatewayErrorNode), "Flattened error tree nodes with root at index 0.")
	Field(2, "retained_usage", TokenUsage, "Validated invocation usage retained on a failed call.")
	Required("nodes")
})

// CompleteResult wraps a unary completion response or structured error.
var CompleteResult = Type("CompleteResult", func() {
	Description("Unary completion result envelope.")
	Field(1, "response", ModelResponse, "Provider response when Complete succeeded.")
	Field(2, "error", GatewayError, "Structured error when Complete failed.")
})

// CountTokensResult wraps a token count or structured error.
var CountTokensResult = Type("CountTokensResult", func() {
	Description("Token count result envelope.")
	Field(1, "count", TokenCount, "Provider token count when CountTokens succeeded.")
	Field(2, "error", GatewayError, "Structured error when CountTokens failed.")
})

// StreamFrame wraps one streaming chunk, terminal response, or structured error.
var StreamFrame = Type("StreamFrame", func() {
	Description("Server-streaming frame carrying a chunk, terminal response, or structured error.")
	Field(1, "kind", String, "Frame discriminator: chunk, response, or error.")
	Field(2, "chunk", StreamChunk, "Incremental chunk when kind is chunk.")
	Field(3, "response", ModelResponse, "Terminal provider response when kind is response.")
	Field(4, "error", GatewayError, "Structured error when kind is error.")
	Required("kind")
})

var _ = Service("model_gateway", func() {
	Description("Model gateway service forwarding canonical model requests, responses, stream frames, and token counts.")

	GRPC(func() {
		Package("goa_ai_model_gateway")
	})

	Method("Complete", func() {
		Description("Execute one unary model completion and return the raw provider response or structured error.")
		Payload(ModelRequest)
		Result(CompleteResult)
		GRPC(func() {})
	})

	Method("Stream", func() {
		Description("Execute one streaming model completion and emit incremental chunk frames followed by the terminal response or error frame.")
		Payload(ModelRequest)
		StreamingResult(StreamFrame)
		GRPC(func() {})
	})

	Method("CountTokens", func() {
		Description("Count exact input tokens for one model request when supported by the configured provider.")
		Payload(ModelRequest)
		Result(CountTokensResult)
		GRPC(func() {})
	})
})
