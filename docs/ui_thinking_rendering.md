## UI Guidance: Rendering Structured Thinking and Order

This note describes how UIs should render transcripts that include structured
thinking blocks alongside assistant text, tool_use, and tool_result parts.

- Display assistant-visible text (TextPart) as usual.
- Thinking (ThinkingPart) is not user-facing content. If you expose it (for
  observability/debug), clearly label it as "Reasoning" and:
  - Prefer signed plaintext when Text+Signature are provided.
  - When only Redacted is present, show a compact placeholder (e.g., “Provider
    reasoning (redacted)”) and optionally the content index / final flag for
    traceability.
- Order is sacred. Render the parts in the exact order they were streamed:
  - Assistant: ThinkingPart, then ToolUsePart blocks (one or more), then any
    TextPart.
  - User: ToolResultPart referencing the prior tool_use ID; user text as needed.
- Group multiple messages by role and timestamp but do not reorder parts within
  each message.
- For troubleshooting, surface the tool_use ID and correlate it with the
  tool_result ID in the UI (e.g., hover details).

This ensures the UI mirrors the provider-accurate flow and remains faithful to
the persisted transcript used for subsequent model calls.


