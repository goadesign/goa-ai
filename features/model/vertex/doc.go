// Package vertex provides Google Cloud Vertex AI model clients for goa-ai:
// a Gemini adapter implementing model.Client and model.TokenCounter, and a
// Claude-on-Vertex constructor that reuses features/model/anthropic through
// the Anthropic SDK's vertex transport.
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
package vertex
