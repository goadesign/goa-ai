package codegen

import (
	"fmt"
	"strings"

	"goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

func parseSidecarArtifactTypeName(def string) (string, bool) {
	const needle = "Artifact *"
	i := strings.Index(def, needle)
	if i < 0 {
		return "", false
	}
	s := def[i+len(needle):]
	// Consume identifier chars.
	j := 0
	for j < len(s) {
		c := s[j]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
			j++
			continue
		}
		break
	}
	if j == 0 {
		return "", false
	}
	return s[:j], true
}

func rewriteCollidingNestedUserTypes(att *goaexpr.AttributeExpr, scope *codegen.NameScope, types map[string]*typeData) *goaexpr.AttributeExpr {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty || scope == nil || types == nil {
		return att
	}
	cloned := goaexpr.DupAtt(att)
	_ = codegen.Walk(cloned, func(a *goaexpr.AttributeExpr) error {
		if a == nil || a.Type == nil || a.Type == goaexpr.Empty {
			return nil
		}
		ut, ok := a.Type.(goaexpr.UserType)
		if !ok || ut == nil {
			return nil
		}
		// Skip types that specify an external package location.
		if loc := codegen.UserTypeLocation(ut); loc != nil && loc.RelImportPath != "" {
			return nil
		}
		var baseName string
		switch u := ut.(type) {
		case *goaexpr.UserTypeExpr:
			baseName = codegen.Goify(u.TypeName, true)
		case *goaexpr.ResultTypeExpr:
			baseName = codegen.Goify(u.TypeName, true)
		default:
			return nil
		}
		if baseName == "" {
			return nil
		}
		existing, exists := types["name:"+baseName]
		if !exists || existing == nil || !existing.IsToolType {
			return nil
		}
		comp := scope.GoTypeDef(ut.Attribute(), false, true)
		if existing.Def == baseName+" = "+comp || existing.Def == baseName+" "+comp {
			return nil
		}
		uniqueName := scope.HashedUnique(ut, baseName)
		if uniqueName == baseName {
			uniqueName = scope.Unique(baseName)
		}
		switch u := ut.(type) {
		case *goaexpr.UserTypeExpr:
			uu := *u
			uu.TypeName = uniqueName
			a.Type = &uu
		case *goaexpr.ResultTypeExpr:
			uu := *u
			uu.TypeName = uniqueName
			a.Type = &uu
		}
		return nil
	})
	return cloned
}

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
