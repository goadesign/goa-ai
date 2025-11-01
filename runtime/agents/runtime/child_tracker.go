package runtime

// The childTracker infrastructure enables support for agent-as-tool scenarios,
// where a parent tool call may discover and invoke multiple child tools dynamically
// across planner iterations. This mechanism allows progress tracking and precise
// event emission (e.g., "3 of 5 tool calls completed") for more expressive agent toolchains.
//
// For details on main workflow execution logic, see workflow.go.
func newChildTracker(parentToolCallID string) *childTracker {
	return &childTracker{
		parentToolCallID: parentToolCallID,
		discovered:       make(map[string]struct{}),
	}
}

// registerDiscovered adds newly discovered child tool IDs to the tracker.
// Returns true if at least one new child was discovered (i.e., count increased).
func (c *childTracker) registerDiscovered(toolCallIDs []string) bool {
	if len(toolCallIDs) == 0 {
		return false
	}
	before := len(c.discovered)
	for _, id := range toolCallIDs {
		if id != "" {
			c.discovered[id] = struct{}{}
		}
	}
	return len(c.discovered) > before
}

// currentTotal returns the current count of discovered children.
func (c *childTracker) currentTotal() int {
	return len(c.discovered)
}

// needsUpdate returns true if the discovered count has increased since the last update event.
func (c *childTracker) needsUpdate() bool {
	return len(c.discovered) > c.lastExpectedTotal
}

// markUpdated records that an update event was emitted with the current discovered count.
func (c *childTracker) markUpdated() {
	c.lastExpectedTotal = len(c.discovered)
}
