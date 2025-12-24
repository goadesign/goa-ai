package codegen

import (
	"encoding/json"
	"path"
	"sort"
	"strings"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	goaexpr "goa.design/goa/v3/expr"
	"goa.design/goa/v3/http/codegen/openapi"
)

// materializeJSONUserTypes builds HTTP server-body style JSON helper user types
// for att and returns the root helper plus all nested helper type definitions.
func (b *toolSpecBuilder) materializeJSONUserTypes(att *goaexpr.AttributeExpr, base string, scope *codegen.NameScope) (*goaexpr.UserTypeExpr, []*goaexpr.UserTypeExpr) {
	visited := make(map[*goaexpr.Object]*goaexpr.UserTypeExpr)
	var defs []*goaexpr.UserTypeExpr

	var build func(a *goaexpr.AttributeExpr, name string) *goaexpr.AttributeExpr
	build = func(a *goaexpr.AttributeExpr, name string) *goaexpr.AttributeExpr {
		if a == nil || a.Type == nil || a.Type == goaexpr.Empty {
			return a
		}
		switch dt := a.Type.(type) {
		case goaexpr.UserType:
			// If underlying is an object, materialize a JSON user type for it.
			if obj := goaexpr.AsObject(dt.Attribute().Type); obj != nil {
				return build(dt.Attribute(), name)
			}
			return a
		case *goaexpr.Object:
			if ut, ok := visited[dt]; ok {
				return &goaexpr.AttributeExpr{Type: ut}
			}
			// Create a new user type for this object.
			tname := scope.Unique(codegen.Goify(name, true))
			obj := &goaexpr.Object{}
			for _, nat := range *dt {
				fieldName := codegen.Goify(nat.Name, true)
				childName := name + fieldName + "JSON"
				ca := build(nat.Attribute, childName)
				*obj = append(*obj, &goaexpr.NamedAttributeExpr{Name: nat.Name, Attribute: ca})
			}
			// Do not propagate Meta from original attributes.
			ut := &goaexpr.UserTypeExpr{AttributeExpr: &goaexpr.AttributeExpr{Type: obj, Description: a.Description, Docs: a.Docs, Validation: a.Validation}, TypeName: tname}
			visited[dt] = ut
			defs = append(defs, ut)
			return &goaexpr.AttributeExpr{Type: ut}
		case *goaexpr.Array:
			// Recurse into element, materialize object as user type if needed.
			ename := name + "ItemJSON"
			elem := build(dt.ElemType, ename)
			return &goaexpr.AttributeExpr{Type: &goaexpr.Array{ElemType: elem}}
		case *goaexpr.Map:
			kname := name + "KeyJSON"
			ename := name + "ElemJSON"
			key := build(dt.KeyType, kname)
			elem := build(dt.ElemType, ename)
			return &goaexpr.AttributeExpr{Type: &goaexpr.Map{KeyType: key, ElemType: elem}}
		default:
			return a
		}
	}

	rootAttr := build(att, base)
	// Root must be a user type for consistent validation and transform flow.
	// Materialize a root user type even when the payload is not an object (for
	// example, a string or array) so the JSON helper matches the wire shape
	// without introducing wrapper objects.
	if ut, ok := rootAttr.Type.(*goaexpr.UserTypeExpr); ok {
		return ut, defs
	}
	tname := scope.Unique(codegen.Goify(base, true))
	rut := &goaexpr.UserTypeExpr{AttributeExpr: rootAttr, TypeName: tname}
	defs = append(defs, rut)
	return rut, defs
}

// buildFieldDescriptions collects dotted field-path descriptions from the provided
// attribute. It follows objects, arrays, maps and user types, trimming any leading
// root qualifiers at error construction time (newValidationError does this for "body.").
func buildFieldDescriptions(att *goaexpr.AttributeExpr) map[string]string {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return nil
	}
	out := make(map[string]string)
	seen := make(map[string]struct{})
	var walk func(prefix string, a *goaexpr.AttributeExpr)
	walk = func(prefix string, a *goaexpr.AttributeExpr) {
		if a == nil || a.Type == nil || a.Type == goaexpr.Empty {
			return
		}
		switch dt := a.Type.(type) {
		case goaexpr.UserType:
			// Avoid infinite recursion on recursive user types.
			id := dt.ID()
			if _, ok := seen[id]; ok {
				return
			}
			seen[id] = struct{}{}
			walk(prefix, dt.Attribute())
		case *goaexpr.Object:
			for _, nat := range *dt {
				name := nat.Name
				path := name
				if prefix != "" {
					path = prefix + "." + name
				}
				if nat.Attribute != nil && nat.Attribute.Description != "" {
					out[path] = nat.Attribute.Description
				}
				walk(path, nat.Attribute)
			}
		case *goaexpr.Array:
			walk(prefix, dt.ElemType)
		case *goaexpr.Map:
			walk(prefix, dt.ElemType)
		case *goaexpr.Union:
			for _, v := range dt.Values {
				walk(prefix, v.Attribute)
			}
		}
	}
	walk("", att)
	if len(out) == 0 {
		return nil
	}
	return out
}

// isEmptyStruct reports whether the attribute resolves to an empty object.
// It follows user types so callers can treat alias user types over empty
// objects the same as literal empty structs.
func isEmptyStruct(att *goaexpr.AttributeExpr) bool {
	if att == nil || att.Type == nil {
		return true
	}
	if att.Type == goaexpr.Empty {
		return true
	}
	switch dt := att.Type.(type) {
	case goaexpr.UserType:
		return isEmptyStruct(dt.Attribute())
	case *goaexpr.Object:
		return len(*dt) == 0
	default:
		return false
	}
}

// attributeHasUnion reports whether the provided attribute (or any of its
// nested children) contains a union type. It follows user types, arrays,
// maps, and objects to detect unions anywhere in the graph.
func attributeHasUnion(att *goaexpr.AttributeExpr) bool {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return false
	}
	seen := make(map[*goaexpr.AttributeExpr]struct{})
	var walk func(a *goaexpr.AttributeExpr) bool
	walk = func(a *goaexpr.AttributeExpr) bool {
		if a == nil || a.Type == nil || a.Type == goaexpr.Empty {
			return false
		}
		if _, ok := seen[a]; ok {
			return false
		}
		seen[a] = struct{}{}
		switch dt := a.Type.(type) {
		case *goaexpr.Union:
			return true
		case goaexpr.UserType:
			return walk(dt.Attribute())
		case *goaexpr.Array:
			return walk(dt.ElemType)
		case *goaexpr.Map:
			if walk(dt.KeyType) {
				return true
			}
			return walk(dt.ElemType)
		case *goaexpr.Object:
			for _, nat := range *dt {
				if nat == nil {
					continue
				}
				if walk(nat.Attribute) {
					return true
				}
			}
		}
		return false
	}
	return walk(att)
}

// collectUserTypeValidators walks the attribute graph and generates validator
// entries for each unique user type encountered that yields non-empty
// validation code. The generated entries are validator-only (no codecs), and
// allow Validate<Name>() to be called from top-level payload validators.
func (b *toolSpecBuilder) collectUserTypeValidators(scope *codegen.NameScope, tool *ToolData, usage typeUsage, att *goaexpr.AttributeExpr) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return
	}
	seen := make(map[string]struct{})
	_ = codegen.Walk(att, func(a *goaexpr.AttributeExpr) error {
		if a == nil || a.Type == nil || a.Type == goaexpr.Empty {
			return nil
		}
		ut, ok := a.Type.(goaexpr.UserType)
		if !ok || ut == nil {
			return nil
		}
		// Skip validator generation for external user types whose underlying
		// attributes contain unions. Goa already represents these unions in the
		// owning package using unexported discriminator interfaces, and
		// regenerating validators here would require helper wrapper types that
		// do not exist in the specs package (leading to impossible type
		// switches and undefined identifiers). Nested union member types still
		// receive validators via their own user type entries.
		if loc := codegen.UserTypeLocation(ut); loc != nil && loc.RelImportPath != "" {
			if attributeHasUnion(ut.Attribute()) {
				return nil
			}
		}
		// Emit standalone validators for all encountered user types so that
		// payload validators can call into Validate<Type> for nested members
		// (including helper types materialized locally and external types).
		// This includes:
		//  - alias user types (UUID, TimeContext, etc.)
		//  - external user types (types package)
		//  - service-local user types (including helper types we materialize)
		// De-duplication is handled by the seen map and b.types cache.
		id := ut.ID()
		if _, ok := seen[id]; ok {
			return nil
		}
		seen[id] = struct{}{}
		// Skip validators for local helper types we materialized only to avoid
		// inline references ("helper type materialized for nested references.").
		// These are service-local convenience aliases and do not need standalone
		// validators; top-level payload validators cover nested fields.
		if uexpr, ok := ut.(*goaexpr.UserTypeExpr); ok {
			// Skip validators for ToolPayload helper wrappers to avoid pointer/value
			// mismatches; nested element helpers still get validators.
			if strings.HasSuffix(uexpr.TypeName, "ToolPayload") {
				return nil
			}
			// Skip validators for local helper types with unexported names
			// (e.g., actionAppend). These helpers are aliases over existing
			// shapes used as nested elements; top-level payload validators
			// already cover their fields, and emitting standalone validators
			// would either be no-ops or introduce undefined identifiers when
			// no corresponding alias type exists in the specs package.
			if len(uexpr.TypeName) > 0 {
				c := uexpr.TypeName[0]
				if c >= 'a' && c <= 'z' {
					return nil
				}
			}
		}
		// Generate validation code for the user type attribute itself. For
		// alias user types, ask Goa to cast to the underlying base type by
		// setting the alias flag so validations operate on correct values
		// (e.g., string(body) for string aliases) and avoid type mismatch.
		var vcode string
		{
			// Use default value semantics for primitives where defaults are present so
			// optional alias/value fields validate as values (not pointers).
			attCtx := codegen.NewAttributeContext(false, false, true, "", scope)
			vcode = validationCodeWithContext(ut.Attribute(), ut, attCtx, true, goaexpr.IsAlias(ut), false, "body", tool, usage, "validator:"+ut.ID())
		}
		// Emit a validator entry even if vcode is empty because Goa-generated
		// parent validators may still call Validate<Type> for nested user types
		// (e.g., when only required validations exist on primitives). Emit a
		// no-op body in that case.
		{
			// Compute the fully-qualified reference and the public type name.
			typeName := ""
			switch u := ut.(type) {
			case *goaexpr.UserTypeExpr:
				typeName = u.TypeName
			case *goaexpr.ResultTypeExpr:
				typeName = u.TypeName
			default:
				typeName = codegen.Goify("UserType", true)
			}
			// Always generate a standalone validator for the user type. The
			// presence of a local alias with the same public name does not
			// conflict since validator entries only emit functions, not types.
			// Qualify with the owning package when available so validators use
			// the correct package alias (e.g., types.TimeContext).
			var fullRef string
			if loc := codegen.UserTypeLocation(ut); loc != nil && loc.PackageName() != "" {
				fullRef = scope.GoFullTypeRef(&goaexpr.AttributeExpr{Type: ut}, loc.PackageName())
			} else {
				fullRef = scope.GoFullTypeRef(&goaexpr.AttributeExpr{Type: ut}, "")
			}
			key := "validator:" + id
			if _, exists := b.types[key]; exists {
				return nil
			}
			b.types[key] = &typeData{
				Key:          key,
				TypeName:     typeName,
				ValidateFunc: "Validate" + typeName,
				Validation:   vcode,
				FullRef:      fullRef,
				// Pointer flag is unused for validator-only entries; leave false
				// to avoid implying pointer semantics for composites.
				Pointer:       false,
				ValidationSrc: strings.Split(vcode, "\n"),
				Usage:         usagePayload,
				TypeImports:   gatherAttributeImports(b.genpkg, &goaexpr.AttributeExpr{Type: ut}),
			}
		}
		return nil
	})
}

// serviceName returns the declaring service name for a tool.
//
// Tool specs are provider-owned: they should identify the service that
// declares/implements the toolset, not the consuming agent service that happens
// to reference it.
func serviceName(tool *ToolData) string {
	ts := tool.Toolset
	if ts.SourceServiceName != "" {
		return ts.SourceServiceName
	}
	if ts.ServiceName != "" {
		return ts.ServiceName
	}
	return ""
}

// toolsetName returns the name of the toolset that contains the tool.
func toolsetName(tool *ToolData) string {
	return tool.Toolset.QualifiedName
}

// gatherAttributeImports collects all import specifications needed for a given
// attribute expression, including imports for user types and meta-type imports.
// It returns a sorted, deduplicated list of import specs.
func gatherAttributeImports(genpkg string, att *goaexpr.AttributeExpr) []*codegen.ImportSpec {
	uniq := make(map[string]*codegen.ImportSpec)
	var visit func(*goaexpr.AttributeExpr)
	visit = func(a *goaexpr.AttributeExpr) {
		if a == nil {
			return
		}
		for _, im := range codegen.GetMetaTypeImports(a) {
			if im.Path != "" {
				uniq[im.Path] = im
			}
		}
		switch dt := a.Type.(type) {
		case goaexpr.UserType:
			if loc := codegen.UserTypeLocation(dt); loc != nil && loc.RelImportPath != "" {
				imp := &codegen.ImportSpec{Name: loc.PackageName(), Path: joinImportPath(genpkg, loc.RelImportPath)}
				uniq[imp.Path] = imp
			}
			visit(dt.Attribute())
		case *goaexpr.Array:
			visit(dt.ElemType)
		case *goaexpr.Map:
			visit(dt.KeyType)
			visit(dt.ElemType)
		case *goaexpr.Object:
			for _, nat := range *dt {
				visit(nat.Attribute)
			}
		case *goaexpr.Union:
			for _, nat := range dt.Values {
				if nat == nil {
					continue
				}
				visit(nat.Attribute)
			}
		case goaexpr.CompositeExpr:
			visit(dt.Attribute())
		}
	}
	visit(att)
	if len(uniq) == 0 {
		return nil
	}
	paths := make([]string, 0, len(uniq))
	for p := range uniq {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	imports := make([]*codegen.ImportSpec, 0, len(paths))
	for _, p := range paths {
		imports = append(imports, uniq[p])
	}
	return imports
}

// servicePkgAlias returns the import alias for the service package using the
// last path segment if available, falling back to the service PkgName.
func servicePkgAlias(svc *service.Data) string {
	// Always use the service package name so it matches the alias
	// used by Goa's NameScope when computing full type references.
	// Deriving the alias from the filesystem path (path.Base(PathName))
	// can diverge from the actual package identifier (e.g., underscores
	// vs. sanitized names), leading to mismatched qualifiers like
	// "atlasdataagent" vs "atlas_data_agent" in generated code.
	return svc.PkgName
}

// schemaForAttribute generates an OpenAPI JSON schema for the given attribute.
// It returns the schema as JSON bytes, or nil if the attribute is empty or
// cannot be represented as a schema.
func schemaForAttribute(att *goaexpr.AttributeExpr) ([]byte, error) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return nil, nil
	}
	prev := openapi.Definitions
	openapi.Definitions = make(map[string]*openapi.Schema)
	defer func() { openapi.Definitions = prev }()
	schema := openapi.AttributeTypeSchema(goaexpr.Root.API, att)
	if schema == nil {
		return nil, nil
	}
	if len(openapi.Definitions) > 0 {
		schema.Defs = openapi.Definitions
	}
	// Prefer a concrete root schema: for user types, inline the referenced
	// definition as the root so that the top-level contains "type":"object".
	// Retain definitions to allow nested $ref resolution.
	if ut, ok := att.Type.(goaexpr.UserType); ok {
		// Compute type name
		tname := ""
		switch u := ut.(type) {
		case *goaexpr.UserTypeExpr:
			tname = u.TypeName
		case *goaexpr.ResultTypeExpr:
			tname = u.TypeName
		}
		if tname != "" {
			if def, ok := openapi.Definitions[tname]; ok && def != nil {
				// Build a new definitions map excluding the root to avoid
				// self-referential cycles during JSON marshaling.
				if len(openapi.Definitions) > 0 {
					defs := make(map[string]*openapi.Schema, len(openapi.Definitions))
					for k, v := range openapi.Definitions {
						if k == tname {
							continue
						}
						defs[k] = v
					}
					if len(defs) > 0 {
						def.Defs = defs
					}
				}
				// Marshal schema JSON directly (Goa emits 2020-12 + $defs).
				b, err := def.JSON()
				if err != nil {
					return b, nil
				}
				return b, nil
			}
		}
	}
	b, err := schema.JSON()
	if err != nil {
		return b, err
	}
	return b, nil
}

// exampleForAttribute produces a minimal JSON example for the given attribute
// using Goa's example generator. When no meaningful example can be derived it
// returns nil so callers can distinguish between "no example" and an empty
// object.
func exampleForAttribute(att *goaexpr.AttributeExpr) []byte {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return nil
	}
	gen := &goaexpr.ExampleGenerator{Randomizer: goaexpr.NewDeterministicRandomizer()}
	v := att.Example(gen)
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil || len(data) == 0 {
		return nil
	}
	// Treat "{}" as a non-informative example and omit it.
	if string(data) == "{}" {
		return nil
	}
	return data
}

// joinImportPath constructs a full import path by joining the generation package
// base path with a relative path. It handles trailing "/gen" suffixes correctly.
func joinImportPath(genpkg, rel string) string {
	if rel == "" {
		return ""
	}
	base := strings.TrimSuffix(genpkg, "/")
	for strings.HasSuffix(base, "/gen") {
		base = strings.TrimSuffix(base, "/gen")
	}
	return path.Join(base, "gen", rel)
}

// lowerCamel converts a string to lower camelCase using Goa's Goify function.
func lowerCamel(s string) string {
	return codegen.Goify(s, false)
}
