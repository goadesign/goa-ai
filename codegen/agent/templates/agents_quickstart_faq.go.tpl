---

## 11. 🤔 Stuck? Common Questions & Fixes

* **Error: "runtime not initialized"**
* **Fix:** Ensure you register agents with the same runtime instance you use to start runs.
* **Error: "agent not registered"**
    * **Fix:** Check that `Register<AgentName>(...)` was called successfully for that agent before you tried to run it.
* **Error: "session id is required"**
    * **Fix:** Always provide a unique, non-empty string for the `sessionID` when calling `agent.Run(...)`.
* **Error: "session not found"**
    * **Fix:** Sessions are explicit. Create the session through the host service before starting runs under that session ID.
* **Error: an MCP executable is missing**
    * **Fix:** Register the generated MCP executor through its used-toolset executor option before starting runs. Shared bindings are registered once. See section 5.
* **Agent-as-Tool isn't working?**
    * **Fix:** Register the exported toolset with `agenttools.NewRegistration(...)` and run its provider worker on the generated workflow queue. Prompt overrides are optional; the runtime builds the default prompt from the tool payload. If you supply a template, validate it with `runtime.ValidateAgentToolTemplates` and provide every field it references.
