package codegen

import (
	"fmt"

	"goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

// rewriteNestedLocalUserTypes walks the attribute and replaces service-local user
// types (types without an explicit struct:pkg:path locator) with local user types
// that use the same public type names. This ensures that transforms targeting
// specs-local aliases reference the emitted helper types (e.g., App, AppInput)
// instead of inventing new names.
func rewriteNestedLocalUserTypes(att *goaexpr.AttributeExpr) *goaexpr.AttributeExpr {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return att
	}
	switch dt := att.Type.(type) {
	case goaexpr.UserType:
		// Preserve external locators; rewrite only service-local user types.
		if loc := codegen.UserTypeLocation(dt); loc != nil && loc.RelImportPath != "" {
			// Recurse into attribute to update children.
			return &goaexpr.AttributeExpr{Type: dt}
		}
		// Compute the local public name from the user type.
		name := ""
		var base *goaexpr.AttributeExpr
		switch u := dt.(type) {
		case *goaexpr.UserTypeExpr:
			name = u.TypeName
			base = u.Attribute()
		case *goaexpr.ResultTypeExpr:
			name = u.TypeName
			base = u.Attribute()
		default:
			return att
		}
		if base == nil || base.Type == nil {
			panic(fmt.Sprintf("agent/generate: user type %q has nil attribute/type (dt=%T)", name, dt))
		}
		// Recurse into the underlying attribute, do not propagate struct:pkg:path.
		dup := *base
		if dup.Meta != nil {
			delete(dup.Meta, "struct:pkg:path")
		}
		return &goaexpr.AttributeExpr{Type: &goaexpr.UserTypeExpr{
			AttributeExpr: rewriteNestedLocalUserTypes(&dup),
			TypeName:      name,
		}}
	case *goaexpr.Array:
		return &goaexpr.AttributeExpr{Type: &goaexpr.Array{ElemType: rewriteNestedLocalUserTypes(dt.ElemType)}}
	case *goaexpr.Map:
		return &goaexpr.AttributeExpr{Type: &goaexpr.Map{
			KeyType:  rewriteNestedLocalUserTypes(dt.KeyType),
			ElemType: rewriteNestedLocalUserTypes(dt.ElemType),
		}}
	case *goaexpr.Object:
		obj := &goaexpr.Object{}
		for _, nat := range *dt {
			if nat == nil || nat.Attribute == nil || nat.Attribute.Type == nil {
				panic(fmt.Sprintf("agent/generate: object field %q in %T has nil attribute/type", nat.Name, dt))
			}
			dup := rewriteNestedLocalUserTypes(nat.Attribute)
			*obj = append(*obj, &goaexpr.NamedAttributeExpr{
				Name:      nat.Name,
				Attribute: dup,
			})
		}
		return &goaexpr.AttributeExpr{Type: obj, Description: att.Description, Docs: att.Docs, Validation: att.Validation}
	case *goaexpr.Union:
		// Leave unions unchanged.
		return att
	default:
		return att
	}
}
