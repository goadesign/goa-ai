package runtime

// activity_input_budget.go enforces workflow-boundary payload budgets for planner activities.
//
// Temporal enforces a hard limit on activity command attribute payload sizes. When the
// runtime attempts to schedule PlanStart/PlanResume with an oversized input, Temporal
// fails the workflow task with BadScheduleActivityAttributes and the run is terminated.
//
// Contract:
//   - Plan activity inputs MUST remain within a strict byte budget.
//   - The budget is measured using JSON encoding to match Temporal's default JSON
//     payload converter (see engine/temporal/data_converter.go).
//   - The runtime enforces this budget deterministically before scheduling the activity,
//     so oversized transcripts fail fast with a clear error instead of terminating the run.

import (
	"encoding/json"
	"fmt"
)

const maxPlanActivityInputBytes = 1_000_000

func enforcePlanActivityInputBudget(input PlanActivityInput) error {
	b, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("runtime: encode plan activity input for budget check: %w", err)
	}
	if len(b) <= maxPlanActivityInputBytes {
		return nil
	}
	return fmt.Errorf(
		"runtime: plan activity input exceeds budget (%d > %d bytes, run_id=%s, messages=%d, tool_results=%d)",
		len(b),
		maxPlanActivityInputBytes,
		input.RunID,
		len(input.Messages),
		len(input.ToolResults),
	)
}
