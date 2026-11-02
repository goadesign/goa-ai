// Package dslshape builds Goa attributes for DSL declarations that accept
// inline object functions, user types, or primitive data types.
package dslshape

import (
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// Build constructs the attribute declared by a schema-bearing Goa-AI DSL
// function. ownerName and suffix identify customized copies of user types.
//
// Build mirrors Goa's method DSL semantics for user types: a customization DSL
// runs against a duplicate of the type, and the duplicate is renamed to
// "<type>_<owner>_<suffix>" only when the customization changes requiredness.
// Customizations that leave requiredness intact (descriptions, examples) keep
// the original type identity so the type stays shared across declarations.
func Build(ownerName, suffix string, value any, args ...any) *goaexpr.AttributeExpr {
	if len(args) > 2 {
		eval.TooManyArgError()
		return nil
	}
	var (
		att         *goaexpr.AttributeExpr
		description string
		customize   func()
		original    goaexpr.UserType
	)
	switch len(args) {
	case 1:
		switch actual := args[0].(type) {
		case string:
			description = actual
		case func():
			customize = actual
		default:
			eval.InvalidArgError("description string or DSL function", args[0])
			return nil
		}
	case 2:
		var ok bool
		description, ok = args[0].(string)
		if !ok {
			eval.InvalidArgError("description string", args[0])
			return nil
		}
		customize, ok = args[1].(func())
		if !ok {
			eval.InvalidArgError("DSL function", args[1])
			return nil
		}
	}
	switch actual := value.(type) {
	case func():
		if len(args) > 0 {
			eval.ReportError("%s with an inline attribute function accepts no additional arguments", suffix)
			return nil
		}
		customize = actual
		att = &goaexpr.AttributeExpr{Type: &goaexpr.Object{}}
	case goaexpr.UserType:
		if len(args) == 0 {
			// Not customized: share the original type instance.
			return &goaexpr.AttributeExpr{Type: actual}
		}
		// Any customization, including a description, operates on a private
		// duplicate so downstream processing never mutates the shared
		// original type instance.
		original = actual
		att = &goaexpr.AttributeExpr{Type: goaexpr.Dup(actual)}
	case goaexpr.DataType:
		att = &goaexpr.AttributeExpr{Type: actual}
	default:
		eval.InvalidArgError("type or function", value)
		return nil
	}
	att.Description = description
	if customize == nil {
		return att
	}
	numreqs := 0
	if att.Validation != nil {
		numreqs = len(att.Validation.Required)
	}
	eval.Execute(customize, att)
	if obj, ok := att.Type.(*goaexpr.Object); ok && len(*obj) == 0 && len(att.Bases) == 0 && len(att.References) == 0 {
		att.Type = goaexpr.Empty
	}
	if original != nil && att.Validation != nil && len(att.Validation.Required) != numreqs {
		if renamer, ok := att.Type.(interface{ Rename(string) }); ok {
			renamer.Rename(original.Name() + "_" + ownerName + "_" + suffix)
		}
	}
	return att
}

// Finalize recursively finalizes user types and bases on an attribute expression,
// materializing Extend-composed fields before expression validation or codegen.
// If the finalized type is an object with zero fields, Finalize collapses it to
// goaexpr.Empty.
func Finalize(att *goaexpr.AttributeExpr) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return
	}
	finalizeVisited(att, make(map[string]bool), make(map[*goaexpr.AttributeExpr]bool))
	if obj, ok := att.Type.(*goaexpr.Object); ok && len(*obj) == 0 {
		att.Type = goaexpr.Empty
	}
}

// finalizeVisited tracks visited user types and attributes to prevent infinite recursion on
// self-referential or mutually referential types while finalizing bases and references.
func finalizeVisited(att *goaexpr.AttributeExpr, visitedTypes map[string]bool, visitedAttrs map[*goaexpr.AttributeExpr]bool) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return
	}
	prepareNestedDependencies(att, visitedTypes, visitedAttrs)
	att.Finalize()
}

// prepareNestedDependencies recursively visits an attribute's bases, references,
// and nested child attributes to ensure all referenced user types and base dependencies
// are fully finalized before att.Finalize() merges them into their callers.
func prepareNestedDependencies(att *goaexpr.AttributeExpr, visitedTypes map[string]bool, visitedAttrs map[*goaexpr.AttributeExpr]bool) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty || visitedAttrs[att] {
		return
	}
	visitedAttrs[att] = true
	for _, dt := range append(append([]goaexpr.DataType{}, att.Bases...), att.References...) {
		if ut, ok := dt.(goaexpr.UserType); ok {
			if id := ut.ID(); id != "" && !visitedTypes[id] {
				visitedTypes[id] = true
				finalizeVisited(ut.Attribute(), visitedTypes, visitedAttrs)
			}
		} else {
			prepareDataTypeDependencies(dt, visitedTypes, visitedAttrs)
		}
	}
	prepareDataTypeDependencies(att.Type, visitedTypes, visitedAttrs)
}

// prepareDataTypeDependencies traverses data types for nested user types and attributes.
func prepareDataTypeDependencies(dt goaexpr.DataType, visitedTypes map[string]bool, visitedAttrs map[*goaexpr.AttributeExpr]bool) {
	if dt == nil {
		return
	}
	if ut, ok := dt.(goaexpr.UserType); ok {
		if id := ut.ID(); id != "" && !visitedTypes[id] {
			visitedTypes[id] = true
			finalizeVisited(ut.Attribute(), visitedTypes, visitedAttrs)
		}
		return
	}
	switch actual := dt.(type) {
	case *goaexpr.Object:
		for _, nat := range *actual {
			if nat != nil && nat.Attribute != nil {
				prepareNestedDependencies(nat.Attribute, visitedTypes, visitedAttrs)
			}
		}
	case *goaexpr.Array:
		if actual.ElemType != nil {
			prepareNestedDependencies(actual.ElemType, visitedTypes, visitedAttrs)
		}
	case *goaexpr.Map:
		if actual.KeyType != nil {
			prepareNestedDependencies(actual.KeyType, visitedTypes, visitedAttrs)
		}
		if actual.ElemType != nil {
			prepareNestedDependencies(actual.ElemType, visitedTypes, visitedAttrs)
		}
	case *goaexpr.Union:
		for _, nat := range actual.Values {
			if nat != nil && nat.Attribute != nil {
				prepareNestedDependencies(nat.Attribute, visitedTypes, visitedAttrs)
			}
		}
	}
}
