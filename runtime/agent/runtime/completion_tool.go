package runtime

// This file requires one named budgeted tool to succeed before a run can finish
// successfully. Planner text, a call limit, or a deadline cannot replace that
// tool's required side effect.

import (
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

// validateCompletionToolPolicy checks that the executing agent owns the
// required completion tool and that the caller's tool filters allow it.
func (r *Runtime) validateCompletionToolPolicy(reg AgentRegistration, runPolicy *PolicyOverrides) error {
	completion := completionToolFromPolicy(runPolicy)
	if completion == "" {
		return nil
	}
	if runPolicy.LimitTerminalPlans != nil {
		return errors.New("completion tool and limit terminal plans cannot be combined")
	}
	spec, ok := agentToolSpec(reg.Specs, completion)
	if !ok {
		return fmt.Errorf("completion tool %q is not registered for agent %q", completion, reg.ID)
	}
	if spec.TerminalRun {
		return fmt.Errorf("completion tool %q must not be a terminal tool", completion)
	}
	if spec.Bookkeeping {
		return fmt.Errorf("completion tool %q must be budgeted", completion)
	}
	if spec.Confirmation != nil {
		return fmt.Errorf("completion tool %q cannot require confirmation", completion)
	}
	compiled := compileToolPolicy(runPolicy)
	if !compiled.allowsTool(completion, toolPolicyFacts{tags: spec.Tags}) {
		return fmt.Errorf("completion tool %q is excluded by the run tool policy", completion)
	}
	return nil
}

// validateCompletionToolWorkflowRetry prevents engine retries from recreating
// run caps and deadlines after a completion-policy failure.
func validateCompletionToolWorkflowRetry(runPolicy *PolicyOverrides, opts *WorkflowOptions) error {
	if completionToolFromPolicy(runPolicy) == "" || opts == nil {
		return nil
	}
	retry := opts.RetryPolicy
	if retry.MaxAttempts != 0 || retry.InitialInterval != 0 || retry.BackoffCoefficient != 0 {
		return errors.New("completion tool runs cannot configure whole-workflow retries")
	}
	return nil
}

// validateCompletionToolPlanResult rejects planner output that assigns another
// action to the same decision as the completion side effect. A completion
// attempt must be the sole action in its planner result, while terminal output
// is never a substitute for the required successful tool result.
func (r *Runtime) validateCompletionToolPlanResult(result *planner.PlanResult, completion tools.Ident) error {
	if completion == "" {
		return nil
	}
	if result.FinalResponse != nil || result.FinalToolResult != nil {
		return completionToolRequiredError(completion, "planner returned a terminal response")
	}
	if result.SynthesizeAfterTools {
		return completionToolRequiredError(completion, "planner requested post-tool synthesis")
	}
	for _, call := range result.ToolCalls {
		if call.Name != completion {
			continue
		}
		if len(result.ToolCalls) != 1 || result.Await != nil {
			return fmt.Errorf("completion tool %q must be the only action in its planner response", completion)
		}
	}
	for _, call := range result.ToolCalls {
		spec, ok := r.toolSpec(call.Name)
		if ok && spec.TerminalRun {
			return completionToolRequiredError(
				completion,
				fmt.Sprintf("planner selected terminal tool %q", call.Name),
			)
		}
	}
	if result.Await != nil {
		for _, call := range awaitToolRequests(result.Await.Items) {
			if call.Name == completion {
				return completionToolRequiredError(completion, "planner delegated its execution to await work")
			}
		}
	}
	return nil
}

// completionToolSucceeded reports whether this step successfully executed the
// configured completion tool. Failed calls remain eligible for normal planner
// correction and retry.
func completionToolSucceeded(records []stepToolRecord, completion tools.Ident) (bool, error) {
	if completion == "" {
		return false, nil
	}
	if err := validateCompletionToolRecords(records, completion); err != nil {
		return false, err
	}
	for _, record := range records {
		if record.call.Name != completion {
			continue
		}
		return record.result.Failure == nil, nil
	}
	return false, nil
}

// validateCompletionToolRecords rejects execution shapes that could only fail
// after the runtime had already exposed an unusable suspension.
func validateCompletionToolRecords(records []stepToolRecord, completion tools.Ident) error {
	if completion == "" {
		return nil
	}
	for _, record := range records {
		if record.call.Name != completion {
			continue
		}
		if record.clarification != nil {
			return fmt.Errorf("completion tool %q cannot request clarification", completion)
		}
		if record.result == nil {
			return fmt.Errorf("completion tool %q returned no result", completion)
		}
	}
	return nil
}

// completionTool returns the tool that must succeed before this run can finish.
func completionTool(input *RunInput) tools.Ident {
	if input == nil || input.Policy == nil {
		return ""
	}
	return completionToolFromPolicy(input.Policy)
}

// completionToolFromPolicy returns the tool that owns success for one run
// policy. Callers use it while validating both new inputs and saved checkpoints.
func completionToolFromPolicy(runPolicy *PolicyOverrides) tools.Ident {
	if runPolicy == nil {
		return ""
	}
	return runPolicy.CompletionTool
}

// completionToolRequiredError explains why a completion run ended without the
// required successful side effect.
func completionToolRequiredError(completion tools.Ident, reason any) error {
	return fmt.Errorf("completion tool %q did not succeed: %v", completion, reason)
}
