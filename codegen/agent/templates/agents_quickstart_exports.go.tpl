---

## 7. Agents Calling Agents (The `Exports` Keyword)

When an agent `Exports` a toolset, other agents can call it. Goa-AI generates a special `agenttools` package to make this easy.

```go
// In your main.go, register the exported toolset so others can find it.
// <agenttools>.ToolsetName contains the exact registration route.
reg, err := <agenttools>.NewRegistration(
    <provider>.Definition(),
    "You are a helpful specialist assistant.",  // A system prompt for the nested agent (optional)
    // Configure per-tool content (optional). If omitted, the runtime builds a default
    // user message from the payload; override the builder with WithPromptBuilder.
    runtime.WithText(<agenttools>.ToolXYZ, "Please perform the following task: {{"{{"}} . {{"}}"}}"),
)
if err != nil { panic(err) }

// Now this toolset is available in the runtime for other agents to use!
if err := rt.RegisterToolset(reg); err != nil { panic(err) }
```

