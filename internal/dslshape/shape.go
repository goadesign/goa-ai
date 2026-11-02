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

// Finalize ensures all Reference and Extend base attributes across the reachable
// schema graph are finalized in dependency order before finalizing att. If the
// finalized top-level type is an object with zero fields, Finalize collapses it
// to goaexpr.Empty.
func Finalize(att *goaexpr.AttributeExpr) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return
	}
	if isAttributeFinalized(att) {
		if obj, ok := att.Type.(*goaexpr.Object); ok && len(*obj) == 0 {
			att.Type = goaexpr.Empty
		}
		return
	}
	savedRefs := make(map[*goaexpr.AttributeExpr][]goaexpr.DataType)
	nodes := resolveSubgraphInheritance(att, savedRefs)
	for _, node := range nodes {
		node.Finalize()
	}
	att.Finalize()
	for a, refs := range savedRefs {
		a.References = refs
	}
	if obj, ok := att.Type.(*goaexpr.Object); ok && len(*obj) == 0 {
		att.Type = goaexpr.Empty
	}
}

// isAttributeFinalized probes whether a has already been finalized by Goa.
// It executes Goa's Finalize on a shallow copy with a sentinel base; if a
// was already finalized, Goa's Finalize returns immediately and leaves the
// sentinel base intact, whereas if a was unfinalized, Goa clears Bases to nil.
func isAttributeFinalized(a *goaexpr.AttributeExpr) bool {
	if a == nil {
		return true
	}
	probe := *a
	probe.Type = &goaexpr.Object{}
	probe.References = nil
	probe.Bases = []goaexpr.DataType{goaexpr.String}
	probe.Finalize()
	return len(probe.Bases) > 0
}

// resolveReferenceTarget recursively resolves child object fields of a reference
// target so that inheritRecursive sees complete schemas on nested child attributes.
func resolveReferenceTarget(
	refAtt *goaexpr.AttributeExpr,
	seen map[*goaexpr.AttributeExpr]bool,
	resolving map[*goaexpr.AttributeExpr]bool,
	resolved map[*goaexpr.AttributeExpr]bool,
	savedRefs map[*goaexpr.AttributeExpr][]goaexpr.DataType,
) {
	if refAtt == nil || refAtt.Type == nil || seen[refAtt] {
		return
	}
	seen[refAtt] = true
	resolveObjectInheritance(refAtt, resolving, resolved, savedRefs)
	if ut, ok := refAtt.Type.(goaexpr.UserType); ok && ut.Attribute() != nil {
		resolveReferenceTarget(ut.Attribute(), seen, resolving, resolved, savedRefs)
	}
	if obj := goaexpr.AsObject(refAtt.Type); obj != nil {
		for _, nat := range *obj {
			if nat != nil && nat.Attribute != nil && goaexpr.IsObject(nat.Attribute.Type) {
				resolveReferenceTarget(nat.Attribute, seen, resolving, resolved, savedRefs)
			}
		}
	}
}

// resolveObjectInheritance resolves References and Bases dependencies on a in
// topological order, merging base schemas before a inherits or merges them.
// Saved references are cleared while Goa finalizes to prevent recursive replay.
func resolveObjectInheritance(
	a *goaexpr.AttributeExpr,
	resolving map[*goaexpr.AttributeExpr]bool,
	resolved map[*goaexpr.AttributeExpr]bool,
	savedRefs map[*goaexpr.AttributeExpr][]goaexpr.DataType,
) {
	if a == nil || a.Type == nil || a.Type == goaexpr.Empty || resolved[a] || isAttributeFinalized(a) {
		return
	}
	if resolving[a] {
		return
	}
	resolving[a] = true
	defer func() {
		resolving[a] = false
	}()

	if ut, ok := a.Type.(goaexpr.UserType); ok && ut.Attribute() != nil {
		resolveObjectInheritance(ut.Attribute(), resolving, resolved, savedRefs)
	}

	for _, ref := range a.References {
		if ru, ok := ref.(goaexpr.UserType); ok && ru.Attribute() != nil {
			resolveReferenceTarget(ru.Attribute(), make(map[*goaexpr.AttributeExpr]bool), resolving, resolved, savedRefs)
		}
	}

	for _, base := range a.Bases {
		if bu, ok := base.(goaexpr.UserType); ok && bu.Attribute() != nil {
			resolveObjectInheritance(bu.Attribute(), resolving, resolved, savedRefs)
		}
	}

	if goaexpr.IsObject(a.Type) {
		for _, ref := range a.References {
			if ru, ok := ref.(goaexpr.UserType); ok && ru.Attribute() != nil {
				a.Inherit(ru.Attribute())
			}
		}
		for _, base := range a.Bases {
			if bu, ok := base.(goaexpr.UserType); ok && bu.Attribute() != nil {
				a.Merge(bu.Attribute())
			}
		}
		a.Bases = nil
		if len(a.References) > 0 {
			savedRefs[a] = a.References
			a.References = nil
		}
	}

	resolved[a] = true
}

// resolveSubgraphInheritance finds all attributes in root's schema graph and
// resolves their inheritance in topological dependency order.
func resolveSubgraphInheritance(root *goaexpr.AttributeExpr, savedRefs map[*goaexpr.AttributeExpr][]goaexpr.DataType) []*goaexpr.AttributeExpr {
	var nodes []*goaexpr.AttributeExpr
	collectInheritanceNodes(root, make(map[*goaexpr.AttributeExpr]bool), &nodes)

	resolving := make(map[*goaexpr.AttributeExpr]bool)
	resolved := make(map[*goaexpr.AttributeExpr]bool)

	for _, node := range nodes {
		resolveObjectInheritance(node, resolving, resolved, savedRefs)
	}
	return nodes
}

// collectInheritanceNodes traverses the schema graph starting at a, recording every
// attribute expression reachable via object fields, array elements, map keys/elements,
// union branches, and base/reference types.
func collectInheritanceNodes(a *goaexpr.AttributeExpr, seen map[*goaexpr.AttributeExpr]bool, nodes *[]*goaexpr.AttributeExpr) {
	if a == nil || a.Type == nil || a.Type == goaexpr.Empty || seen[a] {
		return
	}
	seen[a] = true
	*nodes = append(*nodes, a)

	for _, ref := range a.References {
		if ru, ok := ref.(goaexpr.UserType); ok && ru.Attribute() != nil {
			collectInheritanceNodes(ru.Attribute(), seen, nodes)
		}
	}
	for _, base := range a.Bases {
		if bu, ok := base.(goaexpr.UserType); ok && bu.Attribute() != nil {
			collectInheritanceNodes(bu.Attribute(), seen, nodes)
		}
	}
	if ut, ok := a.Type.(goaexpr.UserType); ok && ut.Attribute() != nil {
		collectInheritanceNodes(ut.Attribute(), seen, nodes)
	}
	switch {
	case goaexpr.IsObject(a.Type):
		for _, nat := range *goaexpr.AsObject(a.Type) {
			if nat != nil && nat.Attribute != nil {
				collectInheritanceNodes(nat.Attribute, seen, nodes)
			}
		}
	case goaexpr.IsUnion(a.Type):
		for _, nat := range goaexpr.AsUnion(a.Type).Values {
			if nat != nil && nat.Attribute != nil {
				collectInheritanceNodes(nat.Attribute, seen, nodes)
			}
		}
	case goaexpr.IsArray(a.Type):
		collectInheritanceNodes(goaexpr.AsArray(a.Type).ElemType, seen, nodes)
	case goaexpr.IsMap(a.Type):
		m := goaexpr.AsMap(a.Type)
		collectInheritanceNodes(m.ElemType, seen, nodes)
		collectInheritanceNodes(m.KeyType, seen, nodes)
	}
}
