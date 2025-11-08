# Agent-as-Tool: Strong Contracts, No Fallbacks

## Desired Outcome

- Parent agents register child agents’ exported toolsets; no service executor wiring in the parent.
- Model tool calls for child tools are executed on the child agent’s worker queue, not inline.
- Runtime bridges child events into the parent stream for UI continuity and aggregates a single parent tool_result.
- Public tool names can be cleanly aliased to provider canonical names without duplicating specs.

## Plan

1. Switch agent-as-tool execution to schedule child workflows (provider route) and wait for completion.
2. Provide a stream bridge that relays child events tagged with parent_tool_use_id.
3. Keep a WithAliases option for registrations to allow public→canonical mapping (optional follow-up).
4. Document the strong-contract behavior and remove inline fallbacks.
5. Add unit tests validating route/queue use and result aggregation.

## Progress

- [x] Child workflow scheduling: parent now starts provider workflow and waits for completion.
- [ ] Event bridge: relay child events into parent stream with parent_tool_use_id.
- [ ] Aliases option for registrations.
- [x] Remove inline execution code path usage in registration.
- [ ] Unit tests for runtime scheduling and aggregation.
