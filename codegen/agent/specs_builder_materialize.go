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
func (b *toolSpecBuilder) materialize(typeName string, att *goaexpr.AttributeExpr, scope *codegen.NameScope, defineType bool) (tt *goaexpr.AttributeExpr, defLine string, fullRef string, imports []*codegen.ImportSpec) {
	if att.Type == goaexpr.Empty {
		// Synthesize a concrete, named empty payload type so templates
		// always have a valid type reference. Using an alias keeps
		// pointer/value semantics straightforward and avoids generating
		// unnecessary struct declarations.
		return att, typeName + " = struct{}", typeName, nil
	}

	// Base imports from attribute metadata and locations
	imports = gatherAttributeImports(b.genpkg, att)

	// Use Goa's type definition helpers to compute RHS of the type definition,
	// qualifying service-local user types against the owning service package.
	switch dt := att.Type.(type) {
	case goaexpr.UserType:
		loc := codegen.UserTypeLocation(dt)
		if loc != nil && loc.PackageName() != "" && loc.RelImportPath != "" {
			// External user type: qualify explicitly with the declared package
			// alias to ensure the reference is properly qualified in generated code.
			pkg := loc.PackageName()
			rhs := scope.GoTypeDefWithTargetPkg(att, false, true, pkg)
			defLine = typeName + " = " + rhs
			// Refer to the alias type name within the local specs package.
			fullRef = typeName
			break
		}

		// Service-local user type (no union): alias to its underlying composite/value
		// without qualifying with the service package. For tool aliases we materialize
		// a local struct where fields carry json struct tags that mirror the original
		// field names so that encoding/json produces payloads consistent with the tool
		// schema even when the design did not set explicit JSON struct tags. Nested
		// user types referenced by the composite are materialized locally by
		// ensureNestedLocalTypes.
		rhs := scope.GoTypeDef(cloneWithJSONTags(dt.Attribute()), false, true)
		defLine = typeName + " = " + rhs
		fullRef = typeName
	case *goaexpr.Array:
		// Build alias to composite; if self-referential, introduce element helper.
		comp := scope.GoTypeDef(att, false, true)
		if strings.Contains(comp, typeName) {
			elemName := typeName + "Item"
			elemKey := "name:" + elemName
			if _, exists := b.types[elemKey]; !exists {
				elemComp := scope.GoTypeDef(dt.ElemType, false, true)
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
		comp := scope.GoTypeDef(att, false, true)
		if strings.Contains(comp, typeName) {
			valName := typeName + "Value"
			valKey := "name:" + valName
			if _, exists := b.types[valKey]; !exists {
				valComp := scope.GoTypeDef(dt.ElemType, false, true)
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
			keyRef := scope.GoTypeDef(dt.KeyType, false, true)
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
		// referenced locally. Do not apply default-based pointer elision here so
		// validation pointer semantics stay aligned with generated field types.
		rhs := scope.GoTypeDef(cloneWithJSONTags(att), false, false)
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
	tt = att
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
		tn += "Sidecar"
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
		genpkg:      genpkg,
		service:     svc,
		svcScope:    scope,
		svcImports:  svcImports,
		types:       make(map[string]*typeData),
		helperScope: scope,
		unions:      make(map[string]*service.UnionTypeData),
	}
}

// ensureNestedLocalTypes walks the attribute and materializes local aliases for
// any nested service-local user types (types without explicit package location).
// This avoids unqualified references to service-only types that are not
// generated in the specs package.
func (b *toolSpecBuilder) ensureNestedLocalTypes(scope *codegen.NameScope, att *goaexpr.AttributeExpr) {
	_ = codegen.Walk(att, func(a *goaexpr.AttributeExpr) error {
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
			return nil
		}
		comp := scope.GoTypeDef(ut.Attribute(), false, true)
		// If a type with the same name already exists (e.g., the top-level tool
		// payload/result alias), only emit a nested helper when the shapes differ.
		// This avoids spurious <Name>2 helpers while still allowing disambiguation
		// for genuine collisions (same Go name, different attribute graph).
		if existing, exists := b.types["name:"+name]; exists && existing != nil {
			if existing.Def == name+" = "+comp || existing.Def == name+" "+comp {
				return nil
			}
		}
		// Use HashedUnique to ensure stable, deterministic naming: the same
		// user type always gets the same unique name even across multiple calls.
		// When the base name collides with pre-registered tool constant names
		// (which are derived by trimming Payload/Result suffixes), HashedUnique
		// will automatically append a suffix to make it unique.
		uniqueName := scope.HashedUnique(ut, name)
		key := "name:" + uniqueName
		if _, exists := b.types[key]; exists {
			return nil
		}
		// Alias to the underlying composite/value shape for the nested type.
		// Use GoTypeDef to inline the concrete shape instead of referencing the
		// user type name, avoiding circular aliases.
		td := &typeData{
			Key:         key,
			TypeName:    uniqueName,
			Doc:         uniqueName + " is a helper type materialized for nested references.",
			Def:         uniqueName + " = " + comp,
			FullRef:     uniqueName,
			NeedType:    true,
			TypeImports: gatherAttributeImports(b.genpkg, ut.Attribute()),
		}
		b.types[key] = td
		return nil
	})
}

// renameCollidingNestedUserTypes rewrites nested service-local user types when
// their public names collide with tool-facing types emitted in the specs package.
func (b *toolSpecBuilder) renameCollidingNestedUserTypes(att *goaexpr.AttributeExpr, scope *codegen.NameScope) *goaexpr.AttributeExpr {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return att
	}
	// Clone so we never mutate shared design expressions. Do NOT add JSON tags
	// here: this pass is name-only and must not alter the emitted shape.
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
		// Determine the base name for this user type.
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
		existing, exists := b.types["name:"+baseName]
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

// materializeJSONUserTypes walks the attribute and returns a root user type
// representing the JSON decode-body (server body style) along with all nested
// helper user types. No inline structs are produced.
