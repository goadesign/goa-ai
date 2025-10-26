// Package codegen provides code generation for agent-based systems.
//
// This file (specs_builder.go) implements tool specification generation. It transforms
// Goa design expressions into JSON schemas, type definitions, and codec functions that
// the agent runtime uses to marshal/unmarshal tool arguments and results.
//
// The generation process:
//  1. buildToolSpecsData scans all tools in an agent
//  2. For each tool, creates typeData for payload and result types
//  3. Generates JSON schemas for runtime validation
//  4. Produces marshal/unmarshal functions and validation code
//  5. Handles cross-service type references (for MCP and external toolsets)
//
// Generated artifacts are consumed by templates (tool_spec.go.tpl, tool_types.go.tpl,
// tool_codecs.go.tpl) to produce the tool_specs package under each agent.
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
	// toolSpecsData aggregates all type and codec info for an agent's tools.
	toolSpecsData struct {
		agent   *AgentData
		genpkg  string
		imports map[string]*codegen.ImportSpec
		types   map[string]*typeData
		order   []*typeData
		tools   []*toolEntry
	}

	// toolEntry pairs a tool declaration with its payload/result type metadata.
	toolEntry struct {
		Name              string
		DisplayName       string
		Service           string
		Toolset           string
		Description       string
		Tags              []string
		IsExportedByAgent bool
		ExportingAgentID  string
		Payload           *typeData
		Result            *typeData

		// Method backed flags for downstream templates (future use for adapters).
		IsMethodBacked bool
		MethodGoName   string
	}

	// typeData is used by the templates to render the tool specs.
	typeData struct {
		Key           string
		TypeName      string
		Doc           string
		Def           string
		SchemaVar     string
		SchemaLiteral string
		ExportedCodec string
		GenericCodec  string
		MarshalFunc   string
		UnmarshalFunc string
		ValidateFunc  string
		Validation    string
		HasValidation bool
		FullRef       string
		ElemRef       string
		Pointer       bool
		CheckNil      bool
		MarshalArg    string
		UnmarshalArg  string
		ValidationSrc []string
		NeedType      bool
		Import        *codegen.ImportSpec
		NilError      string
		DecodeError   string
		ValidateError string
		EmptyError    string
		Usage         typeUsage
		TypeImports   []*codegen.ImportSpec
		GenerateCodec bool
	}

	// toolSpecBuilder walks tool types and generates corresponding type metadata,
	// schemas, and validation code. It maintains caches for deduplication and
	// handles cross-service type references for MCP and external toolsets.
	toolSpecBuilder struct {
		genpkg     string
		agent      *AgentData
		service    *service.Data
		typeScope  *codegen.NameScope
		svcScope   *codegen.NameScope
		svcImports map[string]*codegen.ImportSpec
		types      map[string]*typeData
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
	data := newToolSpecsData(agent)
	builder := newToolSpecBuilder(agent)
	for _, tool := range agent.Tools {
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
	sort.Slice(data.tools, func(i, j int) bool {
		return data.tools[i].Name < data.tools[j].Name
	})
	return data, nil
}

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

func toolsetName(tool *ToolData) string {
	return tool.Toolset.Name
}

func newToolSpecsData(agent *AgentData) *toolSpecsData {
	imports := make(map[string]*codegen.ImportSpec)
	imports[agent.Service.PkgName] = &codegen.ImportSpec{
		Name: agent.Service.PkgName,
		Path: joinImportPath(agent.Genpkg, agent.Service.PathName),
	}
	for _, im := range agent.Service.UserTypeImports {
		alias := im.Name
		if alias == "" {
			alias = path.Base(im.Path)
		}
		imports[alias] = im
	}
	return &toolSpecsData{
		agent:   agent,
		genpkg:  agent.Genpkg,
		imports: imports,
		types:   make(map[string]*typeData),
	}
}

func (d *toolSpecsData) addTool(entry *toolEntry) {
	if entry == nil {
		return
	}
	d.tools = append(d.tools, entry)
	d.addType(entry.Payload)
	d.addType(entry.Result)
}

func (d *toolSpecsData) addType(info *typeData) {
	if info == nil {
		return
	}
	if _, ok := d.types[info.Key]; ok {
		return
	}
	d.types[info.Key] = info
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
		if info.HasValidation {
			return true
		}
	}
	return false
}

func (d *toolSpecsData) needsUnicodeImport() bool {
	for _, info := range d.order {
		if info.HasValidation && strings.Contains(info.Validation, "utf8.") {
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
	for _, im := range d.imports {
		if im.Path != "" {
			uniq[im.Path] = im
		}
	}
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
		codegen.SimpleImport("fmt"),
		codegen.SimpleImport("goa.design/goa-ai/agents/runtime/tools"),
	}
	if d.needsUnicodeImport() {
		base = append(base, codegen.SimpleImport("unicode/utf8"))
	}
	if d.needsGoaImport() {
		base = append(base, codegen.GoaImport(""))
	}
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
		for _, im := range info.TypeImports {
			if im.Path == "" {
				continue
			}
			extra[im.Path] = im
		}
		if !needsServiceImport &&
			d.agent.Service.PkgName != "" &&
			strings.Contains(info.FullRef, d.agent.Service.PkgName+".") {
			needsServiceImport = true
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
	return base
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
		typeScope:  codegen.NewNameScope(),
		svcScope:   scope,
		svcImports: svcImports,
		types:      make(map[string]*typeData),
	}
}

func (b *toolSpecBuilder) serviceForTool(tool *ToolData) *service.Data {
	if tool.Toolset.SourceService != nil {
		return tool.Toolset.SourceService
	}
	return b.agent.Service
}

func (b *toolSpecBuilder) scopeForTool(tool *ToolData) *codegen.NameScope {
	svc := b.serviceForTool(tool)
	if svc.Scope != nil {
		return svc.Scope
	}
	return b.svcScope
}

func (b *toolSpecBuilder) importsForTool(tool *ToolData) map[string]*codegen.ImportSpec {
	if len(tool.Toolset.SourceServiceImports) > 0 {
		return tool.Toolset.SourceServiceImports
	}
	return b.svcImports
}

func (b *toolSpecBuilder) typeFor(tool *ToolData, att *goaexpr.AttributeExpr, usage typeUsage) (*typeData, error) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return nil, nil
	}
	info, err := b.ensurePureType(tool, att, usage)
	if err != nil {
		return nil, err
	}
	scope := b.scopeForTool(tool)
	if err := b.ensureValidationDependencies(att, scope); err != nil {
		return nil, err
	}
	return info, nil
}

// ensurePureType generates a standalone type definition and metadata for a tool's
// payload or result. It creates a unique type name, generates the Go type definition,
// produces an OpenAPI schema, and collects validation code.
//
// For external MCP tools, it duplicates user type attributes to avoid cross-service
// type pollution. For cross-service references, it generates qualified type references
// with appropriate import specs.
//
// The generated typeData includes all information needed by templates to render type
// definitions, codecs, and validation functions. Types are cached by key to ensure
// each unique type is only generated once.
func (b *toolSpecBuilder) ensurePureType(
	tool *ToolData,
	att *goaexpr.AttributeExpr,
	usage typeUsage,
) (*typeData, error) {
	if tool != nil && tool.Toolset != nil && tool.Toolset.Expr != nil && tool.Toolset.Expr.External {
		if ut, ok := att.Type.(goaexpr.UserType); ok {
			if dup := goaexpr.DupAtt(ut.Attribute()); dup != nil {
				dup.Type = ut
				att = dup
			}
		}
	}
	base := codegen.Goify(tool.Toolset.Name+"_"+tool.Name, true)
	if base == "" {
		base = codegen.Goify(tool.Name, true)
	}
	switch usage {
	case usagePayload:
		base += "Payload"
	case usageResult:
		base += "Result"
	}
	typeName := b.typeScope.Unique(base, "")
	key := "*" + typeName
	if existing := b.types[key]; existing != nil {
		return existing, nil
	}
	scope := b.scopeForTool(tool)
	toolImports := b.importsForTool(tool)
	imports := gatherAttributeImports(b.genpkg, att, toolImports, scope)
	svc := b.serviceForTool(tool)
	if svc.Name != b.agent.Service.Name && svc.PathName != "" {
		svcPath := joinImportPath(b.agent.Genpkg, svc.PathName)
		exists := false
		for _, im := range imports {
			if im.Path == svcPath {
				exists = true
				break
			}
		}
		if !exists {
			svcImport := &codegen.ImportSpec{
				Name: svc.PkgName,
				Path: svcPath,
			}
			imports = append(imports, svcImport)
		}
	}
	doc := fmt.Sprintf("%s defines the JSON %s for the %s tool.", typeName, usage, tool.QualifiedName)
	baseDef := b.typeScope.GoTypeDef(att, false, true)
	aliasRef := ""
	if ut, ok := att.Type.(goaexpr.UserType); ok {
		svc := b.serviceForTool(tool)
		if svc.Name != b.agent.Service.Name && svc.PkgName != "" {
			utName := codegen.Goify(ut.Name(), true)
			aliasRef = fmt.Sprintf("%s.%s", svc.PkgName, utName)
			baseDef = aliasRef
		} else {
			qref := qualifiedRef(att, scope)
			if qref != "" && strings.Contains(qref, ".") {
				baseDef = qref
			} else if loc := codegen.UserTypeLocation(ut); loc != nil && loc.RelImportPath != "" {
				baseDef = fmt.Sprintf("%s.%s", loc.PackageName(), codegen.Goify(ut.Name(), true))
			} else {
				alias := aliasFromRef(qref)
				if alias == "" {
					for _, im := range imports {
						if im.Name != "" {
							alias = im.Name
							break
						}
					}
				}
				if alias != "" {
					baseDef = fmt.Sprintf("%s.%s", alias, codegen.Goify(ut.Name(), true))
				}
			}
		}
	}
	def := fmt.Sprintf("%s %s", typeName, baseDef)
	schemaBytes, err := schemaForAttribute(att)
	if err != nil {
		return nil, err
	}
	schemaLiteral := formatSchema(schemaBytes)
	schemaVar := ""
	if schemaLiteral != "" {
		schemaVar = lowerCamel(typeName) + "Schema"
	}
	validation := buildValidation(att, "", scope)
	if aliasRef != "" {
		validation = ""
	}
	if ut, ok := att.Type.(goaexpr.UserType); ok {
		if fields := aliasValueFields(ut); len(fields) > 0 {
			validation = normalizeAliasValidation(validation, fields)
		}
	}
	info := &typeData{
		Key:           key,
		TypeName:      typeName,
		Doc:           doc,
		Def:           def,
		SchemaVar:     schemaVar,
		SchemaLiteral: schemaLiteral,
		ExportedCodec: typeName + "Codec",
		GenericCodec:  lowerCamel(typeName) + "Codec",
		MarshalFunc:   "Marshal" + typeName,
		UnmarshalFunc: "Unmarshal" + typeName,
		ValidateFunc:  "Validate" + typeName,
		Validation:    validation,
		HasValidation: validation != "",
		FullRef:       "*" + typeName,
		ElemRef:       typeName,
		NeedType:      true,
		NilError:      fmt.Sprintf("%s is nil", lowerCamel(typeName)),
		DecodeError:   fmt.Sprintf("decode %s", lowerCamel(typeName)),
		ValidateError: fmt.Sprintf("validate %s", lowerCamel(typeName)),
		EmptyError:    fmt.Sprintf("%s JSON is empty", lowerCamel(typeName)),
		Usage:         usage,
		TypeImports:   imports,
		GenerateCodec: true,
	}
	if aliasRef != "" {
		info.FullRef = aliasRef
		info.ElemRef = aliasRef
		info.CheckNil = false
	}
	finalizeTypeInfo(info)
	b.types[key] = info
	return info, nil
}

func (b *toolSpecBuilder) ensureValidationDependencies(att *goaexpr.AttributeExpr, scope *codegen.NameScope) error {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return nil
	}
	if scope == nil {
		scope = b.svcScope
	}
	if scope == nil {
		scope = b.typeScope
	}
	return b.walkValidationDependencies(att, scope, make(map[string]struct{}))
}

func (b *toolSpecBuilder) walkValidationDependencies(
	att *goaexpr.AttributeExpr,
	scope *codegen.NameScope,
	seen map[string]struct{},
) error {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return nil
	}
	switch dt := att.Type.(type) {
	case goaexpr.UserType:
		if _, ok := seen[dt.ID()]; !ok {
			seen[dt.ID()] = struct{}{}
			if err := b.ensureUserTypeValidation(dt, scope); err != nil {
				return err
			}
			if err := b.walkValidationDependencies(dt.Attribute(), scope, seen); err != nil {
				return err
			}
		}
	case *goaexpr.Object:
		for _, nat := range *dt {
			if err := b.walkValidationDependencies(nat.Attribute, scope, seen); err != nil {
				return err
			}
		}
	case *goaexpr.Array:
		if err := b.walkValidationDependencies(dt.ElemType, scope, seen); err != nil {
			return err
		}
	case *goaexpr.Map:
		if err := b.walkValidationDependencies(dt.KeyType, scope, seen); err != nil {
			return err
		}
		if err := b.walkValidationDependencies(dt.ElemType, scope, seen); err != nil {
			return err
		}
	case *goaexpr.Union:
		for _, nat := range dt.Values {
			if err := b.walkValidationDependencies(nat.Attribute, scope, seen); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *toolSpecBuilder) ensureUserTypeValidation(ut goaexpr.UserType, scope *codegen.NameScope) error {
	if ut == nil {
		return nil
	}
	attr := &goaexpr.AttributeExpr{Type: ut}
	pkg := packageName(codegen.UserTypeLocation(ut), b.service)
	if scope == nil {
		scope = b.typeScope
	}
	fullRef := scope.GoFullTypeRef(attr, pkg)
	if fullRef == "" {
		fullRef = b.typeScope.GoFullTypeRef(attr, pkg)
	}
	if fullRef == "" {
		return fmt.Errorf("tools: unable to compute type reference for user type %q", ut.Name())
	}
	if info := b.types[fullRef]; info != nil {
		return nil
	}
	ctx := &codegen.AttributeContext{Scope: codegen.NewAttributeScope(scope)}
	if pkg != "" {
		ctx.DefaultPkg = pkg
	}
	ctx.UseDefault = true
	validation := strings.TrimSpace(codegen.ValidationCode(
		ut.Attribute(), ut, ctx, true, goaexpr.IsAlias(ut), false, "body",
	))
	if validation == "" {
		return nil
	}
	if fields := aliasValueFields(ut); len(fields) > 0 {
		validation = normalizeAliasValidation(validation, fields)
	}
	typeName := scope.GoTypeName(attr)
	if typeName == "" {
		typeName = codegen.Goify(ut.Name(), true)
	}
	imports := gatherAttributeImports(b.genpkg, attr, b.svcImports, scope)
	info := &typeData{
		Key:           fullRef,
		TypeName:      typeName,
		ExportedCodec: typeName + "Codec",
		GenericCodec:  lowerCamel(typeName) + "Codec",
		MarshalFunc:   "Marshal" + typeName,
		UnmarshalFunc: "Unmarshal" + typeName,
		ValidateFunc:  "Validate" + typeName,
		Validation:    validation,
		HasValidation: true,
		FullRef:       fullRef,
		ElemRef:       strings.TrimPrefix(fullRef, "*"),
		NilError:      fmt.Sprintf("%s is nil", lowerCamel(typeName)),
		DecodeError:   fmt.Sprintf("decode %s", lowerCamel(typeName)),
		ValidateError: fmt.Sprintf("validate %s", lowerCamel(typeName)),
		EmptyError:    fmt.Sprintf("%s JSON is empty", lowerCamel(typeName)),
		TypeImports:   imports,
		GenerateCodec: false,
	}
	finalizeTypeInfo(info)
	if b.service.PkgName != "" &&
		b.service.PathName != "" &&
		strings.Contains(fullRef, b.service.PkgName+".") {
		info.Import = &codegen.ImportSpec{
			Name: b.service.PkgName,
			Path: joinImportPath(b.genpkg, b.service.PathName),
		}
	} else if loc := codegen.UserTypeLocation(ut); loc != nil && loc.RelImportPath != "" {
		info.Import = &codegen.ImportSpec{
			Name: loc.PackageName(),
			Path: joinImportPath(b.genpkg, loc.RelImportPath),
		}
	}
	b.types[fullRef] = info
	return nil
}

func packageName(loc *codegen.Location, svc *service.Data) string {
	if loc != nil {
		return loc.PackageName()
	}
	if svc != nil {
		return svc.PkgName
	}
	return ""
}

func gatherAttributeImports(
	genpkg string,
	att *goaexpr.AttributeExpr,
	svcImports map[string]*codegen.ImportSpec,
	scope *codegen.NameScope,
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
			} else if scope != nil && svcImports != nil {
				if alias := aliasFromRef(scope.GoFullTypeRef(a, "")); alias != "" {
					if im := svcImports[alias]; im.Path != "" {
						uniq[im.Path] = im
					}
				}
			}
			visit(dt.Attribute())
		case *goaexpr.Array:
			visit(dt.ElemType)
		case *goaexpr.Map:
			visit(dt.KeyType)
			visit(dt.ElemType)
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

func aliasFromRef(ref string) string {
	if ref == "" {
		return ""
	}
	ref = strings.TrimPrefix(ref, "*")
	if idx := strings.Index(ref, "."); idx > 0 {
		return ref[:idx]
	}
	return ""
}

func qualifiedRef(att *goaexpr.AttributeExpr, scope *codegen.NameScope) string {
	if scope == nil {
		return ""
	}
	ref := scope.GoFullTypeRef(att, "")
	return strings.TrimPrefix(ref, "*")
}

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

func buildValidation(att *goaexpr.AttributeExpr, pkg string, scope *codegen.NameScope) string {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return ""
	}
	ctx := codegen.NewAttributeContext(false, false, true, pkg, scope)
	return strings.TrimSpace(codegen.ValidationCode(att, nil, ctx, true, goaexpr.IsAlias(att.Type), false, "body"))
}

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

func formatSchema(schema []byte) string {
	if len(schema) == 0 {
		return ""
	}
	content := string(schema)
	return "[]byte(`\n" + content + "\n`)"
}

func lowerCamel(s string) string {
	return codegen.Goify(s, false)
}

func finalizeTypeInfo(info *typeData) {
	info.Pointer = strings.HasPrefix(info.FullRef, "*")
	if info.Pointer {
		info.CheckNil = true
		info.MarshalArg = "v"
		info.UnmarshalArg = "&v"
	} else {
		info.MarshalArg = "v"
		info.UnmarshalArg = "v"
	}
	if info.Validation != "" {
		info.ValidationSrc = strings.Split(info.Validation, "\n")
	}
}

func aliasValueFields(ut goaexpr.UserType) []string {
	return collectAliasValueFields(ut, make(map[string]struct{}))
}

func collectAliasValueFields(ut goaexpr.UserType, seen map[string]struct{}) []string {
	if ut == nil {
		return nil
	}
	if _, ok := seen[ut.ID()]; ok {
		return nil
	}
	seen[ut.ID()] = struct{}{}
	obj := goaexpr.AsObject(ut.Attribute().Type)
	if obj == nil {
		if goaexpr.IsAlias(ut) {
			if aut, ok := ut.Attribute().Type.(goaexpr.UserType); ok {
				return collectAliasValueFields(aut, seen)
			}
		}
		return nil
	}
	fields := make([]string, 0, len(*obj))
	for _, nat := range *obj {
		aut, ok := nat.Attribute.Type.(goaexpr.UserType)
		if !ok || !goaexpr.IsAlias(aut) {
			continue
		}
		if ut.Attribute().IsPrimitivePointer(nat.Name, true) {
			continue
		}
		if aut.Attribute() == nil || aut.Attribute().Type == nil || aut.Attribute().Type.Kind() != goaexpr.StringKind {
			continue
		}
		fields = append(fields, codegen.GoifyAtt(nat.Attribute, nat.Name, true))
	}
	if goaexpr.IsAlias(ut) {
		if aut, ok := ut.Attribute().Type.(goaexpr.UserType); ok {
			fields = append(fields, collectAliasValueFields(aut, seen)...)
		}
	}
	return fields
}

func normalizeAliasValidation(validation string, fields []string) string {
	for _, field := range fields {
		path := "body." + field
		validation = strings.ReplaceAll(validation, path+" != nil", path+" != \"\"")
		validation = strings.ReplaceAll(validation, path+" == nil", path+" == \"\"")
		validation = strings.ReplaceAll(validation, "string(*"+path+")", "string("+path+")")
		validation = strings.ReplaceAll(validation, "*"+path, path)
	}
	return validation
}
