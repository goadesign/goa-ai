// Package hooks uses this file to validate the immutable identity shared by a
// scheduled tool call and its durable result. Runtime reducers and storage
// migrations call the same pure check without sharing orchestration state.
package hooks

import "fmt"

// ValidateToolResultPlacement verifies whether result may use localSchedule in
// resultRunID. A result recorded by its scheduling run requires that run's
// exact schedule. A continuation result recorded by another run must not reuse
// a call ID scheduled by the recording run; callers validate its schedule from
// result.CallRunID separately with ValidateToolResultCorrelation.
func ValidateToolResultPlacement(
	resultRunID string,
	localSchedule *ToolCallScheduledEvent,
	result *ToolResultReceivedEvent,
) error {
	if result == nil {
		return fmt.Errorf("tool result is required")
	}
	if result.CallRunID == resultRunID {
		return ValidateToolResultCorrelation(localSchedule, result)
	}
	if localSchedule != nil {
		return fmt.Errorf(
			"cross-run tool result call %q reuses a schedule from result run %q",
			result.ToolCallID,
			resultRunID,
		)
	}
	return nil
}

// ValidateToolResultCorrelation verifies that result is the completion of
// scheduled. A result may be recorded by a continuation run, but CallRunID must
// identify the run that emitted scheduled and every tool identity field must
// match exactly.
func ValidateToolResultCorrelation(
	scheduled *ToolCallScheduledEvent,
	result *ToolResultReceivedEvent,
) error {
	if scheduled == nil {
		return fmt.Errorf("tool schedule is required")
	}
	if result == nil {
		return fmt.Errorf("tool result is required")
	}
	if result.CallRunID != scheduled.RunID() {
		return fmt.Errorf(
			"tool result call run %q does not match scheduled run %q",
			result.CallRunID,
			scheduled.RunID(),
		)
	}
	if result.ToolCallID != scheduled.ToolCallID {
		return fmt.Errorf(
			"tool result call %q does not match scheduled call %q",
			result.ToolCallID,
			scheduled.ToolCallID,
		)
	}
	if result.ToolName != scheduled.ToolName {
		return fmt.Errorf(
			"tool result name %q does not match scheduled name %q for call %q",
			result.ToolName,
			scheduled.ToolName,
			scheduled.ToolCallID,
		)
	}
	if result.ParentToolCallID != scheduled.ParentToolCallID {
		return fmt.Errorf(
			"tool result parent %q does not match scheduled parent %q for call %q",
			result.ParentToolCallID,
			scheduled.ParentToolCallID,
			scheduled.ToolCallID,
		)
	}
	if result.SessionID() != scheduled.SessionID() {
		return fmt.Errorf(
			"tool result session %q does not match scheduled session %q for call %q",
			result.SessionID(),
			scheduled.SessionID(),
			scheduled.ToolCallID,
		)
	}
	if result.AgentID() != scheduled.AgentID() {
		return fmt.Errorf(
			"tool result agent %q does not match scheduled agent %q for call %q",
			result.AgentID(),
			scheduled.AgentID(),
			scheduled.ToolCallID,
		)
	}
	return nil
}
