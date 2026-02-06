package tools

// runtime_internal.go defines canonical tool identifiers reserved for the goa-ai
// runtime itself.
//
// Contract:
// - These identifiers are stable and may appear in provider transcripts.
// - They are always safe to advertise to models because their semantics are
//   runtime-owned (no external side effects).

// ToolUnavailable is a runtime-owned tool used to represent model tool calls
// whose requested tool name is not registered for the run.
//
// Provider adapters and runtimes rewrite unknown tool calls to this identifier
// to preserve a valid tool_use → tool_result handshake even when models
// hallucinate tool names. The tool returns a structured error and a retry hint
// instructing the model to select from the advertised tool list.
const ToolUnavailable Ident = "runtime.tool_unavailable"
