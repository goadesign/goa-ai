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

// Finalize recursively materializes Reference and Extend dependencies across
// the reachable attribute graph and finalizes att. If the finalized top-level
// type is an object with zero fields, Finalize collapses it to goaexpr.Empty.
func Finalize(att *goaexpr.AttributeExpr) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return
	}
	materializeBasesAndRefs(att, make(map[*goaexpr.AttributeExpr]bool))
	att.Finalize()
	if obj, ok := att.Type.(*goaexpr.Object); ok && len(*obj) == 0 {
		att.Type = goaexpr.Empty
	}
}

// materializeBasesAndRefs traverses actual AttributeExpr instances across the
// reachable schema graph, recursively materializing each Reference and Base
// attribute before inheriting or merging it into its consumer. Keying by
// AttributeExpr pointer (rather than UserType.ID) ensures duplicated inline
// union branch schemas and their duplicated base attributes are materialized,
// and avoiding premature att.Finalize() calls during traversal prevents Goa's
// recursive finalizer from clearing unmaterialized sibling bases.
func materializeBasesAndRefs(a *goaexpr.AttributeExpr, visited map[*goaexpr.AttributeExpr]bool) {
	if a == nil || a.Type == nil || a.Type == goaexpr.Empty || visited[a] {
		return
	}
	visited[a] = true
	if ut, ok := a.Type.(goaexpr.UserType); ok {
		materializeBasesAndRefs(ut.Attribute(), visited)
	}
	switch {
	case goaexpr.IsObject(a.Type):
		for _, ref := range a.References {
			if ru, ok := ref.(goaexpr.UserType); ok {
				materializeBasesAndRefs(ru.Attribute(), visited)
				a.Inherit(ru.Attribute())
			}
		}
		for _, base := range a.Bases {
			if ru, ok := base.(goaexpr.UserType); ok {
				materializeBasesAndRefs(ru.Attribute(), visited)
				a.Merge(ru.Attribute())
			}
		}
		a.Bases = nil
		for _, nat := range *goaexpr.AsObject(a.Type) {
			if nat != nil {
				materializeBasesAndRefs(nat.Attribute, visited)
			}
		}
	case goaexpr.IsUnion(a.Type):
		for _, nat := range goaexpr.AsUnion(a.Type).Values {
			if nat != nil {
				materializeBasesAndRefs(nat.Attribute, visited)
			}
		}
	case goaexpr.IsArray(a.Type):
		materializeBasesAndRefs(goaexpr.AsArray(a.Type).ElemType, visited)
	case goaexpr.IsMap(a.Type):
		m := goaexpr.AsMap(a.Type)
		materializeBasesAndRefs(m.ElemType, visited)
		materializeBasesAndRefs(m.KeyType, visited)
	}
}
