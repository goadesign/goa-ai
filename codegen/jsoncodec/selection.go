// Package jsoncodec selects declarations during generation. This file checks
// placement without creating a DSL root or mutating the evaluated design.
package jsoncodec

import (
	"fmt"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

const selectionKey = "goa-ai:json:codec"

// selections validates marker placement and deduplicates original declarations.
func selections(roots []eval.Root) ([]expr.UserType, error) {
	var selected []expr.UserType
	origins := make(map[expr.UserType]bool)
	for _, root := range roots {
		design, ok := root.(*expr.RootExpr)
		if !ok {
			continue
		}
		declared := make(map[*expr.AttributeExpr]bool)
		types := append([]expr.UserType(nil), design.Types...)
		for _, result := range design.ResultTypes {
			types = append(types, result)
		}
		for _, userType := range types {
			declared[userType.Attribute()] = true
			values, marked := userType.Attribute().Meta[selectionKey]
			if !marked {
				continue
			}
			if len(values) != 0 {
				return nil, fmt.Errorf("%s on type %q takes no values", selectionKey, userType.Name())
			}
			if userType != userType.Origin() {
				return nil, fmt.Errorf("%s requires an original declaration, got copied type %q", selectionKey, userType.Name())
			}
			if codegen.UserTypeLocation(userType) == nil {
				return nil, fmt.Errorf("%s on type %q requires an explicit generated package location", selectionKey, userType.Name())
			}
			if !origins[userType.Origin()] {
				origins[userType.Origin()] = true
				selected = append(selected, userType)
			}
		}
		seen := make(map[*expr.AttributeExpr]bool)
		for _, userType := range types {
			if err := checkPlacement(userType.Attribute(), declared, seen); err != nil {
				return nil, err
			}
		}
		if err := rejectMarker(design.API.Meta, design.API.EvalName()); err != nil {
			return nil, err
		}
		for _, service := range design.Services {
			if err := rejectMarker(service.Meta, service.EvalName()); err != nil {
				return nil, err
			}
			for _, method := range service.Methods {
				if err := rejectMarker(method.Meta, method.EvalName()); err != nil {
					return nil, err
				}
				for _, attribute := range []*expr.AttributeExpr{method.Payload, method.Result, method.StreamingPayload, method.StreamingResult} {
					if err := checkPlacement(attribute, declared, seen); err != nil {
						return nil, err
					}
				}
				for _, serviceError := range method.Errors {
					if err := checkPlacement(serviceError.AttributeExpr, declared, seen); err != nil {
						return nil, err
					}
				}
			}
			for _, serviceError := range service.Errors {
				if err := checkPlacement(serviceError.AttributeExpr, declared, seen); err != nil {
					return nil, err
				}
			}
		}
		for _, designError := range design.Errors {
			if err := checkPlacement(designError.AttributeExpr, declared, seen); err != nil {
				return nil, err
			}
		}
	}
	return selected, nil
}

// rejectMarker reports a misplaced marker, including markers with no values.
func rejectMarker(meta expr.MetaExpr, context string) error {
	if _, marked := meta[selectionKey]; marked {
		return fmt.Errorf("%s is only valid on a named type declaration, not %s", selectionKey, context)
	}
	return nil
}

// checkPlacement visits each attribute once, including recursive references.
func checkPlacement(attribute *expr.AttributeExpr, declared, seen map[*expr.AttributeExpr]bool) error {
	if attribute == nil || seen[attribute] {
		return nil
	}
	seen[attribute] = true
	if !declared[attribute] {
		if err := rejectMarker(attribute.Meta, "an attribute occurrence"); err != nil {
			return err
		}
	}
	switch actual := attribute.Type.(type) {
	case expr.UserType:
		return checkPlacement(actual.Attribute(), declared, seen)
	case *expr.Object:
		for _, field := range *actual {
			if err := checkPlacement(field.Attribute, declared, seen); err != nil {
				return fmt.Errorf("field %q: %w", field.Name, err)
			}
		}
	case *expr.Array:
		return checkPlacement(actual.ElemType, declared, seen)
	case *expr.Map:
		if err := checkPlacement(actual.KeyType, declared, seen); err != nil {
			return err
		}
		return checkPlacement(actual.ElemType, declared, seen)
	case *expr.Union:
		for _, branch := range actual.Values {
			if err := checkPlacement(branch.Attribute, declared, seen); err != nil {
				return err
			}
		}
	}
	return nil
}
