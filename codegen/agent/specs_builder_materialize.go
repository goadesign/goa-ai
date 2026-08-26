// Package codegen turns Goa types into the public Go types and HTTP decoding types
// written for generated tools and completions.
package codegen

import (
	"fmt"
	"strings"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	goaexpr "goa.design/goa/v3/expr"
)

// buildTypeDefinition builds one Go type definition and the name used to refer
// to it.
//
// useDefault controls whether a primitive field with a default is stored as a
// value or a pointer.
func (b *toolSpecBuilder) buildTypeDefinition(typeName string, att *goaexpr.AttributeExpr, scope *codegen.NameScope, defineType bool, ptr bool, useDefault bool) (tt *goaexpr.AttributeExpr, defLine string, fullRef string) {
	if att.Type == goaexpr.Empty {
		// Give an empty payload a named empty struct so generated functions can
		// always refer to a concrete type.
		if defineType {
			return att, typeName + " struct{}", typeName
		}
		return att, typeName + " = struct{}", typeName
	}

	switch dt := att.Type.(type) {
	case goaexpr.UserType:
		// Ignore the source type's package setting when defining the new local
		// type. Nested types keep their package settings, so a type such as
		// types.TaskDefinition keeps the types prefix.
		rhs := scope.GoTypeDef(stripStructPkgMeta(dt.Attribute()), ptr, useDefault)
		if defineType {
			defLine = typeName + " " + rhs
		} else {
			defLine = typeName + " = " + rhs
		}
		fullRef = typeName
		// Use the underlying attribute for schema and validation so we do not write
		// validators for the *design* user type name (which does not exist in the
		// generated specs package).
		tt = dt.Attribute()
	case *goaexpr.Array, *goaexpr.Map:
		comp := scope.GoTypeDef(att, ptr, useDefault)
		defLine = typeName + " = " + comp
		fullRef = typeName
	case *goaexpr.Union:
		rhs := scope.GoTypeDef(att, false, true)
		defLine = typeName + " = " + rhs
		fullRef = typeName
	case *goaexpr.Object, goaexpr.CompositeExpr:
		rhs := scope.GoTypeDef(att, ptr, useDefault)
		if defineType {
			defLine = typeName + " " + rhs
		} else {
			defLine = typeName + " = " + rhs
		}
		fullRef = typeName
	default:
		rhs := scope.GoTypeDef(att, false, true)
		defLine = typeName + " = " + rhs
		fullRef = typeName
	}
	if tt == nil {
		tt = att
	}
	return tt, defLine, fullRef
}

// stableTypeKey returns the exact DSL identity of one planned contract value.
func stableTypeKey(owner *contractTypeOwner, usage typeUsage, qualifier string) specTypeKey {
	if owner == nil {
		return specTypeKey{}
	}
	return specTypeKey{
		OwnerKind:     owner.Kind,
		ScopeName:     owner.ScopeName,
		QualifiedName: owner.QualifiedName,
		Name:          owner.Name,
		Usage:         usage,
		Qualifier:     qualifier,
	}
}

// orderKey returns an unambiguous text form used only to order declarations.
func (k specTypeKey) orderKey() string {
	return fmt.Sprintf(
		"%d:%s%d:%s%d:%s%d:%s%d:%s%d:%s",
		len(k.OwnerKind), k.OwnerKind,
		len(k.ScopeName), k.ScopeName,
		len(k.QualifiedName), k.QualifiedName,
		len(k.Name), k.Name,
		len(k.Usage), k.Usage,
		len(k.Qualifier), k.Qualifier,
	)
}

// newToolSpecsData constructs an empty toolSpecsData container.
func newToolSpecsData(genpkg string, svc *service.Data) *toolSpecsData {
	return &toolSpecsData{
		svc:    svc,
		genpkg: genpkg,
		types:  make(map[string]*typeData),
	}
}

// newToolSpecBuilder creates one builder that reads the final names saved for
// the public package and its HTTP package.
func newToolSpecBuilder(svc *service.Data, planned *toolSpecsPackagePlan, api *goaexpr.APIExpr) *toolSpecBuilder {
	publicScope := planned.public.Scope().Fork()
	transportScope := planned.transport.Scope().Fork()
	return &toolSpecBuilder{
		service:              svc,
		api:                  api,
		publicScope:          publicScope,
		transportScope:       transportScope,
		publicPackage:        planned.public,
		transportPackage:     planned.transport,
		publicUnionErrors:    planned.publicUnionErrors,
		transportUnionErrors: planned.transportUnionErrors,
		planned:              planned,
		svcScope:             publicScope,
		contractTypes:        make(map[specTypeKey]*typeData),
		types:                make(map[string]*typeData),
		helperScope:          publicScope,
		unions:               make(map[codegen.UnionDeclarationID]*unionTypeData),
		transportUnions:      make(map[codegen.UnionDeclarationID]*unionTypeData),
	}
}

// materializeNestedLocalTypes writes the public nested types saved by the plan.
func (b *toolSpecBuilder) materializeNestedLocalTypes(scope *codegen.NameScope, locals []*localizedType, ptr, useDefault bool) {
	for _, localized := range locals {
		ut := localized.generated
		name := localized.declaration.Name()
		key := "name:" + name
		if _, exists := b.types[key]; exists {
			continue
		}
		separator := " = "
		if referencesUserType(ut.AttributeExpr, ut, make(map[*goaexpr.AttributeExpr]struct{})) {
			// Go does not allow a recursive alias. Give the recursive value a
			// defined name so fields can point back to it.
			separator = " "
		}
		b.types[key] = &typeData{
			Key:      key,
			TypeName: name,
			Doc:      name + " is a nested type used by the generated JSON contract.",
			Def:      name + separator + scope.GoTypeDef(ut.AttributeExpr, ptr, useDefault),
			FullRef:  name,
			NeedType: true,
		}
	}
}

// referencesUserType reports whether a nested named type eventually points
// back to target. The walk stops at attributes it has already checked.
func referencesUserType(att *goaexpr.AttributeExpr, target goaexpr.UserType, seen map[*goaexpr.AttributeExpr]struct{}) bool {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return false
	}
	if _, ok := seen[att]; ok {
		return false
	}
	seen[att] = struct{}{}
	switch actual := att.Type.(type) {
	case goaexpr.UserType:
		if actual == target {
			return true
		}
		return referencesUserType(actual.Attribute(), target, seen)
	case *goaexpr.Object:
		for _, field := range *actual {
			if referencesUserType(field.Attribute, target, seen) {
				return true
			}
		}
	case *goaexpr.Array:
		return referencesUserType(actual.ElemType, target, seen)
	case *goaexpr.Map:
		return referencesUserType(actual.KeyType, target, seen) ||
			referencesUserType(actual.ElemType, target, seen)
	case *goaexpr.Union:
		for _, field := range actual.Values {
			if field != nil && referencesUserType(field.Attribute, target, seen) {
				return true
			}
		}
	}
	return false
}

// materializeNestedTransportTypes writes the nested types used to decode JSON.
func (b *toolSpecBuilder) materializeNestedTransportTypes(scope *codegen.NameScope, locals []*localizedType) {
	for _, localized := range locals {
		ut := localized.generated
		name := localized.declaration.Name()
		validateFunc := b.planned.transportValidators[localized.source].Name()
		key := "transport:" + name
		if _, exists := b.types[key]; exists {
			continue
		}
		// Primitive aliases stay values at the top level. Objects and unions stay
		// pointers so validation can distinguish a missing value from an empty one.
		httpctx := modelJSONTransportContext(scope, !goaexpr.IsPrimitive(ut), "")
		vcode := codegen.ValidationCode(ut.AttributeExpr, ut, httpctx, true, goaexpr.IsAlias(ut), false, "body")
		var vlines []string
		if strings.TrimSpace(vcode) != "" {
			vlines = strings.Split(vcode, "\n")
		}
		tref := scope.GoTypeRef(&goaexpr.AttributeExpr{Type: ut})
		transportCtx := modelJSONTransportContext(scope, true, "")
		b.types[key] = &typeData{
			Key:                    key,
			TypeName:               name,
			Doc:                    name + " is a nested type used while decoding tool JSON.",
			NeedType:               false,
			IsToolType:             false,
			GenerateCodec:          false,
			TransportTypeName:      name,
			ValidateFunc:           validateFunc,
			TransportDef:           name + " " + transportTypeDef(scope, ut.AttributeExpr, transportCtx),
			TransportValidationSrc: vlines,
			TransportTypeRef:       tref,
			TransportPointer:       strings.HasPrefix(tref, "*"),
		}
	}
}

// localizeNestedTypes copies att and replaces nested service types with types
// that will be written in the selected output package. HTTP helper types also
// receive JSON field names and pointer rules used by request decoding.
func localizeNestedTypes(att *goaexpr.AttributeExpr, transport bool, sourceTypes map[goaexpr.UserType]goaexpr.UserType) (*goaexpr.AttributeExpr, []*localizedType) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return att, nil
	}
	cloned := goaexpr.DupAtt(att)
	localBySource := make(map[goaexpr.UserType]*localizedType)
	var locals []*localizedType
	_ = codegen.Walk(cloned, func(a *goaexpr.AttributeExpr) error {
		if a == nil || a.Type == nil || a.Type == goaexpr.Empty {
			return nil
		}
		if a == cloned {
			return nil
		}
		ut, ok := a.Type.(goaexpr.UserType)
		if !ok || ut == nil {
			return nil
		}
		if !transport && codegen.UserTypeLocation(ut) != nil {
			return nil
		}

		var name string
		switch u := ut.(type) {
		case *goaexpr.UserTypeExpr:
			name = codegen.Goify(u.TypeName, true)
		case *goaexpr.ResultTypeExpr:
			name = codegen.Goify(u.TypeName, true)
		default:
			return nil
		}
		if name == "" {
			name = codegen.Goify(ut.Name(), true)
		}
		if name == "" {
			return nil
		}

		if transport {
			name += "Transport"
		}
		source := ut.Origin()
		if original := sourceTypes[source]; original != nil {
			source = original
		}
		if cached := localBySource[source]; cached != nil {
			a.Type = cached.generated
			return nil
		}
		base := stripStructPkgMeta(goaexpr.DupAtt(ut.Attribute()))
		if transport {
			normalizeModelJSONTransportAttrRecursive(base)
		}
		generated := &goaexpr.UserTypeExpr{
			AttributeExpr: base,
			TypeName:      name,
		}
		local := &localizedType{source: source, generated: generated}
		localBySource[source] = local
		locals = append(locals, local)
		a.Type = generated
		return nil
	})
	return cloned, locals
}
