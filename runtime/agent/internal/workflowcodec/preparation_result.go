// Package workflowcodec decodes preparation activity results as published input
// or a saved stop request. It rejects missing or competing branches so workers
// preserve the original stop reason and cannot treat a stop as prepared input.
package workflowcodec

import (
	"errors"
	"reflect"

	"goa.design/goa-ai/runtime/agent/api"
)

func validatePreparationResult(value reflect.Value) error {
	if !value.IsValid() || !value.CanInterface() {
		return nil
	}
	if (value.Type() == reflect.TypeFor[*api.AgentOperationActivityResult]() || value.Type() == reflect.TypeFor[*api.AgentChildActivityOutput]()) && value.IsNil() {
		return errors.New("workflow codec: preparation result is required")
	}
	var stopped *api.CancellationRequest
	branches := 0
	switch value.Type() {
	case reflect.TypeFor[api.AgentOperationActivityResult]():
		result := value.Interface().(api.AgentOperationActivityResult)
		if result.Prepared != nil {
			branches++
		}
		stopped = result.Stopped
	case reflect.TypeFor[api.AgentChildActivityOutput]():
		result := value.Interface().(api.AgentChildActivityOutput)
		if result.Success != nil {
			branches++
		}
		if result.Failure != nil {
			branches++
		}
		stopped = result.Stopped
	default:
		return nil
	}
	if stopped != nil {
		branches++
		if stopped.RunID == "" || stopped.Reason == "" {
			return errors.New("workflow codec: preparation stop requires run id and reason")
		}
	}
	if branches != 1 {
		return errors.New("workflow codec: preparation result requires exactly one branch")
	}
	return nil
}
