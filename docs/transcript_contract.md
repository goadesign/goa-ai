## Transcript Contract (goa-ai runtime)

This document specifies the contract for providing the full conversation transcript
to goa-ai model clients (e.g., Bedrock) for re-encoding into provider payloads.
The transcript is authoritative: goa-ai does not synthesize or normalize beyond
provider-required name sanitization.

### High-level model
- The application (e.g., aura) persists every event (thinking, assistant text,
  assistant tool_use with id and args, user tool_result with tool_use_id and content)
  in exact order for the run.
- Before each model call, the application sends the entire run transcript to goa-ai
  as `[]*model.Message`, preserving order. The last element is the new delta
  (user text or user tool_result).
- Goa-ai re-encodes these messages into provider-specific formats in the same order,
  preserving structure. No tool_use injection, no thinking toggles.
- Streaming: goa-ai forwards provider chunks in real time (thinking, assistant text).
  Tool use is surfaced only when complete (on content block stop).

### Allowed parts
Each `model.Message` contains ordered `Parts` that are one of:
- `model.ThinkingPart`: either signed plaintext (Text+Signature) or Redacted bytes
- `model.TextPart`: assistant or user visible text
- `model.ToolUsePart`: assistant-declared tool_use (ID, Name, Input)
- `model.ToolResultPart`: user tool_result correlated via ToolUseID (Content any/string)

Order is significant and must reflect the actual stream sequence.

### Bedrock-specific invariants
- When an assistant message contains tool_use, Bedrock requires that message
  to begin with a reasoning block (thinking). Provide `model.ThinkingPart`
  before `model.ToolUsePart` in that assistant message.
- User tool_result must reference a prior assistant tool_use via ToolUseID.
- Tool names may need sanitization (`[a-zA-Z0-9_-]+`). Goa-ai performs a reversible
  name mapping inside the Bedrock adapter only (adapter-local concern).

### Example
Assistant tool call with interleaved thinking (simplified):
1. Assistant:
   - ThinkingPart(Text+Signature)
   - ToolUsePart(ID="tu_1", Name="search_assets", Input={"q":"pump #42"})
2. User:
   - ToolResultPart(ToolUseID="tu_1", Content={"assets":[...]} )
3. Assistant:
   - ThinkingPart(Redacted)
   - TextPart("Top matches are...")

### Application responsibilities
- Provide the full transcript for the entire run in `req.Messages` every call.
- Ensure ordering is faithful to the stream that was persisted.
- Place `ThinkingPart` before `ToolUsePart` within an assistant message that
  declares tools (if the provider requires it).
- Include `ToolUsePart.ID` and correlate `ToolResultPart.ToolUseID`.

### Goa-ai responsibilities
- Re-encode only: map `model.Message` parts to provider blocks in order.
- Maintain a reversible tool name map within the provider adapter.
- Stream provider events to the application in real time.


