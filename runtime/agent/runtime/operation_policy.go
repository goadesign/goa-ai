// Package runtime accepts text-only restrictions and exact root worker builds
// for operations. The provider owns planner policy and generated workflow routes.
package runtime

import (
	"errors"
	"reflect"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolserverdata"
)

// exactOperationOptions rejects unsupported options before any option can change
// messages, preparation identity, or provider policy. A false text restriction
// preserves the original nil policy and its existing request identity.
func exactOperationOptions(options []RunOption) (runStart, error) {
	var start runStart
	for _, option := range options {
		switch option.(type) {
		case textOnlyOption, rootExecutionTargetOption:
			option.apply(&start)
		default:
			return runStart{}, errors.New("exact operations accept only WithTextOnly and WithRootExecutionTarget")
		}
	}
	if start.input.Policy != nil && !start.input.Policy.TextOnly {
		start.input.Policy = nil
	}
	return start, nil
}

// validateExactOperationPolicy checks accepted caller input before provider
// preparation. The provider may supply planner policy only after this check.
func validateExactOperationPolicy(policy *PolicyOverrides) error {
	if policy != nil && !reflect.DeepEqual(policy, &PolicyOverrides{TextOnly: policy.TextOnly}) {
		return errors.New("exact operations accept only the text-only caller restriction")
	}
	return nil
}

// exactOperationSpec selects the generated argument contract before caller input
// is decoded. Interactive tools cannot be started with a text-only restriction.
func exactOperationSpec(spec tools.ToolSpec, policy *PolicyOverrides) (tools.ToolSpec, error) {
	if policy == nil || !policy.TextOnly {
		return spec, nil
	}
	if spec.RequiresUI || spec.Confirmation != nil || spec.TextOnly == nil {
		return tools.ToolSpec{}, errors.New("operation requires UI or has no generated text-only contract")
	}
	return spec.ForTextOnly(), nil
}

// validateTextOnlyOperationOutput checks provider replies before an activity
// returns them and again before a workflow saves them. Waiting for provider work
// is allowed, but asking the host for input or returning UI data is not.
func validateTextOnlyOperationOutput(output *api.ToolOutput) error {
	if output.PendingExecution != nil && pendingHostInput(output.PendingExecution) != nil || output.Clarification != nil {
		return errors.New("text-only operations cannot request host input")
	}
	return toolserverdata.ValidateTextOnly(output.ServerData)
}
