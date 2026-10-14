// Package runtime retains each terminal message's original provider identity
// whether the planner returns the message alone or with a typed operation result.
package runtime

import "goa.design/goa-ai/runtime/agent/planner"

// terminalResponse selects the message from an already validated terminal
// result. Every provenance, transcript and output consumer uses the same value.
func terminalResponse(response *planner.FinalResponse, result *planner.FinalToolResult) *planner.FinalResponse {
	if response != nil {
		return response
	}
	if result != nil {
		return result.FinalResponse
	}
	return nil
}
