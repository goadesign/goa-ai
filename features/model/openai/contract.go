// Package openai preserves the provider-neutral model.Client contract at the
// package boundary.
//
// Contract checklist:
//   - Unary calls preserve assistant text, tool calls, tool-call IDs, usage, and
//     stop reasons.
//   - Streaming emits provider-neutral text, tool-call delta/final, usage, and
//     stop chunks.
//   - Requests are stateless at the adapter boundary: callers must provide the
//     full provider-ready transcript in-order, and missing history fails fast at
//     the owned runtime boundary instead of being heuristically rehydrated.
//   - Transcript encoding round-trips assistant tool_use and user tool_result
//     messages when the assistant turn is representable by OpenAI's
//     single-message shape; unrepresentable assistant interleaving fails fast,
//     and tool-result errors remain explicit.
//   - Provider-visible tool names stay reversible through deterministic
//     sanitization, while goa-ai keeps using canonical dotted tool identifiers.
//   - Model-class routing stays inside the adapter; planners continue selecting
//     logical model families instead of raw provider IDs.
//   - Structured output is provider-enforced when requested, but it cannot be
//     combined with tools.
//   - Cache-bearing requests and explicit cache checkpoints fail fast; the
//     adapter does not silently drop unsupported cache semantics.
//   - Thinking only supports the representable subset: enable + configured
//     reasoning effort. Budgeted or interleaved thinking requests fail fast
//     instead of being heuristically remapped.
package openai
