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
	Field(1, "length", Int64, "Total term count.")
	Field(2, "terms", MapOf(String, Int64), "Term frequencies.")
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
	Field(3, "budget_tokens", Int64, "Reasoning token budget.")
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
	Field(1, "document_index", Int64, "Zero-based document index.")
	Field(2, "start", Int64, "Inclusive start character index.")
	Field(3, "end", Int64, "Exclusive end character index.")
	Required("document_index", "start", "end")
})

// DocumentPageLocation locates a page span inside a cited document.
var DocumentPageLocation = Type("DocumentPageLocation", func() {
	Description("Page range inside a cited document.")
	Field(1, "document_index", Int64, "Zero-based document index.")
	Field(2, "start", Int64, "Inclusive start page number.")
	Field(3, "end", Int64, "Inclusive end page number.")
	Required("document_index", "start", "end")
})

// DocumentChunkLocation locates a chunk span inside a cited document.
var DocumentChunkLocation = Type("DocumentChunkLocation", func() {
	Description("Chunk range inside a cited document.")
	Field(1, "document_index", Int64, "Zero-based document index.")
	Field(2, "start", Int64, "Inclusive start chunk index.")
	Field(3, "end", Int64, "Exclusive end chunk index.")
	Required("document_index", "start", "end")
})

// CitationLocation locates cited content inside a source document.
var CitationLocation = Type("CitationLocation", func() {
	Description("Location of a citation within its source.")
	OneOf("span", func() {
		TypeName("CitationSpan")
		Field(1, "document_char", DocumentCharLocation, "Character span location.")
		Field(2, "document_page", DocumentPageLocation, "Page span location.")
		Field(3, "document_chunk", DocumentChunkLocation, "Chunk span location.")
	})
	Required("span")
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

// TextPartDetail carries a text message block.
var TextPartDetail = Type("TextPartDetail", func() {
	Description("Text content block.")
	Field(1, "text", String, "Text content.")
	Required("text")
})

// ThinkingPartDetail carries a provider reasoning block.
var ThinkingPartDetail = Type("ThinkingPartDetail", func() {
	Description("Provider reasoning block.")
	Field(1, "text", String, "Plaintext reasoning content.")
	Field(2, "signature", String, "Provider signature for finalized plaintext thinking.")
	Field(3, "redacted", Bytes, "Redacted reasoning bytes.")
	Field(4, "index", Int64, "Reasoning block sequence index.")
	Field(5, "final", Boolean, "Whether the thinking block is finalized.")
	Required("text", "signature", "index", "final")
})

// ImagePartDetail carries a raw image block.
var ImagePartDetail = Type("ImagePartDetail", func() {
	Description("Raw image content block.")
	Field(1, "format", String, "Image encoding format.")
	Field(2, "bytes", Bytes, "Raw image bytes.")
	Required("format")
})

// ImageSourcePartDetail carries a retained generated image source descriptor.
var ImageSourcePartDetail = Type("ImageSourcePartDetail", func() {
	Description("Retained generated image descriptor.")
	Field(1, "source_kind", String, "Opaque source descriptor kind.")
	Field(2, "data", Bytes, "Raw JSON image source descriptor.")
	Required("source_kind")
})

// DocumentPartDetail carries a document block.
var DocumentPartDetail = Type("DocumentPartDetail", func() {
	Description("Document content block.")
	Field(1, "name", String, "Document identifier.")
	Field(2, "format", String, "Document format or extension.")
	Field(3, "bytes", Bytes, "Raw binary document bytes.")
	Field(4, "text", String, "Plaintext document content.")
	Field(5, "chunks", ArrayOf(String), "Ordered document text chunks.")
	Field(6, "uri", String, "External document URI.")
	Field(7, "context", String, "Document citation context.")
	Field(8, "cite", Boolean, "Whether document citations are requested.")
	Required("name", "format", "text", "uri", "context", "cite")
})

// CitationsPartDetail carries assistant text with source citations.
var CitationsPartDetail = Type("CitationsPartDetail", func() {
	Description("Assistant text with source citations.")
	Field(1, "text", String, "Cited assistant text.")
	Field(2, "citations", ArrayOf(Citation), "Attached citation metadata.")
	Required("text")
})

// ToolUsePartDetail carries an assistant tool invocation block.
var ToolUsePartDetail = Type("ToolUsePartDetail", func() {
	Description("Assistant tool invocation block.")
	Field(1, "id", String, "Tool call identifier.")
	Field(2, "name", String, "Canonical tool identifier.")
	Field(3, "input", Bytes, "Raw JSON tool arguments.")
	Field(4, "thought_signature", String, "Provider reasoning signature.")
	Required("id", "name", "thought_signature")
})

// ToolResultPartDetail carries a correlated tool result block.
var ToolResultPartDetail = Type("ToolResultPartDetail", func() {
	Description("Correlated tool result block.")
	Field(1, "tool_use_id", String, "Correlated tool use identifier.")
	Field(2, "content", Bytes, "JSON-encoded tool result content value.")
	Field(3, "content_present", Boolean, "Whether tool result content was non-nil.")
	Field(4, "blocks_json", Bytes, "MCP-encoded tool content blocks.")
	Field(5, "is_error", Boolean, "Whether the tool result represents an error.")
	Required("tool_use_id", "content_present", "is_error")
})

// CacheCheckpointDetail marks a prompt cache checkpoint boundary.
var CacheCheckpointDetail = Type("CacheCheckpointDetail", func() {
	Description("Prompt cache checkpoint marker.")
})

// MessagePart represents one concrete model.Part variant in a message.
var MessagePart = Type("MessagePart", func() {
	Description("Exclusive sum type of model.Part variants.")
	OneOf("payload", func() {
		TypeName("MessagePartPayload")
		Field(1, "text", TextPartDetail, "Text content block.")
		Field(2, "thinking", ThinkingPartDetail, "Provider reasoning block.")
		Field(3, "image", ImagePartDetail, "Raw image content block.")
		Field(4, "image_source", ImageSourcePartDetail, "Retained generated image descriptor.")
		Field(5, "document", DocumentPartDetail, "Document content block.")
		Field(6, "citations", CitationsPartDetail, "Assistant text with source citations.")
		Field(7, "tool_use", ToolUsePartDetail, "Assistant tool invocation block.")
		Field(8, "tool_result", ToolResultPartDetail, "Correlated tool result block.")
		Field(9, "cache_checkpoint", CacheCheckpointDetail, "Prompt cache checkpoint marker.")
	})
	Required("payload")
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
	Field(8, "max_tokens", Int64, "Maximum output tokens.")
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
	Field(3, "input_tokens", Int64, "Prompt/input tokens consumed.")
	Field(4, "output_tokens", Int64, "Completion/output tokens generated.")
	Field(5, "total_tokens", Int64, "Total tokens consumed.")
	Field(6, "cache_read_tokens", Int64, "Tokens read from prompt cache.")
	Field(7, "cache_write_tokens", Int64, "Tokens written to prompt cache.")
	Required("model", "model_class", "input_tokens", "output_tokens", "total_tokens", "cache_read_tokens", "cache_write_tokens")
})

// ModelResponse is the typed wire representation of model.Response.
var ModelResponse = Type("ModelResponse", func() {
	Description("Provider completion response.")
	Field(1, "content", ArrayOf(ModelMessage), "Ordered assistant messages.")
	Field(2, "usage", TokenUsage, "Token consumption metadata.")
	Field(3, "usage_reported", Boolean, "Whether the provider explicitly reported complete token usage.")
	Field(4, "usage_incomplete", Boolean, "Whether the provider reported only partial token usage.")
	Field(5, "stop_reason", String, "Provider stop reason.")
	Field(6, "output_limited", Boolean, "Whether generation stopped at its token or context limit.")
	Required("usage", "usage_reported", "usage_incomplete", "stop_reason", "output_limited")
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

// StopDetail carries the stop reason and output-limited flag for a stop chunk.
var StopDetail = Type("StopDetail", func() {
	Description("Terminal stop chunk details.")
	Field(1, "reason", String, "Provider stop reason.")
	Field(2, "output_limited", Boolean, "Whether generation ended at its token or context limit.")
	Required("reason", "output_limited")
})

// StreamChunk represents one incremental model.Chunk event.
var StreamChunk = Type("StreamChunk", func() {
	Description("Exclusive sum type of model.Chunk variants.")
	OneOf("payload", func() {
		TypeName("StreamChunkPayload")
		Field(1, "text", ModelMessage, "Message payload for text chunks.")
		Field(2, "thinking", ModelMessage, "Message payload for thinking chunks.")
		Field(3, "tool_call", ToolCall, "Tool invocation for tool_call chunks.")
		Field(4, "tool_call_delta", ToolCallDelta, "Argument fragment for tool_call_delta chunks.")
		Field(5, "completion", Completion, "Structured payload for completion chunks.")
		Field(6, "completion_delta", CompletionDelta, "Fragment for completion_delta chunks.")
		Field(7, "usage", TokenUsage, "Token delta for usage chunks.")
		Field(8, "stop", StopDetail, "Stop details for stop chunks.")
	})
	Required("payload")
})

// TokenCount represents one provider token count result.
var TokenCount = Type("TokenCount", func() {
	Description("Provider input token count.")
	Field(1, "model", String, "Resolved provider model identifier.")
	Field(2, "model_class", String, "Logical model family.")
	Field(3, "input_tokens", Int64, "Counted input tokens.")
	Field(4, "exact", Boolean, "Whether the count is provider-exact.")
	Required("model", "model_class", "input_tokens", "exact")
})

// PlainErrorDetail carries the message text and optional cause for an untyped error node.
var PlainErrorDetail = Type("PlainErrorDetail", func() {
	Description("Untyped error node message and optional wrapped cause index.")
	Field(1, "message", String, "Plain error text.")
	Field(2, "cause_index", Int64, "Index into GatewayError.nodes of the wrapped cause (-1 when none).", func() {
		Minimum(-1)
	})
	Required("message", "cause_index")
})

// RequestValidationErrorDetail carries the wrapped cause index for a model.RequestValidationError node.
var RequestValidationErrorDetail = Type("RequestValidationErrorDetail", func() {
	Description("Application-side request validation error wrapping a cause node.")
	Field(1, "cause_index", Int64, "Index into GatewayError.nodes of the wrapped cause.", func() {
		Minimum(0)
	})
	Required("cause_index")
})

// ProviderErrorDetail encodes model.ProviderError fields.
var ProviderErrorDetail = Type("ProviderErrorDetail", func() {
	Description("Structured provider error details.")
	Field(1, "provider", String, "Provider identifier.", func() {
		MinLength(1)
	})
	Field(2, "operation", String, "Provider operation name.")
	Field(3, "http_status", Int64, "HTTP status code when available.")
	Field(4, "kind", String, "Coarse-grained provider error classification.", func() {
		Enum("auth", "invalid_request", "rate_limited", "unavailable", "unknown")
	})
	Field(5, "code", String, "Provider-specific error code.")
	Field(6, "message", String, "Provider error message.")
	Field(7, "request_id", String, "Provider request identifier.")
	Field(8, "retryable", Boolean, "Whether retrying the call may succeed.")
	Field(9, "cause_index", Int64, "Index into GatewayError.nodes of the wrapped cause (-1 when none).", func() {
		Minimum(-1)
	})
	Required("provider", "operation", "http_status", "kind", "code", "message", "request_id", "retryable", "cause_index")
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
	Field(1, "kind", String, "Closed output-validation category.", func() {
		Enum(
			"response_shape",
			"output_bounds",
			"tool_identity",
			"tool_arguments",
			"tool_choice",
			"structured_output",
			"stream_protocol",
			"usage",
		)
	})
	Field(2, "evidence", ResponseEvidence, "Bounded response fingerprint.")
	Field(3, "usage", TokenUsage, "Validated scalar token usage.")
	Field(4, "recovery_correction", String, "Framework-authored recovery guidance.")
	Field(5, "usage_incomplete", Boolean, "Whether usage represents only partial counts (for example, a stream rejected after initial input token chunks before output tokens arrive, a provider response omitting input or output counters, or a multi-round tool-search failure after an uncounted round).")
	Field(6, "cause_index", Int64, "Index into GatewayError.nodes of the wrapped cause.", func() {
		Minimum(0)
	})
	Required("kind", "evidence", "recovery_correction", "usage_incomplete", "cause_index")
})

// MalformedToolArgsErrorDetail carries the tool name and wrapped cause index for a malformed-tool-arguments marker.
var MalformedToolArgsErrorDetail = Type("MalformedToolArgsErrorDetail", func() {
	Description("Malformed tool arguments error marker and wrapped parsing cause index.")
	Field(1, "tool_name", String, "Canonical tool name for malformed arguments.", func() {
		MinLength(1)
	})
	Field(2, "cause_index", Int64, "Index into GatewayError.nodes of the wrapped parsing cause.", func() {
		Minimum(0)
	})
	Required("tool_name", "cause_index")
})

// JoinedErrorDetail carries the child node indices for a multi-unwrap error node.
var JoinedErrorDetail = Type("JoinedErrorDetail", func() {
	Description("Child node indices for a joined error.")
	Field(1, "child_indices", ArrayOf(Int64, func() {
		Minimum(0)
	}), "Indices into GatewayError.nodes of joined child errors.", func() {
		MinLength(1)
	})
	Required("child_indices")
})

// GatewayErrorNode encodes one node in a structured error tree.
var GatewayErrorNode = Type("GatewayErrorNode", func() {
	Description("Single node in an encoded error tree.")
	OneOf("detail", func() {
		TypeName("GatewayErrorNodeDetail")
		Field(1, "plain", PlainErrorDetail, "Untyped error message and optional wrapped cause.")
		Field(2, "sentinel", String, "Known sentinel error code.", func() {
			Enum(
				"streaming_unsupported",
				"structured_output_unsupported",
				"tool_search_unsupported",
				"tool_content_unsupported",
				"rate_limited",
				"empty_stream",
				"token_counting_unsupported",
				"request_byte_capacity",
				"image_source_capacity",
				"context_canceled",
				"context_deadline_exceeded",
			)
		})
		Field(3, "request_validation", RequestValidationErrorDetail, "Application-side request validation error wrapping a cause node.")
		Field(4, "provider", ProviderErrorDetail, "Structured provider error details and optional wrapped cause.")
		Field(5, "validation", ValidationErrorDetail, "Output validation error details and wrapped cause.")
		Field(6, "unadvertised_tool", String, "Rejected unadvertised tool name.", func() {
			MinLength(1)
		})
		Field(7, "malformed_tool_args", MalformedToolArgsErrorDetail, "Malformed tool arguments marker and wrapped parsing cause.")
		Field(8, "joined", JoinedErrorDetail, "Child node indices for a joined error.")
	})
	Required("detail")
})

// GatewayError encodes a structured error tree and optional retained usage.
var GatewayError = Type("GatewayError", func() {
	Description("Structured gateway error tree preserving provider, validation, sentinel, and retained-usage contracts.")
	Field(1, "nodes", ArrayOf(GatewayErrorNode), "Flattened error tree nodes with root at index 0.", func() {
		MinLength(1)
	})
	Field(2, "retained_usage", TokenUsage, "Validated invocation usage retained on a failed call.")
	Field(3, "retained_usage_incomplete", Boolean, "Whether retained_usage represents only partial counts (for example, a multi-round tool-search invocation that failed on a later round after earlier rounds reported usage, a stream that failed after emitting partial usage chunks, or a provider response that omitted input or output token counters).")
	Required("nodes", "retained_usage_incomplete")
})

// CompleteResult wraps an exclusive unary completion response or structured error.
var CompleteResult = Type("CompleteResult", func() {
	Description("Unary completion result envelope.")
	OneOf("outcome", func() {
		TypeName("CompleteOutcome")
		Field(1, "response", ModelResponse, "Provider response when Complete succeeded.")
		Field(2, "error", GatewayError, "Structured error when Complete failed.")
	})
	Required("outcome")
})

// CountTokensResult wraps an exclusive token count or structured error.
var CountTokensResult = Type("CountTokensResult", func() {
	Description("Token count result envelope.")
	OneOf("outcome", func() {
		TypeName("CountTokensOutcome")
		Field(1, "count", TokenCount, "Provider token count when CountTokens succeeded.")
		Field(2, "error", GatewayError, "Structured error when CountTokens failed.")
	})
	Required("outcome")
})

// StreamFrame wraps one exclusive streaming chunk, terminal response, or structured error.
var StreamFrame = Type("StreamFrame", func() {
	Description("Server-streaming frame carrying an exclusive chunk, terminal response, or structured error.")
	OneOf("payload", func() {
		TypeName("StreamFramePayload")
		Field(1, "chunk", StreamChunk, "Incremental chunk frame.")
		Field(2, "response", ModelResponse, "Terminal provider response frame.")
		Field(3, "error", GatewayError, "Terminal structured error frame.")
	})
	Required("payload")
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
