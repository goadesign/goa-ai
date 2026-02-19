package codegen

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	goaexpr "goa.design/goa/v3/expr"
	"goa.design/goa/v3/http/codegen/openapi"
)

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
			// Unions marshal as a canonical {type,value} object. Field paths should
			// reflect the actual wire contract to avoid misleading dotted paths like
			// "block.text" that omit the "value" envelope.
			valuePrefix := prefix
			if valueKey := dt.GetValueKey(); valueKey != "" {
				if valuePrefix != "" {
					valuePrefix = valuePrefix + "." + valueKey
				} else {
					valuePrefix = valueKey
				}
			}
			for _, v := range dt.Values {
				walk(valuePrefix, v.Attribute)
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

	// Normalize to JSON-native shapes (map[string]any, []any, float64, string, bool)
	// so downstream rewriting logic doesn't have to handle typed maps/slices that
	// Goa's example generator may produce for single-field objects.
	raw, err := json.Marshal(v)
	if err != nil || len(raw) == 0 {
		return nil
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return nil
	}
	normalized = canonicalizeUnionExamples(att, normalized)
	data, err := json.Marshal(normalized)
	if err != nil || len(data) == 0 {
		return nil
	}
	// Treat "{}" as a non-informative example and omit it.
	if string(data) == "{}" {
		return nil
	}
	return data
}

// canonicalizeUnionExamples rewrites Goa's "flattened" union examples into the
// canonical JSON shape required by Goa-generated codecs: {type,value}.
//
// Goa's Union.Example returns only the selected branch value, which is useful
// for documentation but misleading for tool specs where the runtime decoder
// expects explicit discriminators. This helper preserves the structure produced
// by the standard example generator and wraps only union nodes.
func canonicalizeUnionExamples(att *goaexpr.AttributeExpr, example any) any {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return example
	}
	switch dt := att.Type.(type) {
	case goaexpr.UserType:
		return canonicalizeUnionExamples(dt.Attribute(), example)
	case *goaexpr.Object:
		m, ok := example.(map[string]any)
		if !ok {
			return example
		}
		for k, v := range m {
			child := att.Find(k)
			m[k] = canonicalizeUnionExamples(child, v)
		}
		return m
	case *goaexpr.Array:
		s, ok := example.([]any)
		if !ok {
			return example
		}
		for i, v := range s {
			s[i] = canonicalizeUnionExamples(dt.ElemType, v)
		}
		return s
	case *goaexpr.Map:
		m, ok := example.(map[string]any)
		if !ok {
			return example
		}
		for k, v := range m {
			m[k] = canonicalizeUnionExamples(dt.ElemType, v)
		}
		return m
	case *goaexpr.Union:
		if example == nil || len(dt.Values) == 0 {
			return example
		}

		var chosen *goaexpr.NamedAttributeExpr
		chosen = pickUnionVariantForExample(dt, example)
		if chosen == nil {
			panic(fmt.Sprintf("agent/specs_builder: union example does not match any variant (type=%q)", dt.TypeName))
		}

		typeKey := dt.GetTypeKey()
		if typeKey == "" {
			typeKey = "type"
		}
		valueKey := dt.GetValueKey()
		if valueKey == "" {
			valueKey = "value"
		}

		return map[string]any{
			typeKey:  chosen.Name,
			valueKey: canonicalizeUnionExamples(chosen.Attribute, example),
		}
	default:
		return example
	}
}

func pickUnionVariantForExample(u *goaexpr.Union, example any) *goaexpr.NamedAttributeExpr {
	// Prefer key-based matching for object-shaped unions: Goa emits object examples
	// as map[string]any, but IsCompatible may not be able to match user type
	// variants directly (it reasons about Go types, not JSON shapes).
	if m, ok := example.(map[string]any); ok {
		for _, nat := range u.Values {
			if nat == nil || nat.Attribute == nil {
				continue
			}
			if unionVariantMatchesObjectKeys(nat.Attribute, m) {
				return nat
			}
		}
	}

	for _, nat := range u.Values {
		if nat == nil || nat.Attribute == nil || nat.Attribute.Type == nil {
			continue
		}
		attr := unwrapUserTypeAttr(nat.Attribute)
		if attr == nil || attr.Type == nil {
			continue
		}
		if attr.Type.IsCompatible(example) {
			return nat
		}
	}

	return nil
}

func unionVariantMatchesObjectKeys(att *goaexpr.AttributeExpr, example map[string]any) bool {
	attr := unwrapUserTypeAttr(att)
	if attr == nil {
		return false
	}
	obj, ok := attr.Type.(*goaexpr.Object)
	if !ok || obj == nil {
		return false
	}

	fields := make(map[string]struct{}, len(*obj))
	for _, nat := range *obj {
		fields[nat.Name] = struct{}{}
	}

	for k := range example {
		if _, ok := fields[k]; !ok {
			return false
		}
	}
	return true
}

func unwrapUserTypeAttr(att *goaexpr.AttributeExpr) *goaexpr.AttributeExpr {
	if att == nil || att.Type == nil {
		return att
	}
	for {
		ut, ok := att.Type.(goaexpr.UserType)
		if !ok {
			return att
		}
		att = ut.Attribute()
		if att == nil || att.Type == nil {
			return att
		}
	}
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
