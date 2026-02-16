package codegen

import (
	"path"
	"strings"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	goaexpr "goa.design/goa/v3/expr"
)

// materialize computes the Go type definition (or alias), its fully-qualified
// reference, and required imports for the given attribute.
//
// useDefault controls default-value pointer elision for primitive fields in
// object types (see goa expr.AttributeExpr.IsPrimitivePointer).
func (b *toolSpecBuilder) materialize(typeName string, att *goaexpr.AttributeExpr, scope *codegen.NameScope, defineType bool, ptr bool, useDefault bool) (tt *goaexpr.AttributeExpr, defLine string, fullRef string, imports []*codegen.ImportSpec) {
	if att.Type == goaexpr.Empty {
		// Synthesize a concrete, named empty payload type so templates
		// always have a valid type reference. Using an alias keeps
		// pointer/value semantics straightforward and avoids generating
		// unnecessary struct declarations.
		if defineType {
			return att, typeName + " struct{}", typeName, nil
		}
		return att, typeName + " = struct{}", typeName, nil
	}

	// Base imports from attribute metadata and locations
	imports = gatherAttributeImports(b.genpkg, att)

	// Use Goa's type definition helpers to compute RHS of the type definition.
	switch dt := att.Type.(type) {
	case goaexpr.UserType:
		// Materialize a local shape based on the underlying attribute.
		// The tool specs package is always the "current" package for these types.
		// Do NOT let struct:pkg:path on the *source* user type influence the
		// qualification decisions inside inline structs: it would treat nested
		// shared types (pkg "types") as if they were local and emit
		// `TaskDefinition` instead of `types.TaskDefinition`.
		rhs := scope.GoTypeDef(stripStructPkgMeta(dt.Attribute()), ptr, useDefault)
		if defineType {
			defLine = typeName + " " + rhs
		} else {
			defLine = typeName + " = " + rhs
		}
		fullRef = typeName
		// Use the underlying attribute for schema/validation walks so we don't emit
		// validators for the *design* user type name (which does not exist in the
		// generated specs package).
		tt = dt.Attribute()
	case *goaexpr.Array:
		// Build alias to composite; if self-referential, introduce element helper.
		comp := scope.GoTypeDef(att, ptr, useDefault)
		if strings.Contains(comp, typeName) {
			elemName := typeName + "Item"
			elemKey := "name:" + elemName
			if _, exists := b.types[elemKey]; !exists {
				elemComp := scope.GoTypeDef(dt.ElemType, ptr, useDefault)
				b.types[elemKey] = &typeData{
					Key:           elemKey,
					TypeName:      elemName,
					Doc:           elemName + " is a helper element for " + typeName + ".",
					Def:           elemName + " = " + elemComp,
					FullRef:       elemName,
					NeedType:      true,
					TypeImports:   gatherAttributeImports(b.genpkg, dt.ElemType),
					ExportedCodec: "",
					GenericCodec:  "",
					GenerateCodec: false,
				}
			}
			defLine = typeName + " = []" + elemName
			fullRef = typeName
		} else {
			defLine = typeName + " = " + comp
			fullRef = typeName
		}
	case *goaexpr.Map:
		comp := scope.GoTypeDef(att, ptr, useDefault)
		if strings.Contains(comp, typeName) {
			valName := typeName + "Value"
			valKey := "name:" + valName
			if _, exists := b.types[valKey]; !exists {
				valComp := scope.GoTypeDef(dt.ElemType, ptr, useDefault)
				b.types[valKey] = &typeData{
					Key:           valKey,
					TypeName:      valName,
					Doc:           valName + " is a helper value for " + typeName + ".",
					Def:           valName + " = " + valComp,
					FullRef:       valName,
					NeedType:      true,
					TypeImports:   gatherAttributeImports(b.genpkg, dt.ElemType),
					ExportedCodec: "",
					GenericCodec:  "",
					GenerateCodec: false,
				}
			}
			keyRef := scope.GoTypeDef(dt.KeyType, ptr, useDefault)
			defLine = typeName + " = map[" + keyRef + "]" + valName
			fullRef = typeName
		} else {
			defLine = typeName + " = " + comp
			fullRef = typeName
		}
	case *goaexpr.Union:
		// Unions are generated as named sum types. Alias the tool-facing type to
		// the union type name so codecs and schemas can refer to the tool name
		// while preserving the union method set.
		rhs := scope.GoTypeDef(att, false, true)
		defLine = typeName + " = " + rhs
		fullRef = typeName
	case *goaexpr.Object, goaexpr.CompositeExpr:
		// Alias to inline struct definition using Goa's type def helper without
		// service package qualification so nested service user types are
		// referenced locally.
		rhs := scope.GoTypeDef(att, ptr, useDefault)
		if defineType {
			// Emit a concrete struct type so callers can attach methods (for
			// example, agent.BoundedResult on bounded tool result types).
			defLine = typeName + " " + rhs
		} else {
			defLine = typeName + " = " + rhs
		}
		fullRef = typeName
	default:
		// Primitives (and other scalar types): alias to the underlying type so
		// the specs package always exposes a stable, tool-scoped type name.
		rhs := scope.GoTypeDef(att, false, true)
		defLine = typeName + " = " + rhs
		fullRef = typeName
	}
	if tt == nil {
		tt = att
	}
	return tt, defLine, fullRef, imports
}

// stableTypeKey returns a deterministic cache key for the tool-facing type name
// within a toolset scope.
func stableTypeKey(tool *ToolData, usage typeUsage) string {
	if tool == nil {
		return ""
	}
	tn := codegen.Goify(tool.Name, true)
	switch usage {
	case usagePayload:
		tn += "Payload"
	case usageResult:
		tn += "Result"
	case usageSidecar:
		tn += "ServerData"
	}
	scope := ""
	if tool.Toolset != nil {
		scope = tool.Toolset.QualifiedName
	}
	return "scope:" + scope + "/name:" + tn
}

// newToolSpecsData constructs an empty toolSpecsData container.
func newToolSpecsData(genpkg string, svc *service.Data) *toolSpecsData {
	return &toolSpecsData{
		svc:    svc,
		genpkg: genpkg,
		types:  make(map[string]*typeData),
	}
}

// newToolSpecBuilder constructs a builder for one specs package generation run.
func newToolSpecBuilder(genpkg string, svc *service.Data) *toolSpecBuilder {
	// Use a fresh NameScope per specs package generation. This matches Goa’s
	// transport generators and avoids accumulating suffixes across multiple
	// generator passes (e.g., Find3).
	scope := codegen.NewNameScope()
	svcImports := make(map[string]*codegen.ImportSpec)
	for _, im := range svc.UserTypeImports {
		if im.Path == "" {
			continue
		}
		alias := im.Name
		if alias == "" {
			alias = path.Base(im.Path)
		}
		svcImports[alias] = im
	}
	return &toolSpecBuilder{
		genpkg:                   genpkg,
		service:                  svc,
		svcScope:                 scope,
		svcImports:               svcImports,
		types:                    make(map[string]*typeData),
		helperScope:              scope,
		unions:                   make(map[string]*service.UnionTypeData),
		codecTransformHelperKeys: make(map[string]struct{}),
	}
}

// ensureNestedLocalTypes walks att and materializes local aliases for nested
// *service-local* user types (types without explicit package location).
//
// Public tool-facing types are service-level shapes: they should keep any
// design-owned type locations (`struct:pkg:path`) intact so nested types can be
// referenced via imports (e.g. `types.FacilityFact`). Only service-local types
// that would otherwise be unqualified and undefined in the specs package are
// localized.
func (b *toolSpecBuilder) ensureNestedLocalTypes(scope *codegen.NameScope, att *goaexpr.AttributeExpr, ptr bool, useDefault bool) *goaexpr.AttributeExpr {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return att
	}
	cloned := goaexpr.DupAtt(att)
	localByID := make(map[string]goaexpr.UserType)
	localsByName := make(map[string]*goaexpr.UserTypeExpr)
	_ = codegen.Walk(cloned, func(a *goaexpr.AttributeExpr) error {
		if a == nil || a.Type == nil || a.Type == goaexpr.Empty {
			return nil
		}
		// Do not rewrite the root type itself; only localize nested user types.
		if a == cloned {
			return nil
		}
		ut, ok := a.Type.(goaexpr.UserType)
		if !ok || ut == nil {
			return nil
		}
		// Only localize service-local user types. External user types are referenced
		// via their package imports and do not need local aliases here.
		if codegen.UserTypeLocation(ut) != nil {
			return nil
		}

		// Determine the type name for the nested user type.
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

		id := ut.ID()
		if id != "" {
			if cached, ok := localByID[id]; ok && cached != nil {
				a.Type = cached
				return nil
			}
		}
		base := stripStructPkgMeta(goaexpr.DupAtt(ut.Attribute()))
		local := &goaexpr.UserTypeExpr{
			AttributeExpr: base,
			// IMPORTANT (Goa-style): do not pre-reserve a helper name using the
			// *source* user type as the NameScope key. NameScope keys user types by
			// their hash (which includes the type name). If we reserve "Foo" under
			// the hash for the design user type, later references to the *helper*
			// user type (a distinct hash) become "Foo2" while the emitted definition
			// stays "Foo", producing undefined identifiers in generated code.
			//
			// Instead, set the helper's own base name and let NameScope derive the
			// final symbol for both references and emitted definitions.
			TypeName: name,
		}
		if id != "" {
			localByID[id] = local
		}
		localsByName[ut.Hash()] = local
		a.Type = local
		return nil
	})

	// Emit local nested helpers after the attribute graph has been fully rewritten.
	// This guarantees helper defs reference other local helpers (not external
	// service packages like `gen/types`).
	for _, ut := range localsByName {
		name := scope.GoTypeName(&goaexpr.AttributeExpr{Type: ut})
		key := "name:" + name
		if _, exists := b.types[key]; exists {
			continue
		}
		b.types[key] = &typeData{
			Key:         key,
			TypeName:    name,
			Doc:         name + " is a helper type materialized for nested references.",
			Def:         name + " = " + scope.GoTypeDef(ut.AttributeExpr, ptr, useDefault),
			FullRef:     name,
			NeedType:    true,
			TypeImports: gatherAttributeImports(b.genpkg, ut.AttributeExpr),
		}
	}
	return cloned
}

// ensureNestedLocalTransportTypes rewrites nested user type references in att
// to point at locally materialized transport types and records those type
// definitions for emission in codecs.go.
//
// Transport types are internal: they exist only to decode/validate JSON using
// HTTP server-body conventions (pointer primitives + explicit json field names)
// before converting to the public tool types used throughout the codebase.
func (b *toolSpecBuilder) ensureNestedLocalTransportTypes(scope *codegen.NameScope, att *goaexpr.AttributeExpr) *goaexpr.AttributeExpr {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return att
	}
	cloned := goaexpr.DupAtt(att)
	localByID := make(map[string]goaexpr.UserType)
	localsByName := make(map[string]*goaexpr.UserTypeExpr)
	_ = codegen.Walk(cloned, func(a *goaexpr.AttributeExpr) error {
		if a == nil || a.Type == nil || a.Type == goaexpr.Empty {
			return nil
		}
		// Do not rewrite the root type itself; only localize nested user types.
		if a == cloned {
			return nil
		}
		ut, ok := a.Type.(goaexpr.UserType)
		if !ok || ut == nil {
			return nil
		}

		// Determine the type name for the nested user type.
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

		// IMPORTANT (Goa-style): do not pre-reserve a helper name using the *source*
		// user type as the scope key. Goa's NameScope keys user types by (hashed) name.
		// Reserving "FooTransport" under the hash for "Foo" causes later references to
		// the helper user type (hash "FooTransport") to become "FooTransport2" while the
		// emitted definition stays "FooTransport".
		//
		// Instead, insert a helper user type whose *own name* is the intended base
		// ("FooTransport") and let NameScope consistently derive the final symbol for
		// both references and emitted definitions.
		uniqueName := name + "Transport"
		id := ut.ID()
		if id != "" {
			if cached, ok := localByID[id]; ok && cached != nil {
				a.Type = cached
				return nil
			}
		}
		base := stripStructPkgMeta(goaexpr.DupAtt(ut.Attribute()))
		normalizeTransportAttrRecursive(base)
		local := &goaexpr.UserTypeExpr{
			AttributeExpr: base,
			TypeName:      uniqueName,
		}
		if id != "" {
			localByID[id] = local
		}
		localsByName[uniqueName] = local
		a.Type = local
		return nil
	})

	for _, ut := range localsByName {
		name := scope.GoTypeName(&goaexpr.AttributeExpr{Type: ut})
		key := "transport:" + name
		if _, exists := b.types[key]; exists {
			continue
		}
		// Match Goa’s validation behavior: only treat values as pointers for
		// validation purposes when the type is non-primitive (objects/unions, etc.).
		// Alias primitives (e.g. type Time string) must validate as values, otherwise
		// Goa’s ValidationCode will emit nil checks and dereferences that can’t compile.
		httpctx := codegen.NewAttributeContext(!goaexpr.IsPrimitive(ut), false, false, "", scope)
		vcode := codegen.ValidationCode(ut.AttributeExpr, ut, httpctx, true, goaexpr.IsAlias(ut), false, "body")
		var vlines []string
		if strings.TrimSpace(vcode) != "" {
			vlines = strings.Split(vcode, "\n")
		}
		tref := scope.GoTypeRef(&goaexpr.AttributeExpr{Type: ut})
		b.types[key] = &typeData{
			Key:                    key,
			TypeName:               name,
			Doc:                    name + " is an internal transport helper type materialized for nested references.",
			NeedType:               false,
			IsToolType:             false,
			GenerateCodec:          false,
			TypeImports:            gatherAttributeImports(b.genpkg, ut.AttributeExpr),
			TransportTypeName:      name,
			TransportDef:           name + " " + scope.GoTypeDef(ut.AttributeExpr, true, false),
			TransportImports:       gatherAttributeImports(b.genpkg, ut.AttributeExpr),
			TransportValidationSrc: vlines,
			TransportTypeRef:       tref,
			TransportPointer:       strings.HasPrefix(tref, "*"),
		}
	}

	return cloned
}
