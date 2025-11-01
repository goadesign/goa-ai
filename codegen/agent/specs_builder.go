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
	// toolSpecsData aggregates all type and codec metadata for an agent's tools.
	// It collects type definitions, schemas, and codec functions during the
	// generation process and provides them to templates for rendering.
	toolSpecsData struct {
		// Agent data for the agent being processed.
		agent *AgentData
		// Generation package base path.
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
		// Qualified tool name used in runtime lookups (e.g., "service.toolset.tool").
		Name string
		// Human-readable tool name for display purposes.
		DisplayName string
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
		// Whether to check for nil before marshaling.
		CheckNil bool
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
	}

	// toolSpecBuilder walks tool types and generates corresponding type metadata,
	// schemas, and validation code. It maintains caches for deduplication and
	// handles cross-service type references for MCP and external toolsets.
	toolSpecBuilder struct {
		// Generation package base path.
		genpkg string
		// Agent data for the agent being processed.
		agent *AgentData
		// Service data for the agent's primary service.
		service *service.Data
		// Name scope for service type references.
		svcScope *codegen.NameScope
		// Import specs for service types.
		svcImports map[string]*codegen.ImportSpec
		// Cache of generated type metadata indexed by cache key.
		types map[string]*typeData
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
	return buildToolSpecsDataFor(agent, agent.Tools)
}

// buildToolSpecsDataFor builds specs/types/codecs data for the provided tool slice.
func buildToolSpecsDataFor(agent *AgentData, tools []*ToolData) (*toolSpecsData, error) {
	data := newToolSpecsData(agent)
	builder := newToolSpecBuilder(agent)
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
			Name:              tool.QualifiedName,
			DisplayName:       tool.DisplayName,
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
		if strings.TrimSpace(info.Validation) != "" {
			return true
		}
	}
	return false
}

func (d *toolSpecsData) needsUnicodeImport() bool {
	for _, info := range d.order {
		if strings.TrimSpace(info.Validation) != "" && strings.Contains(info.Validation, "utf8.") {
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
	serviceImportPath := joinImportPath(d.agent.Genpkg, d.agent.Service.PathName)
	for _, info := range d.typesList() {
		if info.Import != nil && info.Import.Path != "" {
			extra[info.Import.Path] = info.Import
			if info.Import.Name == d.agent.Service.PkgName {
				needsServiceImport = true
			}
		}
		if info.ServiceImport != nil && info.ServiceImport.Path != "" {
			extra[info.ServiceImport.Path] = info.ServiceImport
			if info.ServiceImport.Name == d.agent.Service.PkgName {
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
			extra[serviceImportPath] = &codegen.ImportSpec{Name: d.agent.Service.PkgName, Path: serviceImportPath}
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

func (b *toolSpecBuilder) serviceForTool(t *ToolData) *service.Data {
	if t.Toolset.SourceService != nil {
		return t.Toolset.SourceService
	}
	return b.agent.Service
}

func (b *toolSpecBuilder) scopeForTool(t *ToolData) *codegen.NameScope {
	// Prefer the source service (provider) scope when available (e.g., MCP/external)
	if t != nil && t.Toolset != nil && t.Toolset.SourceService != nil && t.Toolset.SourceService.Scope != nil {
		return t.Toolset.SourceService.Scope
	}
	if b.agent.Service.Scope != nil {
		return b.agent.Service.Scope
	}
	return b.svcScope
}

func (b *toolSpecBuilder) typeFor(tool *ToolData, att *goaexpr.AttributeExpr, usage typeUsage) (*typeData, error) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		// Synthesize an empty object for payloads with no args so that a
		// concrete Payload type is always generated. This keeps adapters and
		// codecs uniform and avoids nil checks in generated code.
		if usage == usagePayload {
			empty := &goaexpr.AttributeExpr{Type: &goaexpr.Object{}}
			att = empty
		} else {
			return nil, nil
		}
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
	svc := b.serviceForTool(tool)

	// Stable cache key: reference for service-alias, otherwise deterministic name
	key := stableTypeKey(tool, usage)
	if existing := b.types[key]; existing != nil {
		return existing, nil
	}

	// Materialize definition and type reference
	tt, defLine, fullRef, imports := b.materialize(tool, typeName, att, scope, svc, usage)

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
		Validation:    "",
		FullRef:       fullRef,
		NeedType:      defLine != "",
		NilError:      fmt.Sprintf("%s is nil", lowerCamel(typeName)),
		DecodeError:   fmt.Sprintf("decode %s", lowerCamel(typeName)),
		ValidateError: fmt.Sprintf("validate %s", lowerCamel(typeName)),
		EmptyError:    fmt.Sprintf("%s JSON is empty", lowerCamel(typeName)),
		Usage:         usage,
		TypeImports:   imports,
		GenerateCodec: true,
	}
	// Populate pointer and validation-related fields using the attribute type,
	// avoiding any string manipulation of the computed references.
	if _, isUser := att.Type.(goaexpr.UserType); isUser {
		info.Pointer = true
		info.CheckNil = true
	}
	info.MarshalArg = "v"
	info.UnmarshalArg = "v"
	if info.Validation != "" {
		info.ValidationSrc = strings.Split(info.Validation, "\n")
	}
	b.types[key] = info
	// Also ensure any nested service-local user types are materialized locally so
	// unqualified references inside composite shapes compile.
	b.ensureNestedLocalTypes(scope, tt)
	return info, nil
}

// materialize builds the concrete type definition line, its effective attribute
// (for local definitions), the fully-qualified reference with correct pointer
// semantics, and the set of imports needed. For service aliases, ServiceImport
// is returned to drive deterministic imports downstream.
// materialize returns the effective attribute (unchanged), an empty type
// definition (we do not synthesize local types), the fully-qualified reference
// to the owner or primitive type, and the imports required.
//
//nolint:unparam // signature kept stable with previous implementation
func (b *toolSpecBuilder) materialize(
	tool *ToolData,
	typeName string,
	att *goaexpr.AttributeExpr,
	scope *codegen.NameScope,
	svc *service.Data,
	_ typeUsage,
) (
	tt *goaexpr.AttributeExpr,
	defLine string,
	fullRef string,
	imports []*codegen.ImportSpec,
) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return att, "", "", nil
	}

	// Base imports from attribute metadata and locations
	imports = gatherAttributeImports(b.genpkg, att)

	// Compute underlying reference for user types to drive aliasing/imports
	switch dt := att.Type.(type) {
	case goaexpr.UserType:
		// If the user type specifies a custom package, alias to that package/type
		// and preserve the underlying reference. Otherwise, qualify service-local
		// user types against the service package so codecs reference the owner
		// type directly (e.g., *alpha.Doc), while still emitting a stable local
		// alias for readability (e.g., DocPayload = alpha.Doc).
		loc := codegen.UserTypeLocation(dt)
		if loc != nil && loc.PackageName() != "" && loc.RelImportPath != "" {
			pkg := packageName(loc, svc)
			baseRef := scope.GoFullTypeRef(&goaexpr.AttributeExpr{Type: dt}, pkg)
			nonPtr := strings.TrimPrefix(baseRef, "*")
			defLine = typeName + " = " + nonPtr
			fullRef = baseRef
			// external import already captured via gatherAttributeImports
		} else {
			// Service-local user type: alias to the owner service type and set
			// FullRef to the qualified pointer reference so codecs target the
			// canonical service type.
			var utName string
			switch u := dt.(type) {
			case *goaexpr.UserTypeExpr:
				utName = u.TypeName
			case *goaexpr.ResultTypeExpr:
				utName = u.TypeName
			default:
				utName = scope.GoTypeRef(dt.Attribute())
			}
			alias := servicePkgAlias(svc)
			if alias != "" && utName != "" {
				// Ensure the service import is included.
				svcPath := joinImportPath(b.genpkg, svc.PathName)
				imports = append(imports, &codegen.ImportSpec{Name: alias, Path: svcPath})
				defLine = typeName + " = " + alias + "." + utName
				fullRef = "*" + alias + "." + utName
			} else {
				// Fallback to aliasing the underlying composite/value shape.
				comp := scope.GoTypeRef(dt.Attribute())
				defLine = typeName + " = " + comp
				fullRef = "*" + typeName
			}
		}
	case *goaexpr.Array:
		// Alias to the composite type; if the alias would be self-referential
		// (e.g., "T = []*T"), materialize an element helper type and alias to
		// a slice of that element to preserve strong typing.
		comp := scope.GoTypeRef(att)
		if strings.Contains(comp, typeName) {
			// Create element helper type: <TypeName>Item = <elemComp>
			elemName := typeName + "Item"
			elemKey := "name:" + elemName
			if _, exists := b.types[elemKey]; !exists {
				elemComp := scope.GoTypeRef(dt.ElemType)
				b.types[elemKey] = &typeData{
					Key:         elemKey,
					TypeName:    elemName,
					Doc:         elemName + " is a helper element for " + typeName + ".",
					Def:         elemName + " = " + elemComp,
					FullRef:     elemName,
					NeedType:    true,
					TypeImports: gatherAttributeImports(b.genpkg, dt.ElemType),
					// No codecs/schemas for helper element types.
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
		comp := scope.GoTypeRef(att)
		if strings.Contains(comp, typeName) {
			// Create value helper type: <TypeName>Value = <valueComp>
			valName := typeName + "Value"
			valKey := "name:" + valName
			if _, exists := b.types[valKey]; !exists {
				valComp := scope.GoTypeRef(dt.ElemType)
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
			keyRef := scope.GoTypeRef(dt.KeyType)
			defLine = typeName + " = map[" + keyRef + "]" + valName
			fullRef = typeName
		} else {
			defLine = typeName + " = " + comp
			fullRef = typeName
		}
	case *goaexpr.Object, goaexpr.CompositeExpr:
		// Materialize a named struct type using alias to the inline struct.
		// Using an alias keeps marshaling semantics identical to the inline shape.
		comp := scope.GoTypeRef(att)
		defLine = typeName + " = " + comp
		fullRef = typeName
	default:
		// Primitives: refer directly by type
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

// ensureValidationDependencies is a no-op: tool specs do not emit local validation.
// (removed)

// (removed)

// (removed)

func newToolSpecsData(agent *AgentData) *toolSpecsData {
	return &toolSpecsData{
		agent:  agent,
		genpkg: agent.Genpkg,
		types:  make(map[string]*typeData),
	}
}

func newToolSpecBuilder(agent *AgentData) *toolSpecBuilder {
	scope := agent.Service.Scope
	if scope == nil {
		scope = codegen.NewNameScope()
	}
	svcImports := make(map[string]*codegen.ImportSpec)
	for _, im := range agent.Service.UserTypeImports {
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
		genpkg:     agent.Genpkg,
		agent:      agent,
		service:    agent.Service,
		svcScope:   scope,
		svcImports: svcImports,
		types:      make(map[string]*typeData),
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
		comp := scope.GoTypeRef(ut.Attribute())
		td := &typeData{
			Key:         key,
			TypeName:    name,
			Doc:         name + " is a helper type materialized for nested references.",
			Def:         name + " = " + comp,
			FullRef:     name,
			NeedType:    true,
			TypeImports: gatherAttributeImports(b.genpkg, ut.Attribute()),
			// No codecs/schemas/validation for nested helper types.
			ExportedCodec: "",
			GenericCodec:  "",
			GenerateCodec: false,
		}
		b.types[key] = td
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
	return tool.Toolset.Name
}

// gatherAttributeImports collects all import specifications needed for a given
// attribute expression, including imports for user types and meta-type imports.
// It returns a sorted, deduplicated list of import specs.
func gatherAttributeImports(
	genpkg string,
	att *goaexpr.AttributeExpr,
) []*codegen.ImportSpec {
	if att == nil {
		return nil
	}
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
	if svc == nil {
		return ""
	}
	// Always use the service package name so it matches the alias
	// used by Goa's NameScope when computing full type references.
	// Deriving the alias from the filesystem path (path.Base(PathName))
	// can diverge from the actual package identifier (e.g., underscores
	// vs. sanitized names), leading to mismatched qualifiers like
	// "atlasdataagent" vs "atlas_data_agent" in generated code.
	return svc.PkgName
}

// packageName returns the package name for a user type location, falling back
// to the service package name if no location is provided.
func packageName(loc *codegen.Location, svc *service.Data) string {
	if loc != nil {
		return loc.PackageName()
	}
	if svc != nil {
		return svc.PkgName
	}
	return ""
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
		schema.Definitions = openapi.Definitions
	}
	return schema.JSON()
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

// finalizeTypeInfo completes the initialization of a typeData struct by setting
// pointer-related fields, marshal/unmarshal arguments, and validation source lines.
// finalizeTypeInfo removed: pointer and validation fields are derived in ensureType.
