package dsl

import (
	"fmt"

	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"

	agentsexpr "goa.design/goa-ai/expr/agent"
)

func ensureBoundedResultShape(tool *agentsexpr.ToolExpr) {
	if tool == nil {
		return
	}
	if tool.Return == nil || tool.Return.Type == nil || tool.Return.Type == goaexpr.Empty {
		eval.ReportError("BoundedResult requires a non-empty object result schema")
		return
	}

	root := tool.Return
	var ut goaexpr.UserType
	if t, ok := tool.Return.Type.(goaexpr.UserType); ok && t != nil {
		ut = t
		root = t.Attribute()
	}
	obj := goaexpr.AsObject(root.Type)
	if obj == nil {
		eval.ReportError("BoundedResult requires the tool result schema to be an object")
		return
	}

	present := boundedFieldPresence(root)

	switch {
	case present.none():
		if ut != nil {
			dup, ok := goaexpr.Dup(ut).(goaexpr.UserType)
			if !ok || dup == nil {
				eval.ReportError("BoundedResult: could not duplicate tool result user type")
				return
			}
			if renamer, ok := dup.(interface{ Rename(string) }); ok {
				renamer.Rename(ut.Name() + "_" + tool.Name + "_BoundedResult")
			}
			tool.Return.Type = dup
			root = dup.Attribute()
			obj = goaexpr.AsObject(root.Type)
		}
	case present.all():
		// Author provided the entire canonical bounds field set: validate below.
	default:
		eval.ReportError("BoundedResult: either declare all bounds fields (returned,total,truncated,refinement_hint) or declare none and let BoundedResult add them")
		return
	}

	if obj == nil {
		eval.ReportError("BoundedResult requires the tool result schema to be an object")
		return
	}

	ensureBoundField(root, obj, "returned", goaexpr.Int, "Number of items returned in this bounded view.")
	ensureBoundField(root, obj, "truncated", goaexpr.Boolean, "True when the result is a bounded/truncated view over a larger data set.")
	ensureOptionalBoundField(root, obj, "total", goaexpr.Int, "Total number of matching items before truncation, when known.")
	ensureOptionalBoundField(root, obj, "refinement_hint", goaexpr.String, "Short guidance on how to refine the query or page when results are truncated.")

	ensureRequired(root, "returned")
	ensureRequired(root, "truncated")

	if !isValidBoundField(root, "returned", goaexpr.Int) || !isValidBoundField(root, "truncated", goaexpr.Boolean) {
		eval.ReportError("BoundedResult: invalid bounds field types (returned must be Int, truncated must be Boolean)")
	}
	if !isValidOptionalBoundField(root, "total", goaexpr.Int) || !isValidOptionalBoundField(root, "refinement_hint", goaexpr.String) {
		eval.ReportError("BoundedResult: invalid optional bounds field types or requiredness (total/refinement_hint must be optional)")
	}
}

type boundedPresence struct {
	returned       bool
	total          bool
	truncated      bool
	refinementHint bool
}

func boundedFieldPresence(att *goaexpr.AttributeExpr) boundedPresence {
	p := boundedPresence{}
	if att == nil {
		return p
	}
	p.returned = att.Find("returned") != nil
	p.total = att.Find("total") != nil
	p.truncated = att.Find("truncated") != nil
	p.refinementHint = att.Find("refinement_hint") != nil
	return p
}

func (p boundedPresence) none() bool {
	return !p.returned && !p.total && !p.truncated && !p.refinementHint
}

func (p boundedPresence) all() bool {
	return p.returned && p.total && p.truncated && p.refinementHint
}

func ensureBoundField(root *goaexpr.AttributeExpr, obj *goaexpr.Object, name string, typ goaexpr.DataType, desc string) {
	if obj.Attribute(name) == nil {
		obj.Set(name, &goaexpr.AttributeExpr{Type: typ, Description: desc})
		return
	}
	a := root.Find(name)
	if a == nil || a.Type == nil || a.Type == goaexpr.Empty {
		eval.ReportError(fmt.Sprintf("BoundedResult: field %q must be a %s", name, typeName(typ)))
		return
	}
	if a.Type != typ {
		eval.ReportError(fmt.Sprintf("BoundedResult: field %q must be a %s", name, typeName(typ)))
		return
	}
	if a.Description == "" {
		a.Description = desc
	}
}

func ensureOptionalBoundField(root *goaexpr.AttributeExpr, obj *goaexpr.Object, name string, typ goaexpr.DataType, desc string) {
	if obj.Attribute(name) == nil {
		obj.Set(name, &goaexpr.AttributeExpr{Type: typ, Description: desc})
		return
	}
	a := root.Find(name)
	if a == nil || a.Type == nil || a.Type == goaexpr.Empty {
		eval.ReportError(fmt.Sprintf("BoundedResult: field %q must be a %s when present", name, typeName(typ)))
		return
	}
	if a.Type != typ {
		eval.ReportError(fmt.Sprintf("BoundedResult: field %q must be a %s when present", name, typeName(typ)))
		return
	}
	if a.Description == "" {
		a.Description = desc
	}
}

func ensureRequired(att *goaexpr.AttributeExpr, name string) {
	if att.Validation == nil {
		att.Validation = &goaexpr.ValidationExpr{}
	}
	for _, r := range att.Validation.Required {
		if r == name {
			return
		}
	}
	att.Validation.Required = append(att.Validation.Required, name)
}

func hasRequired(att *goaexpr.AttributeExpr, name string) bool {
	if att == nil || att.Validation == nil {
		return false
	}
	for _, r := range att.Validation.Required {
		if r == name {
			return true
		}
	}
	return false
}

func isValidBoundField(att *goaexpr.AttributeExpr, name string, typ goaexpr.DataType) bool {
	a := att.Find(name)
	if a == nil || a.Type == nil || a.Type == goaexpr.Empty {
		return false
	}
	return a.Type == typ
}

func isValidOptionalBoundField(att *goaexpr.AttributeExpr, name string, typ goaexpr.DataType) bool {
	a := att.Find(name)
	if a == nil {
		return false
	}
	if a.Type == nil || a.Type == goaexpr.Empty {
		return false
	}
	return a.Type == typ && !hasRequired(att, name)
}

func typeName(t goaexpr.DataType) string {
	switch t {
	case goaexpr.Int:
		return "Int"
	case goaexpr.Boolean:
		return "Boolean"
	case goaexpr.String:
		return "String"
	default:
		return "type"
	}
}
