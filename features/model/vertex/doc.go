// Package vertex provides Google Cloud Vertex AI model clients for goa-ai:
// a Gemini adapter implementing model.Client and model.TokenCounter, and a
// Claude-on-Vertex constructor that is pure construction — it builds an
// Anthropic SDK client against the SDK's Vertex transport and hands it to
// features/model/anthropic, which owns both the Messages translation and
// the HTTP-status provider-error classification for every Anthropic-hosted
// adapter (direct API and Vertex-hosted alike). The constructor (see
// anthropic.go) adds no translation or error-mapping code of its own.
//
// Adapter contract (mirrors features/model/openai/contract.go):
//
//   - Stateless: every call receives the full provider-ready transcript.
//   - Model-class routing happens inside the adapter via Options
//     (DefaultModel/HighModel/SmallModel); explicit Request.Model wins.
//   - Tool names are sanitized reversibly; tool calls for names not
//     advertised this request surface as-is so the runtime can produce an
//     "unknown tool" result.
//   - PromptRefs are provenance metadata and are never sent on the wire.
//   - No internal retries. Throttling surfaces as
//     errors.Join(model.ErrRateLimited, *model.ProviderError); other
//     failures as *model.ProviderError with kind/status/retryable set.
//   - Unsupported feature combinations fail fast (e.g. Gemini structured
//     output cannot be combined with tool definitions).
//   - No silent fallbacks for states this adapter's own contract cannot
//     legally produce: an invalid-base64 ThinkingPart.Signature or
//     ToolUsePart.ThoughtSignature, a non-object tool input, and a
//     ToolResultPart with no matching ToolUsePart in the transcript are all
//     invariant violations and return precise errors instead of a
//     best-effort coercion.
//   - Gemini 3-class models attach thought signatures to functionCall parts
//     (not just to thought parts); this adapter round-trips them through
//     model.ToolCall.ThoughtSignature / model.ToolUsePart.ThoughtSignature
//     using the same base64 convention as ThinkingPart.Signature.
package vertex
