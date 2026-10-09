// Package mcpcontract selects the Goa result fields that an MCP method may
// return. Catalogs and generated agent codecs use the same selected contract;
// a view never reconstructs fields that Goa omitted from the response.
package mcpcontract

import (
	"fmt"

	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa/v3/expr"
)

// Result returns the exact fields of a fixed Goa view. When the service chooses
// a view during execution, it returns a OneOf contract containing that view's
// name and selected fields. Ordinary results keep their authored contract.
func Result(method *expr.MethodExpr) (*expr.AttributeExpr, error) {
	mapping, err := mcpinput.InputExchange(method)
	if err != nil {
		return nil, err
	}
	task, err := mcpinput.TaskExchange(method)
	if err != nil {
		return nil, err
	}
	attribute := method.Result
	if task != nil {
		attribute, mapping = task.Read.Result, nil
	}
	result, viewed := attribute.Type.(*expr.ResultTypeExpr)
	if !viewed {
		return completedContract(attribute, mapping, task)
	}
	if view, fixed := FixedView(attribute); fixed {
		return ResultView(method, view)
	}
	union := &expr.Union{TypeName: result.TypeName + "MCPViews"}
	for _, view := range result.Views {
		completed, err := ResultView(method, view.Name)
		if err != nil {
			return nil, fmt.Errorf("select completed result for view %q: %w", view.Name, err)
		}
		union.Values = append(union.Values, &expr.NamedAttributeExpr{Name: view.Name, Attribute: completed})
	}
	return &expr.AttributeExpr{
		Type:        union,
		Description: "The view selected by the service and the fields returned through that view.",
	}, nil
}

// ResultView selects one authored view and returns its completed value. Catalogs
// and server conversions use this same selection while endpoint calls retain
// the full native outcome, including requests for additional input.
func ResultView(method *expr.MethodExpr, view string) (*expr.AttributeExpr, error) {
	mapping, err := mcpinput.InputExchange(method)
	if err != nil {
		return nil, err
	}
	task, err := mcpinput.TaskExchange(method)
	if err != nil {
		return nil, err
	}
	attribute := method.Result
	if task != nil {
		attribute, mapping = task.Read.Result, nil
	}
	selected, err := SelectView(attribute, view)
	if err != nil {
		return nil, err
	}
	return completedContract(selected, mapping, task)
}

// SelectView returns only fields and validation included in the named view.
// It retains nested type locations so generated codecs use the authored types.
func SelectView(attribute *expr.AttributeExpr, view string) (*expr.AttributeExpr, error) {
	result, ok := attribute.Type.(*expr.ResultTypeExpr)
	if !ok {
		return nil, fmt.Errorf("MCP result does not declare Goa views")
	}
	projected, err := expr.Project(result, view)
	if err != nil {
		return nil, fmt.Errorf("select MCP result view %q: %w", view, err)
	}
	selected := expr.DupAtt(attribute)
	selected.Type = projected
	return selected, nil
}

// FixedView resolves the view selected by the design. Multiple available views
// without a method selection leave the choice to the service during execution.
func FixedView(attribute *expr.AttributeExpr) (string, bool) {
	result, ok := attribute.Type.(*expr.ResultTypeExpr)
	if !ok {
		return "", false
	}
	if view, selected := attribute.Meta.Last(expr.ViewMetaKey); selected {
		return view, true
	}
	if !result.HasMultipleViews() {
		return expr.DefaultView, true
	}
	return "", false
}

// completedContract removes an operation's unfinished branch after selecting
// its native result view. Nested result types use their authored view or Goa's
// default; only the top-level service result can choose a view during execution.
func completedContract(attribute *expr.AttributeExpr, mapping *mcpinput.Exchange, task *mcpinput.TaskBinding) (*expr.AttributeExpr, error) {
	var err error
	if mapping != nil {
		attribute, err = completedOutcome(attribute, mapping.OutcomeName, "InputExchange")
		if err != nil {
			return nil, err
		}
	}
	if task != nil {
		return completedOutcome(attribute, "outcome", "TaskExchange")
	}
	return attribute, nil
}

// completedOutcome selects finished data from the authored or selected view.
// Missing outcome fields are errors; omitted fields are never reconstructed.
func completedOutcome(attribute *expr.AttributeExpr, outcomeName, contract string) (*expr.AttributeExpr, error) {
	object := expr.AsObject(attribute.Type)
	if object == nil || object.Attribute(outcomeName) == nil {
		return nil, fmt.Errorf("%s selected result view omits outcome %q", contract, outcomeName)
	}
	union := expr.AsUnion(object.Attribute(outcomeName).Type)
	if union == nil {
		return nil, fmt.Errorf("%s selected outcome is not a OneOf", contract)
	}
	for _, branch := range union.Values {
		if branch.Name != "complete" {
			continue
		}
		if _, viewed := branch.Attribute.Type.(*expr.ResultTypeExpr); !viewed {
			return branch.Attribute, nil
		}
		view := expr.DefaultView
		if selected, explicit := branch.Attribute.Meta.Last(expr.ViewMetaKey); explicit {
			view = selected
		}
		return SelectView(branch.Attribute, view)
	}
	return nil, fmt.Errorf("%s selected outcome omits complete branch", contract)
}
