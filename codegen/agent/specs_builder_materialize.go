// Package codegen turns Goa types into the public Go types and HTTP decoding types
// written for generated tools and completions.
package codegen

import (
	"sort"
	"strings"

	"goa.design/goa-ai/codegen/shared"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	goaexpr "goa.design/goa/v3/expr"
)

// buildTypeDefinition builds one Go type definition, the name used to refer to
// it, and its imports.
//
// useDefault controls whether a primitive field with a default is stored as a
// value or a pointer.
func (b *toolSpecBuilder) buildTypeDefinition(typeName string, att *goaexpr.AttributeExpr, scope *codegen.NameScope, defineType bool, ptr bool, useDefault bool) (tt *goaexpr.AttributeExpr, defLine string, fullRef string, imports []*codegen.ImportSpec) {
	if att.Type == goaexpr.Empty {
		// Give an empty payload a named empty struct so generated functions can
		// always refer to a concrete type.
		if defineType {
			return att, typeName + " struct{}", typeName, nil
		}
		return att, typeName + " = struct{}", typeName, nil
	}

	imports = shared.GatherAttributeImports(b.genpkg, att)

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
	return tt, defLine, fullRef, imports
}

// stableTypeKey returns a map key made from the toolset, tool, value kind, and
// server-data kind.
func stableTypeKey(owner *contractTypeOwner, usage typeUsage, qualifier string) string {
	if owner == nil {
		return ""
	}
	tn := codegen.Goify(owner.Name, true)
	switch usage {
	case usagePayload:
		tn += "Payload"
	case usageResult:
		tn += "Result"
	case usageServerData:
		if qualifier != "" {
			tn += codegen.Goify(qualifier, true)
		}
		tn += "ServerData"
	}
	return "scope:" + owner.ScopeName + "/name:" + tn
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
func newToolSpecBuilder(genpkg string, svc *service.Data, planned *toolSpecsPackagePlan, api *goaexpr.APIExpr) *toolSpecBuilder {
	publicScope := planned.public.Scope().Fork()
	transportScope := planned.transport.Scope().Fork()
	return &toolSpecBuilder{
		genpkg:               genpkg,
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
		types:                make(map[string]*typeData),
		helperScope:          publicScope,
		unions:               make(map[codegen.UnionTypeID]*unionTypeData),
		transportUnions:      make(map[codegen.UnionTypeID]*unionTypeData),
	}
}

// materializeNestedLocalTypes writes the public nested types saved by the plan.
func (b *toolSpecBuilder) materializeNestedLocalTypes(scope *codegen.NameScope, locals []*goaexpr.UserTypeExpr, ptr, useDefault bool) {
	for _, ut := range locals {
		name := scope.GoTypeName(&goaexpr.AttributeExpr{Type: ut})
		key := "name:" + name
		if _, exists := b.types[key]; exists {
			continue
		}
		b.types[key] = &typeData{
			Key:         key,
			TypeName:    name,
			Doc:         name + " is a nested type used by the generated JSON contract.",
			Def:         name + " = " + scope.GoTypeDef(ut.AttributeExpr, ptr, useDefault),
			FullRef:     name,
			NeedType:    true,
			TypeImports: shared.GatherAttributeImports(b.genpkg, ut.AttributeExpr),
		}
	}
}

// materializeNestedTransportTypes writes the nested types used to decode JSON.
func (b *toolSpecBuilder) materializeNestedTransportTypes(scope *codegen.NameScope, locals []*goaexpr.UserTypeExpr) {
	for _, ut := range locals {
		name := scope.GoTypeName(&goaexpr.AttributeExpr{Type: ut})
		validateFunc := b.planned.transportValidators[ut.Hash()].Name()
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
			TypeImports:            shared.GatherAttributeImports(b.genpkg, ut.AttributeExpr),
			TransportTypeName:      name,
			ValidateFunc:           validateFunc,
			TransportDef:           name + " " + transportTypeDef(scope, ut.AttributeExpr, transportCtx),
			TransportImports:       shared.GatherAttributeImports(b.genpkg, ut.AttributeExpr),
			TransportValidationSrc: vlines,
			TransportTypeRef:       tref,
			TransportPointer:       strings.HasPrefix(tref, "*"),
		}
	}
}

// localizeNestedTypes copies att and replaces nested service types with types
// that will be written in the selected output package. HTTP helper types also
// receive JSON field names and pointer rules used by request decoding.
func localizeNestedTypes(att *goaexpr.AttributeExpr, transport bool) (*goaexpr.AttributeExpr, []*goaexpr.UserTypeExpr) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return att, nil
	}
	cloned := goaexpr.DupAtt(att)
	localByID := make(map[string]goaexpr.UserType)
	localsByHash := make(map[string]*goaexpr.UserTypeExpr)
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
		id := ut.ID()
		if id != "" {
			if cached, ok := localByID[id]; ok && cached != nil {
				a.Type = cached
				return nil
			}
		}
		base := stripStructPkgMeta(goaexpr.DupAtt(ut.Attribute()))
		if transport {
			normalizeModelJSONTransportAttrRecursive(base)
		}
		local := &goaexpr.UserTypeExpr{
			AttributeExpr: base,
			TypeName:      name,
		}
		if id != "" {
			localByID[id] = local
		}
		localsByHash[local.Hash()] = local
		a.Type = local
		return nil
	})

	hashes := make([]string, 0, len(localsByHash))
	for hash := range localsByHash {
		hashes = append(hashes, hash)
	}
	sort.Strings(hashes)
	locals := make([]*goaexpr.UserTypeExpr, len(hashes))
	for index, hash := range hashes {
		locals[index] = localsByHash[hash]
	}
	return cloned, locals
}
