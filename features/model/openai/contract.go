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
//     Every request explicitly asks for encrypted reasoning content alongside
//     store:false, even when reasoning is left to the provider default. Returned
//     reasoning metadata is preserved for the caller's next transcript.
//   - Transcript encoding round-trips assistant tool_use and user tool_result
//     messages when the assistant turn is representable by OpenAI's
//     single-message shape; unrepresentable assistant interleaving fails fast,
//     and tool-result errors remain explicit.
//   - Provider-visible tool names stay reversible through deterministic
//     sanitization, while goa-ai keeps using canonical dotted tool identifiers.
//   - Model-class routing stays inside the adapter; planners continue selecting
//     logical model families instead of raw provider IDs.
//   - Direct OpenAI uses strict projected schemas. NewBedrock preserves full
//     tool schemas with strict:false; canonical output validation still rejects
//     invalid arguments without repairing them.
//   - Direct OpenAI structured output is provider-enforced when requested, but
//     it cannot be combined with tools. NewBedrock rejects structured output
//     with model.ErrStructuredOutputUnsupported before inference.
//   - Cache-bearing requests and explicit cache checkpoints fail fast; the
//     adapter does not silently drop unsupported cache semantics.
//   - Enabled thinking uses the configured ThinkingEffort. Disabled thinking
//     sends DisabledThinkingEffort only when configured; absent thinking always
//     leaves the effort unspecified. These options apply to every model routed
//     through the client; unsupported model settings remain provider errors.
//     Enabled budgeted or interleaved thinking requests fail fast instead of
//     being heuristically remapped.
//   - Neither Responses constructor implements token counting. The validated
//     client returns model.ErrTokenCountingUnsupported, never a guessed count.
package openai
