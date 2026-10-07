// Package codegen plans one JSON codec for each view that a service can return.
// The adapter keeps the endpoint's view name and encodes only that view's fields;
// agent codecs and stored results use the same declared OneOf contract.
package codegen

import (
	"fmt"

	jsoncodec "goa.design/goa-ai/codegen/internal/codec"
	"goa.design/goa-ai/codegen/internal/mcpcontract"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
)

type (
	// plannedResultView retains one view's selected codec before names are final.
	plannedResultView struct {
		name  string
		value *jsoncodec.Value
	}

	// resultViewCodec supplies the exact generated functions for one view.
	resultViewCodec struct {
		// Name is the view selected by the original endpoint.
		Name string
		// Encode converts only this view's fields into JSON.
		Encode string
		// Validate checks only this view's fields before content conversion.
		Validate string
	}
)

// planExecutionViewCodecs plans the selected fields separately for each view.
// A service-selected name therefore cannot weaken another view's required fields.
func planExecutionViewCodecs(plan *jsoncodec.Plan, services *goaservice.Plan, method *expr.MethodExpr, direction jsoncodec.Direction, preferred string) ([]*plannedResultView, error) {
	contract, err := mcpcontract.Result(method)
	if err != nil {
		return nil, err
	}
	union := expr.AsUnion(contract.Type)
	views := make([]*plannedResultView, 0, len(union.Values))
	for index, branch := range union.Values {
		selected, layout, err := planMCPResultView(services, method, branch.Name)
		if err != nil {
			return nil, err
		}
		key := method.Service.Name + ":" + method.Name + ":view:" + branch.Name
		value, err := plan.Add(key, fmt.Sprintf("%sView%dResult", preferred, index), selected, layout, direction)
		if err != nil {
			return nil, fmt.Errorf("plan MCP result view %q: %w", branch.Name, err)
		}
		views = append(views, &plannedResultView{name: branch.Name, value: value})
	}
	return views, nil
}

// planResultValidation adds the selected typed checks used before prompts,
// resource contents or suggestions are copied into their protocol response.
func (p *plannedMethodCodec) planResultValidation() error {
	if p.result != nil && p.result.ValidationDeclaration() == nil {
		return p.result.PlanValidation()
	}
	for _, view := range p.views {
		if view.value.ValidationDeclaration() == nil {
			if err := view.value.PlanValidation(); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateExecutionViews checks every service-selected view before generation.
// A prompt, resource or suggestion cannot omit a field its protocol requires.
func validateExecutionViews(method *expr.MethodExpr, validate func(*expr.AttributeExpr) error) error {
	result, viewed := method.Result.Type.(*expr.ResultTypeExpr)
	if !viewed {
		return nil
	}
	if _, fixed := mcpcontract.FixedView(method.Result); fixed {
		return nil
	}
	for _, view := range result.Views {
		selected, err := mcpcontract.SelectView(method.Result, view.Name)
		if err != nil {
			return err
		}
		if err := validate(selected); err != nil {
			return fmt.Errorf("method %q result view %q: %w", method.Name, view.Name, err)
		}
	}
	return nil
}
