// Package codegen provides code generation for agent-based systems.
//
// This file implements tool specification generation for agents. It transforms
// Goa design expressions into JSON schemas, type definitions, and codec
// functions that the agent runtime uses to marshal/unmarshal tool arguments
// and results during agent execution.
//
// The generation process operates in several phases:
//
//  1. buildToolSpecsData scans all tools defined in an agent's toolsets
//  2. For each tool, creates typeData for payload and result types by walking
//     the attribute expressions and generating Go type definitions
//  3. Generates OpenAPI JSON schemas for runtime validation
//  4. Produces marshal/unmarshal functions and validation code
//  5. Handles cross-service type references for MCP and external toolsets,
//     ensuring proper import paths and type aliases
//
// Generated artifacts are consumed by templates (tool_spec.go.tpl,
// tool_types.go.tpl, tool_codecs.go.tpl) to produce the tool_specs package
// under each agent's generated code directory.
package codegen

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	goaexpr "goa.design/goa/v3/expr"
	"goa.design/goa/v3/http/codegen/openapi"
)

type (
	// toolSpecsData aggregates all type and codec metadata for a set of tools
	// owned by a single Goa service. It collects type definitions, schemas, and
	// codec functions during the generation process and provides them to
	// templates for rendering.
	toolSpecsData struct {
		// svc is the Goa service that owns the tools.
		svc *service.Data
		// genpkg is the Go import path to the generated root (typically `<module>/gen`).
		genpkg string
		// Type metadata indexed by cache key for deduplication.
		types map[string]*typeData
		// Deterministic ordering of types for generation.
		order []*typeData
		// Tool entries with their payload/result type metadata.
		tools []*toolEntry
	}

	// toolEntry pairs a tool declaration with its payload/result type metadata.
	// Each entry represents one tool that will be generated in the tool_specs
	// package with its associated type definitions and codecs.
	toolEntry struct {
		// Qualified tool name used in runtime lookups (e.g., "toolset.tool").
		Name string
		// Title is the human-friendly display title.
		Title string
		// Service name that owns the tool.
		Service string
		// Toolset name that contains the tool.
		Toolset string
		// Tool description for documentation and LLM context.
		Description string
		// Classification tags for policy and filtering.
		Tags []string
		// Whether this tool is exported by an agent (agent-as-tool).
		IsExportedByAgent bool
		// ID of the agent that exports this tool.
		ExportingAgentID string
		// Type metadata for the tool's input arguments.
		Payload *typeData
		// Type metadata for the tool's output result.
		Result *typeData
	}

	// typeData holds all metadata needed to generate a type definition, schema,
	// and codec functions for a tool's payload or result type. It includes the
	// Go type definition, JSON schema, validation code, and import specifications.
	typeData struct {
		// Cache key for type deduplication (either "ref:<fullref>" or "name:<typename>").
		Key string
		// Go type name (e.g., "MyToolPayload").
		TypeName string
		// GoDoc comment describing the type.
		Doc string
		// Type definition line (e.g., "MyType struct { ... }" or "MyType = service.Type").
		Def string
		// Variable name for the JSON schema (e.g., "myToolPayloadSchema").
		SchemaVar string
		// JSON schema as a Go byte slice literal.
		SchemaLiteral string
		// Typed codec variable name (e.g., "MyToolPayloadCodec").
		ExportedCodec string
		// Untyped codec variable name (e.g., "myToolPayloadCodec").
		GenericCodec string
		// Marshal function name (e.g., "MarshalMyToolPayload").
		MarshalFunc string
		// Unmarshal function name (e.g., "UnmarshalMyToolPayload").
		UnmarshalFunc string
		// Validation function name (e.g., "ValidateMyToolPayload").
		ValidateFunc string
		// Validation code body.
		Validation string
		// Fully-qualified type reference with pointer prefix (e.g., "*MyType" or "MyType").
		FullRef string
		// Whether the type is a pointer.
		Pointer bool
		// Argument expression for marshal calls (e.g., "v" or "*v").
		MarshalArg string
		// Argument expression for unmarshal calls (e.g., "v" or "&v").
		UnmarshalArg string
		// Validation code split into lines for template rendering.
		ValidationSrc []string
		// Whether to generate a type definition.
		NeedType bool
		// Import spec for the type's package (when aliasing external types).
		Import *codegen.ImportSpec
		// Import spec for the service package (when referencing service types).
		ServiceImport *codegen.ImportSpec
		// Error message for nil values.
		NilError string
		// Error message for decode failures.
		DecodeError string
		// Error message for validation failures.
		ValidateError string
		// Error message for empty JSON input.
		EmptyError string
		// Whether this is a payload or result type.
		Usage typeUsage
		// Imports needed for this type's definition.
		TypeImports []*codegen.ImportSpec
		// Whether to generate codec functions.
		GenerateCodec bool
		// FieldDescs maps dotted field paths to descriptions (for payload types).
		FieldDescs map[string]string
		// AcceptEmpty indicates that empty JSON input should be accepted and
		// treated as the zero value (only for payloads). This is true for
		// payload types that are empty structs (no fields).
		AcceptEmpty bool
		// JSON decode-body support (payloads only)
		JSONTypeName      string
		JSONDef           string
		JSONRef           string
		JSONValidation    string
		JSONValidationSrc []string
		TransformBody     string
		TransformHelpers  []*codegen.TransformFunctionData
	}

	// toolSpecBuilder walks tool types and generates corresponding type metadata,
	// schemas, and validation code. It maintains caches for deduplication and
	// handles cross-service type references for MCP and external toolsets.
	toolSpecBuilder struct {
		// Generation package base path.
		genpkg string
		// Service data for the owning service.
		service *service.Data
		// Name scope for service type references.
		svcScope *codegen.NameScope
		// Import specs for service types.
		svcImports map[string]*codegen.ImportSpec
		// Cache of generated type metadata indexed by cache key.
		types map[string]*typeData
		// helperScope provides a global scope to assign short, unique names
		// to transform helper functions across all generated tool payloads.
		// Using a shared scope ensures there are no collisions while keeping
		// names compact and readable.
		helperScope *codegen.NameScope
	}

	typeUsage string
)

const (
	usagePayload typeUsage = "payload"
	usageResult  typeUsage = "result"
)

// buildToolSpecsData constructs the complete type and codec metadata for all tools
// in an agent. It walks each tool's argument and result types, generates corresponding
// Go type definitions, OpenAPI schemas, and validation code, then assembles everything
// into a toolSpecsData structure for template consumption.
//
// The function handles cross-service type references (for MCP and external toolsets),
// deduplicates types across tools, and maintains topological ordering for type
// dependencies. Returns an error if type resolution or schema generation fails.
func buildToolSpecsData(agent *AgentData) (*toolSpecsData, error) {
	return buildToolSpecsDataFor(agent.Genpkg, agent.Service, agent.Tools)
}

// buildToolSpecsDataFor builds specs/types/codecs data for the provided tool
// slice using the owning service as the context for type/import resolution.
func buildToolSpecsDataFor(genpkg string, svc *service.Data, tools []*ToolData) (*toolSpecsData, error) {
	data := newToolSpecsData(genpkg, svc)
	builder := newToolSpecBuilder(genpkg, svc)
	for _, tool := range tools {
		payload, err := builder.typeFor(tool, tool.Args, usagePayload)
		if err != nil {
			return nil, err
		}
		result, err := builder.typeFor(tool, tool.Return, usageResult)
		if err != nil {
			return nil, err
		}
		entry := &toolEntry{
			// Name is the qualified tool ID used at runtime (toolset.tool).
			Name:              tool.QualifiedName,
			Title:             tool.Title,
			Service:           serviceName(tool),
			Toolset:           toolsetName(tool),
			Description:       tool.Description,
			Tags:              tool.Tags,
			IsExportedByAgent: tool.IsExportedByAgent,
			ExportingAgentID:  tool.ExportingAgentID,
			Payload:           payload,
			Result:            result,
		}
		data.addTool(entry)
	}
	// Add any additional nested/local types in a deterministic order.
	if len(builder.types) > 0 {
		infos := make([]*typeData, 0, len(builder.types))
		for _, info := range builder.types {
			infos = append(infos, info)
		}
		sort.Slice(infos, func(i, j int) bool { return infos[i].TypeName < infos[j].TypeName })
		for _, info := range infos {
			data.addType(info)
		}
	}
	sort.Slice(data.tools, func(i, j int) bool {
		return data.tools[i].Name < data.tools[j].Name
	})
	return data, nil
}

func (d *toolSpecsData) addTool(entry *toolEntry) {
	d.tools = append(d.tools, entry)
	d.addType(entry.Payload)
	d.addType(entry.Result)
}

func (d *toolSpecsData) addType(info *typeData) {
	if info == nil {
		return
	}
	key := info.Key
	if key == "" {
		key = info.TypeName
	}
	if _, ok := d.types[key]; ok {
		return
	}
	d.types[key] = info
	d.order = append(d.order, info)
}

func (d *toolSpecsData) typesList() []*typeData {
	return d.order
}

func (d *toolSpecsData) pureTypes() []*typeData {
	var out []*typeData
	for _, info := range d.order {
		if info.NeedType {
			out = append(out, info)
		}
	}
	return out
}

func (d *toolSpecsData) needsGoaImport() bool {
	for _, info := range d.order {
		if info.Validation != "" || info.JSONValidation != "" {
			return true
		}
	}
	return false
}

func (d *toolSpecsData) needsUnicodeImport() bool {
	for _, info := range d.order {
		if (info.Validation != "" && strings.Contains(info.Validation, "utf8.")) ||
			(info.JSONValidation != "" && strings.Contains(info.JSONValidation, "utf8.")) {
			return true
		}
	}
	return false
}

func (d *toolSpecsData) typeImports() []*codegen.ImportSpec {
	if len(d.order) == 0 {
		return nil
	}
	uniq := make(map[string]*codegen.ImportSpec)
	for _, info := range d.order {
		for _, im := range info.TypeImports {
			if im.Path == "" {
				continue
			}
			uniq[im.Path] = im
		}
		if info.ServiceImport != nil && info.ServiceImport.Path != "" {
			uniq[info.ServiceImport.Path] = info.ServiceImport
		}
	}
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

func (d *toolSpecsData) codecsImports() []*codegen.ImportSpec {
	base := []*codegen.ImportSpec{
		codegen.SimpleImport("encoding/json"),
		codegen.SimpleImport("errors"),
		codegen.SimpleImport("fmt"),
		codegen.SimpleImport("goa.design/goa-ai/runtime/agent/tools"),
	}
	if d.needsUnicodeImport() {
		base = append(base, codegen.SimpleImport("unicode/utf8"))
	}
	needsGoa := d.needsGoaImport()
	extra := make(map[string]*codegen.ImportSpec)
	needsServiceImport := false
	serviceImportPath := joinImportPath(d.genpkg, d.svc.PathName)
	for _, info := range d.typesList() {
		if info.Import != nil && info.Import.Path != "" {
			extra[info.Import.Path] = info.Import
			if info.Import.Name == d.svc.PkgName {
				needsServiceImport = true
			}
		}
		if info.ServiceImport != nil && info.ServiceImport.Path != "" {
			extra[info.ServiceImport.Path] = info.ServiceImport
			if info.ServiceImport.Name == d.svc.PkgName {
				needsServiceImport = true
			}
		}
		for _, im := range info.TypeImports {
			if im.Path == "" {
				continue
			}
			extra[im.Path] = im
		}
	}
	if needsServiceImport && serviceImportPath != "" {
		if _, exists := extra[serviceImportPath]; !exists {
			extra[serviceImportPath] = &codegen.ImportSpec{Name: d.svc.PkgName, Path: serviceImportPath}
		}
	}
	if len(extra) > 0 {
		paths := make([]string, 0, len(extra))
		for p := range extra {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			base = append(base, extra[p])
		}
	}
	if needsGoa {
		base = append(base, codegen.GoaImport(""))
	}
	// Keep strings import last to match golden expectations.
	base = append(base, codegen.SimpleImport("strings"))
	return base
}

func (b *toolSpecBuilder) scopeForTool(t *ToolData) *codegen.NameScope {
	// Prefer the source service (provider) scope when available (e.g., MCP/external)
	if t != nil && t.Toolset != nil && t.Toolset.SourceService != nil && t.Toolset.SourceService.Scope != nil {
		return t.Toolset.SourceService.Scope
	}
	if b.service.Scope != nil {
		return b.service.Scope
	}
	return b.svcScope
}

func (b *toolSpecBuilder) typeFor(tool *ToolData, att *goaexpr.AttributeExpr, usage typeUsage) (*typeData, error) {
	// For method-backed tools, prefer the tool Return type for RESULTs when it
	// is explicitly declared in the DSL so that model-facing schemas reflect
	// the tool contract (e.g., AtlasListDevicesToolReturn). When no Return is
	// provided, fall back to the bound service method result type so specs
	// alias the concrete service result directly.
	//
	// For PAYLOADs, always use the tool's own argument type to prevent
	// server-only fields (e.g., session_id) from leaking into tool-visible
	// schemas. Server fields are injected post-decode by adapters before
	// making the actual service method call.
	if tool != nil && tool.IsMethodBacked && usage == usageResult {
		if (tool.Return == nil || tool.Return.Type == goaexpr.Empty) &&
			tool.MethodResultAttr != nil && tool.MethodResultAttr.Type != goaexpr.Empty {
			att = tool.MethodResultAttr
		}
	}

	if usage == usagePayload && att.Type == goaexpr.Empty {
		// For payloads with no arguments, synthesize an empty object.
		// This ensures a concrete Payload type is always generated for adapters and codecs,
		// avoiding nil checks in generated code.
		att = &goaexpr.AttributeExpr{Type: &goaexpr.Object{}}
	}

	info, err := b.buildTypeInfo(tool, att, usage)
	if err != nil {
		return nil, err
	}
	return info, nil
}

// ensureType generates a standalone type definition and metadata for a tool's
// payload or result using a simplified, mode-driven materialization policy.
func (b *toolSpecBuilder) buildTypeInfo(tool *ToolData, att *goaexpr.AttributeExpr, usage typeUsage) (*typeData, error) {
	if tool == nil || tool.Toolset == nil {
		return nil, fmt.Errorf("invalid tool metadata: nil tool or toolset")
	}
	typeName := codegen.Goify(tool.Name, true)
	switch usage {
	case usagePayload:
		typeName += "Payload"
	case usageResult:
		typeName += "Result"
	}

	scope := b.scopeForTool(tool)

	// Stable cache key: reference for service-alias, otherwise deterministic name
	key := stableTypeKey(tool, usage)
	if existing := b.types[key]; existing != nil {
		return existing, nil
	}

	// Preserve user types so codecs reference service user types explicitly
	// (e.g., *alpha.Doc) even for non-method-backed tools. This ensures
	// deterministic aliasing and imports and matches the repository tests
	// which assert service-qualified references in generated codecs.
	// Materialize definition and type reference
	tt, defLine, fullRef, imports := b.materialize(typeName, att, scope)
	// Determine pointer semantics for top-level alias/value.
	aliasIsPointer := strings.Contains(defLine, "= *")
	ptr := aliasIsPointer || strings.HasPrefix(fullRef, "*")

	// JSON schema from effective attribute
	schemaBytes, err := schemaForAttribute(tt)
	if err != nil {
		return nil, err
	}
	schemaLiteral := formatSchema(schemaBytes)
	schemaVar := ""
	if schemaLiteral != "" {
		schemaVar = lowerCamel(typeName) + "Schema"
	}

	doc := fmt.Sprintf("%s defines the JSON %s for the %s tool.", typeName, usage, tool.QualifiedName)
	info := &typeData{
		Key:           key,
		TypeName:      typeName,
		Doc:           doc,
		Def:           defLine,
		SchemaVar:     schemaVar,
		SchemaLiteral: schemaLiteral,
		ExportedCodec: typeName + "Codec",
		GenericCodec:  lowerCamel(typeName) + "Codec",
		MarshalFunc:   "Marshal" + typeName,
		UnmarshalFunc: "Unmarshal" + typeName,
		ValidateFunc:  "Validate" + typeName,
		FullRef:       fullRef,
		NeedType:      defLine != "",
		NilError:      fmt.Sprintf("%s is nil", lowerCamel(typeName)),
		DecodeError:   fmt.Sprintf("decode %s", lowerCamel(typeName)),
		ValidateError: fmt.Sprintf("validate %s", lowerCamel(typeName)),
		EmptyError:    fmt.Sprintf("%s JSON is empty", lowerCamel(typeName)),
		Usage:         usage,
		TypeImports:   imports,
		GenerateCodec: true,
		Pointer:       ptr,
		MarshalArg:    "v",
		UnmarshalArg:  "v",
	}
	// For tool payloads, untyped codecs should return pointers.
	// Record pointer intent via the flag; templates will render "*" where needed
	// using Goa NameScope-derived base references (no string surgery here).
	if usage == usagePayload {
		info.Pointer = true
	}
	// Populate JSON validation and field descriptions for payload types only.
	if usage == usagePayload {
		// Do not generate standalone validation for the final payload type.
		info.ValidateFunc = ""
		info.Validation = ""
		info.ValidationSrc = nil
		// Accept empty JSON for payloads that are empty structs (no fields).
		if isEmptyStruct(att) {
			info.AcceptEmpty = true
		}
		// Build JSON decode-body types uniformly, treating Empty as an empty object.
		jsonAttr := att
		if att.Type == goaexpr.Empty {
			jsonAttr = &goaexpr.AttributeExpr{Type: &goaexpr.Object{}}
		}
		// 1) JSON decode-body type: materialize a single named user type for the
		// root body with inline nested objects (no separate nested user types).
		jsonRoot, jsonDefs := b.materializeJSONUserTypes(jsonAttr, typeName+"JSON", scope)
		// Compute the final public name for the root JSON type so that
		// references in codecs match the emitted type name in types.go.
		// JSON helper types are emitted in the current package; use GoTypeName
		// so names are unqualified when no package is needed.
		jsonRootPublic := scope.GoTypeName(&goaexpr.AttributeExpr{Type: jsonRoot})
		info.JSONTypeName = jsonRootPublic
		info.JSONRef = jsonRootPublic

		// Emit the JSON root type as a standalone declaration.
		for _, jut := range jsonDefs {
			// Ensure JSON helper fields carry struct tag metadata so GoTypeDef
			// can emit json tags that match the original field names (including
			// underscores). Without this, names like "device_aliases" will not
			// populate fields such as DeviceAliases correctly.
			if obj := goaexpr.AsObject(jut.Attribute().Type); obj != nil {
				for _, nat := range *obj {
					if nat == nil || nat.Attribute == nil {
						continue
					}
					if nat.Attribute.Meta == nil {
						nat.Attribute.Meta = make(map[string][]string)
					}
					// Only set when no tag is present so DSL authors can override it.
					if _, ok := nat.Attribute.Meta["struct:tag:json"]; !ok {
						nat.Attribute.Meta["struct:tag:json"] = []string{nat.Name}
					}
				}
			}

			// Use Goa scope to compute the final public name, which guarantees
			// consistency with references produced by GoTypeDef/GoTypeName.
			// Helper JSON types are local to the specs package; use GoTypeName
			// to keep names unqualified.
			jattr := &goaexpr.AttributeExpr{Type: jut}
			gname := scope.GoTypeName(jattr)
			def := gname + " " + scope.GoTypeDef(jut.Attribute(), true, false)
			ref := scope.GoTypeRef(jattr)
			// Generate standalone validator for this JSON helper user type so
			// root validators that call Validate<Helper> compile.
			httpctx := codegen.NewAttributeContext(true, false, false, "", scope)
			vcode := codegen.ValidationCode(jut.Attribute(), nil, httpctx, true, false, false, "body")
			td := &typeData{
				Key:           "json:" + gname,
				TypeName:      gname,
				Doc:           gname + " is a helper type for JSON decode-body.",
				Def:           def,
				FullRef:       ref,
				NeedType:      true,
				TypeImports:   gatherAttributeImports(b.genpkg, jut.Attribute()),
				ValidateFunc:  "Validate" + gname,
				Validation:    vcode,
				ValidationSrc: strings.Split(vcode, "\n"),
			}
			if _, exists := b.types[td.Key]; !exists {
				b.types[td.Key] = td
			}
		}

		// 2) Validation against JSON body using HTTP server-like AttributeContext
		httpctx := codegen.NewAttributeContext(true, false, false, "", scope)
		jv := codegen.ValidationCode(jsonRoot.Attribute(), nil, httpctx, true, false, false, "raw")
		if jv != "" {
			info.JSONValidation = jv
			info.JSONValidationSrc = strings.Split(jv, "\n")
		}

		// 3) Transform raw(JSON) -> final type.
		// For empty payloads, emit a direct initializer to the local alias to avoid
		// Goa's transform generating '&Empty{}'.
		if att.Type == goaexpr.Empty {
			info.TransformBody = "v := &" + typeName + "{}"
		} else {
			// Synthesize source and target user types from the underlying base attribute
			// so we don't create recursive self-references.
			baseAttr := att
			if ut, ok := att.Type.(goaexpr.UserType); ok && ut != nil {
				baseAttr = ut.Attribute()
			}
			// Treat empty payloads as empty objects for transforms so Goa emits
			// '&<TypeName>{}' rather than '&Empty{}' in generated code.
			if baseAttr.Type == goaexpr.Empty {
				baseAttr = &goaexpr.AttributeExpr{Type: &goaexpr.Object{}}
			}
			srcUT := jsonRoot
			srcAttr := &goaexpr.AttributeExpr{Type: srcUT}
			var tgtAttr *goaexpr.AttributeExpr
			if u2, ok := att.Type.(goaexpr.UserType); ok && u2 != nil {
				// Transform directly into the declared user type (external or local)
				tgtAttr = &goaexpr.AttributeExpr{Type: u2}
			} else {
				// Synthesize a local user type name for anonymous payloads
				tgtUT := &goaexpr.UserTypeExpr{AttributeExpr: baseAttr, TypeName: typeName}
				tgtAttr = &goaexpr.AttributeExpr{Type: tgtUT}
			}
			// Use the service scope so helper names/types resolved by transforms
			// match the emitted JSON helper type names (avoid JSON2 suffix skew).
			srcCtx := codegen.NewAttributeContext(true, false, false, "", scope)
			tgtCtx := codegen.NewAttributeContext(false, false, true, "", scope)
			body, helpers, err := codegen.GoTransform(srcAttr, tgtAttr, "raw", "v", srcCtx, tgtCtx, "payload", true)
			if err == nil && body != "" {
				info.TransformBody = body
				if len(helpers) > 0 {
					info.TransformHelpers = helpers
				}
			}
		}

		// Keep field descriptions for validation error enrichment
		if fdesc := buildFieldDescriptions(tt); len(fdesc) > 0 {
			info.FieldDescs = fdesc
		}
	}
	b.types[key] = info
	// Also index by the public type name so auxiliary passes (e.g.,
	// validator collection) can detect that a concrete alias already
	// exists and avoid emitting duplicate helpers for the same name.
	nameKey := "name:" + typeName
	if _, exists := b.types[nameKey]; !exists {
		b.types[nameKey] = info
	}
	// Also ensure any nested service-local user types are materialized locally so
	// unqualified references inside composite shapes compile.
	b.ensureNestedLocalTypes(scope, tt)
	// Collect validators for all unique user types referenced within payloads so
	// nested validations do not rely on external packages to provide local
	// Validate<Name> functions.
	if usage == usagePayload {
		b.collectUserTypeValidators(scope, tt)
	}
	return info, nil
}

// isEmptyStruct reports whether the provided attribute ultimately resolves to
// an object with no fields (empty struct). It follows user type aliases to
// inspect the underlying attribute graph.
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

// materialize builds the concrete type definition line, its effective attribute
// (for local definitions), the fully-qualified reference with correct pointer
// semantics, and the set of imports needed. For service aliases, ServiceImport
// is returned to drive deterministic imports downstream.
// materialize returns the effective attribute (unchanged), an empty type
// definition (we do not synthesize local types), the fully-qualified reference
// to the owner or primitive type, and the imports required.
func (b *toolSpecBuilder) materialize(typeName string, att *goaexpr.AttributeExpr, scope *codegen.NameScope) (tt *goaexpr.AttributeExpr, defLine string, fullRef string, imports []*codegen.ImportSpec) {
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
		} else {
			// Service-local user type: alias to its underlying composite/value
			// without qualifying with the service package. For tool aliases we
			// materialize a local struct where fields carry json struct tags
			// that mirror the original field names so that encoding/json
			// produces payloads consistent with the tool schema even when the
			// design did not set explicit JSON struct tags. Nested user types
			// referenced by the composite are materialized locally by
			// ensureNestedLocalTypes.
			rhs := scope.GoTypeDef(cloneWithJSONTags(dt.Attribute()), false, true)
			defLine = typeName + " = " + rhs
			fullRef = typeName
		}
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
	case *goaexpr.Object, goaexpr.CompositeExpr:
		// Alias to inline struct definition using Goa's type def helper without
		// service package qualification so nested service user types are
		// referenced locally. Do not apply default-based pointer elision here so
		// validation pointer semantics stay aligned with generated field types.
		rhs := scope.GoTypeDef(cloneWithJSONTags(att), false, false)
		defLine = typeName + " = " + rhs
		fullRef = typeName
	default:
		// Primitives: refer directly by type (no local alias emitted).
		fullRef = scope.GoTypeRef(att)
	}
	tt = att
	return tt, defLine, fullRef, imports
}

// stableTypeKey returns a deterministic cache key for the top-level type.
//
//   - For ServiceReferenced user types, the fully-qualified ref is used so
//     aliases converge across tools.
//   - Otherwise, the deterministic local name is used.
func stableTypeKey(tool *ToolData, usage typeUsage) string {
	tn := codegen.Goify(tool.Name, true)
	switch usage {
	case usagePayload:
		tn += "Payload"
	case usageResult:
		tn += "Result"
	}
	return "name:" + tn
}

func newToolSpecsData(genpkg string, svc *service.Data) *toolSpecsData {
	return &toolSpecsData{
		svc:    svc,
		genpkg: genpkg,
		types:  make(map[string]*typeData),
	}
}

func newToolSpecBuilder(genpkg string, svc *service.Data) *toolSpecBuilder {
	scope := svc.Scope
	if scope == nil {
		scope = codegen.NewNameScope()
	}
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
		helperScope: codegen.NewNameScope(),
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
			name = u.TypeName
		case *goaexpr.ResultTypeExpr:
			name = u.TypeName
		default:
			return nil
		}
		if name == "" {
			return nil
		}
		key := "name:" + name
		if _, exists := b.types[key]; exists {
			return nil
		}
		// Alias to the underlying composite/value shape for the nested type.
		// Use GoTypeDef to inline the concrete shape instead of referencing the
		// user type name, avoiding circular aliases.
		comp := scope.GoTypeDef(ut.Attribute(), false, true)
		td := &typeData{
			Key:         key,
			TypeName:    name,
			Doc:         name + " is a helper type materialized for nested references.",
			Def:         name + " = " + comp,
			FullRef:     name,
			NeedType:    true,
			TypeImports: gatherAttributeImports(b.genpkg, ut.Attribute()),
		}
		b.types[key] = td
		return nil
	})
}

// materializeJSONUserTypes walks the attribute and returns a root user type
// representing the JSON decode-body (server body style) along with all nested
// helper user types. No inline structs are produced.
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
	// Root must be a user type (object). If not, create a trivial wrapper to keep
	// transform and validation consistent.
	if ut, ok := rootAttr.Type.(*goaexpr.UserTypeExpr); ok {
		return ut, defs
	}
	// Wrap non-object payloads in a synthetic single-field object to keep the
	// decode/validate/transform flow uniform.
	tname := scope.Unique(codegen.Goify(base, true))
	obj := &goaexpr.Object{&goaexpr.NamedAttributeExpr{Name: "Value", Attribute: rootAttr}}
	rut := &goaexpr.UserTypeExpr{AttributeExpr: &goaexpr.AttributeExpr{Type: obj}, TypeName: tname}
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

// collectUserTypeValidators walks the attribute graph and generates validator entries
// for each unique user type encountered that yields non-empty validation code. The
// generated entries are validator-only (no codecs), and allow Validate<Name>() to be
// called from top-level payload validators.
func (b *toolSpecBuilder) collectUserTypeValidators(scope *codegen.NameScope, att *goaexpr.AttributeExpr) {
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
			vcode = codegen.ValidationCode(ut.Attribute(), nil, attCtx, true, goaexpr.IsAlias(ut), false, "body")
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

// serviceName returns the service name for a tool, preferring the toolset's
// explicit ServiceName, then falling back to the agent's service name.
func serviceName(tool *ToolData) string {
	ts := tool.Toolset
	if ts.ServiceName != "" {
		return ts.ServiceName
	}
	if ts.Agent != nil {
		return ts.Agent.Service.Name
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

// formatSchema formats a JSON schema byte slice as a Go byte slice literal
// suitable for embedding in generated code.
func formatSchema(schema []byte) string {
	if len(schema) == 0 {
		return ""
	}
	content := string(schema)
	return "[]byte(`\n" + content + "\n`)"
}

// lowerCamel converts a string to lower camelCase using Goa's Goify function.
func lowerCamel(s string) string {
	return codegen.Goify(s, false)
}
