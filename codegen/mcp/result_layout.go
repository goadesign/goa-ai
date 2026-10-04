// Package codegen connects fixed MCP result contracts to Goa's generated view types.
// The server encodes the selected fields already returned by the endpoint;
// the codec never rebuilds a full service result with omitted values.
package codegen

import (
	"fmt"

	"goa.design/goa-ai/codegen/internal/mcpcontract"
	"goa.design/goa/v3/codegen"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
)

type (
	// resultTypePair identifies one generated type under one selected view.
	// Repeated fields share that selection; different views keep separate fields.
	resultTypePair struct {
		source, contract expr.DataType
	}
)

// planMCPResult keeps Goa's optional view fields in the source Go layout while
// applying the selected view's required fields to the private JSON contract.
func planMCPResult(services *goaservice.Plan, method *expr.MethodExpr) (*expr.AttributeExpr, *codegen.GoTypePlan, error) {
	contract, err := mcpcontract.Result(method)
	if err != nil {
		return nil, nil, err
	}
	if _, fixed := mcpcontract.FixedView(method); !fixed {
		layout, err := services.MethodTypeLayout(method, contract)
		return contract, layout, err
	}
	source, err := services.ProjectedResult(method)
	if err != nil {
		return nil, nil, err
	}
	if err := selectResultFields(source, contract, make(map[resultTypePair]expr.DataType)); err != nil {
		return nil, nil, fmt.Errorf("select MCP result fields for method %q: %w", method.Name, err)
	}
	layout, err := services.MethodTypeLayout(method, source)
	if err != nil {
		return nil, nil, err
	}
	return source, layout, nil
}

// selectResultFields retains the generated view declarations and copies only
// fields and constraints from the selected contract. Each nested view receives
// its own field selection, including when siblings share a generated Go type.
// Omitted fields stay outside both JSON encoding and decoding.
func selectResultFields(source, contract *expr.AttributeExpr, seen map[resultTypePair]expr.DataType) error {
	source.Validation = nil
	if validation := expr.EffectiveValidation(contract); validation != nil {
		source.Validation = validation.Dup()
	}
	pair := resultTypePair{source.Type, contract.Type}
	if selected, ok := seen[pair]; ok {
		source.Type = selected
		return nil
	}
	if named, ok := source.Type.(expr.UserType); ok {
		attribute := *named.Attribute()
		selected := named.Dup(&attribute)
		seen[pair] = selected
		source.Type = selected
		target := contract
		if namedTarget, ok := contract.Type.(expr.UserType); ok {
			target = namedTarget.Attribute()
		}
		return selectResultFields(selected.Attribute(), target, seen)
	}
	switch actual := source.Type.(type) {
	case *expr.Object:
		target := expr.AsObject(contract.Type)
		if target == nil {
			return fmt.Errorf("view object has a non-object contract")
		}
		fields := make(expr.Object, 0, len(*target))
		for _, field := range *target {
			attribute := actual.Attribute(field.Name)
			if attribute == nil {
				return fmt.Errorf("view field %q is absent from Goa's generated view type", field.Name)
			}
			selected := *attribute
			if err := selectResultFields(&selected, field.Attribute, seen); err != nil {
				return err
			}
			fields = append(fields, &expr.NamedAttributeExpr{Name: field.Name, Attribute: &selected})
		}
		source.Type = &fields
	case *expr.Array:
		target := expr.AsArray(contract.Type)
		if target == nil {
			return fmt.Errorf("view array has a non-array contract")
		}
		selected, element := *actual, *actual.ElemType
		selected.ElemType = &element
		source.Type = &selected
		return selectResultFields(selected.ElemType, target.ElemType, seen)
	case *expr.Union:
		target := expr.AsUnion(contract.Type)
		if target == nil || len(target.Values) != len(actual.Values) {
			return fmt.Errorf("view union does not match its selected contract")
		}
		selected := *actual
		selected.Values = make([]*expr.NamedAttributeExpr, len(actual.Values))
		source.Type = &selected
		for index, branch := range actual.Values {
			if branch.Name != target.Values[index].Name {
				return fmt.Errorf("view union branch does not match its selected contract")
			}
			attribute := *branch.Attribute
			selected.Values[index] = &expr.NamedAttributeExpr{Name: branch.Name, Attribute: &attribute}
			if err := selectResultFields(&attribute, target.Values[index].Attribute, seen); err != nil {
				return err
			}
		}
	case *expr.Map:
		target := expr.AsMap(contract.Type)
		if target == nil {
			return fmt.Errorf("view map has a non-map contract")
		}
		selected, element := *actual, *actual.ElemType
		selected.ElemType = &element
		source.Type = &selected
		return selectResultFields(selected.ElemType, target.ElemType, seen)
	}
	return nil
}
