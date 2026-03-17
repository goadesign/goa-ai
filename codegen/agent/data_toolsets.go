// Package codegen keeps toolset and tool metadata construction separate from
// top-level generator assembly.
//
// This file owns the provider-facing part of generator data building: resolving
// the source service for each toolset, deriving canonical names/imports, and
// expanding tool expressions into template-ready metadata. The helpers here are
// pure package-internal builders; they assume Goa evaluation invariants hold and
// panic only when the evaluated design violates those invariants.
package codegen

import (
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"goa.design/goa-ai/codegen/naming"
	agentsExpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	goaexpr "goa.design/goa/v3/expr"
)

// collectToolsets materializes one Used/Exported toolset group into sorted
// ToolsetData entries for a single agent.
func collectToolsets(
	agent *AgentData,
	group *agentsExpr.ToolsetGroupExpr,
	kind ToolsetKind,
	servicesData *service.ServicesData,
) []*ToolsetData {
	if group == nil || len(group.Toolsets) == 0 {
		return nil
	}
	toolsets := make([]*ToolsetData, 0, len(group.Toolsets))
	for _, tsExpr := range group.Toolsets {
		toolsets = append(toolsets, newToolsetData(agent, tsExpr, kind, servicesData))
	}
	slices.SortFunc(toolsets, func(a, b *ToolsetData) int {
		return strings.Compare(a.Name, b.Name)
	})
	return toolsets
}

// newToolsetData resolves one DSL toolset into the generator's canonical
// ToolsetData shape, including provider ownership, local package paths, and the
// concrete tool metadata needed by downstream templates.
func newToolsetData(
	agent *AgentData,
	expr *agentsExpr.ToolsetExpr,
	kind ToolsetKind,
	servicesData *service.ServicesData,
) *ToolsetData {
	toolsetSlug := naming.SanitizeToken(expr.Name, "toolset")
	serviceName := agent.Service.Name
	queue := naming.QueueName(agent.Service.PathName, agent.Slug, toolsetSlug, "tasks")
	qualifiedName := expr.Name
	sourceService := agent.Service

	// If this is an MCP toolset, use the service name from the MCP service.
	if expr.Provider != nil && expr.Provider.Kind == agentsExpr.ProviderMCP && servicesData != nil && expr.Provider.MCPService != "" {
		if svc := servicesData.Get(expr.Provider.MCPService); svc != nil {
			sourceService = svc
		}
	}
	// Track provider service name (when referencing an exported toolset).
	var originServiceName string
	if expr.Origin != nil && expr.Origin.Agent != nil && expr.Origin.Agent.Service != nil {
		originServiceName = expr.Origin.Agent.Service.Name
	}
	// If this toolset references an origin from another agent, inherit the source service.
	if originServiceName != "" && servicesData != nil {
		if svc := servicesData.Get(originServiceName); svc != nil {
			sourceService = svc
		}
	}
	// If this is a method-backed toolset, prefer the service referenced by the
	// first bound method (BindTo) when present.
	isMCPBacked := expr.Provider != nil && expr.Provider.Kind == agentsExpr.ProviderMCP
	if !isMCPBacked && servicesData != nil && len(expr.Tools) > 0 {
		if svcName := expr.Tools[0].BoundServiceName(); svcName != "" {
			if svc := servicesData.Get(svcName); svc != nil {
				sourceService = svc
			}
		}
	}
	sourceServiceName := serviceName
	if sourceService != nil && sourceService.Name != "" {
		sourceServiceName = sourceService.Name
	} else if expr.Provider != nil && expr.Provider.MCPService != "" {
		sourceServiceName = expr.Provider.MCPService
	}
	var imports map[string]*codegen.ImportSpec
	if sourceService != nil {
		imports = buildServiceImportMap(sourceService)
	}

	// When consuming a local toolset (defined within this agent/service), qualify
	// it under the consumer namespace to prevent collisions. When the toolset is
	// exported by another agent (Origin set and different service), reuse the
	// provider's canonical name so callers see consistent identifiers end-to-end.
	if kind == ToolsetKindUsed && !isMCPBacked {
		if originServiceName == "" || originServiceName == agent.Service.Name {
			qualifiedName = fmt.Sprintf("%s.%s", sourceServiceName, expr.Name)
		}
	}

	ts := &ToolsetData{
		Expr:              expr,
		Name:              expr.Name,
		Title:             naming.HumanizeTitle(expr.Name),
		Description:       expr.Description,
		Tags:              slices.Clone(expr.Tags),
		ServiceName:       serviceName,
		SourceServiceName: sourceServiceName,
		SourceService:     sourceService,
		// QualifiedName is the toolset-scoped identifier (`toolset`).
		QualifiedName:        qualifiedName,
		TaskQueue:            queue,
		Kind:                 kind,
		Agent:                agent,
		PathName:             toolsetSlug,
		PackageName:          toolsetSlug,
		SourceServiceImports: imports,
		PackageImportPath:    path.Join(agent.ImportPath, toolsetSlug),
		Dir:                  filepath.Join(agent.Dir, toolsetSlug),
		SpecsPackageName:     toolsetSlug,
		// SpecsImportPath/SpecsDir are assigned after building the complete design
		// so ownership can be resolved deterministically (service-owned vs agent-exported).
		SpecsImportPath: "",
		SpecsDir:        "",
	}

	if kind == ToolsetKindExported {
		ts.AgentToolsPackage = toolsetSlug
		ts.AgentToolsDir = filepath.Join(agent.Dir, "agenttools", toolsetSlug)
		ts.AgentToolsImportPath = path.Join(agent.ImportPath, "agenttools", toolsetSlug)
	}

	// Handle toolset based on provider type.
	switch {
	case expr.Provider != nil && expr.Provider.Kind == agentsExpr.ProviderRegistry:
		ts.IsRegistryBacked = true
		regName := ""
		if expr.Provider.Registry != nil {
			regName = expr.Provider.Registry.Name
		}
		regPkgName := codegen.SnakeCase(regName)
		regClientImport := path.Join(agent.Genpkg, agent.Service.PathName, "registry", regPkgName)
		regClientAlias := "reg" + codegen.Goify(regName, false)
		ts.Registry = &RegistryToolsetMeta{
			RegistryName:             regName,
			ToolsetName:              expr.Provider.ToolsetName,
			Version:                  expr.Provider.Version,
			QualifiedName:            ts.QualifiedName,
			RegistryClientImportPath: regClientImport,
			RegistryClientAlias:      regClientAlias,
		}
		// Registry toolsets have no compile-time tools; they are discovered at runtime.
		// The Tools slice remains empty; specs generation will create placeholder
		// structures that are populated via runtime discovery.

	case isMCPBacked:
		mcpService := expr.Provider.MCPService
		mcpToolset := expr.Provider.MCPToolset
		if mcpService != "" {
			ts.SourceServiceName = mcpService
		}
		helperPkg := "mcp_" + codegen.SnakeCase(mcpService)
		helperImport := path.Join(agent.Genpkg, helperPkg)
		helperAlias := "mcp" + codegen.Goify(mcpService, false)
		helperFunc := fmt.Sprintf("Register%s%sToolset",
			codegen.Goify(mcpService, true), codegen.Goify(mcpToolset, true))
		ts.MCP = &MCPToolsetMeta{
			ServiceName:      mcpService,
			SuiteName:        mcpToolset,
			QualifiedName:    ts.QualifiedName,
			HelperImportPath: helperImport,
			HelperAlias:      helperAlias,
			HelperFunc:       helperFunc,
			ConstName:        mcpToolsetConstName(agent.GoName, mcpService, mcpToolset, toolsetSlug),
		}
		// Populate from Goa-backed MCP if available; otherwise keep/derive tools
		// from inline declarations (custom external MCP).
		found := populateMCPToolset(ts)
		if !found && len(expr.Tools) > 0 {
			for _, toolExpr := range expr.Tools {
				tool := newToolData(ts, toolExpr, servicesData)
				ts.Tools = append(ts.Tools, tool)
			}
			slices.SortFunc(ts.Tools, func(a, b *ToolData) int {
				return strings.Compare(a.Name, b.Name)
			})
		}

	default:
		for _, toolExpr := range expr.Tools {
			tool := newToolData(ts, toolExpr, servicesData)
			ts.Tools = append(ts.Tools, tool)
		}
		slices.SortFunc(ts.Tools, func(a, b *ToolData) int {
			return strings.Compare(a.Name, b.Name)
		})
		// Any method-backed tool requires an adapter; no bypass logic.
		for _, t := range ts.Tools {
			if t.IsMethodBacked {
				ts.NeedsAdapter = true
				break
			}
		}
	}

	return ts
}

// buildServiceImportMap indexes the user-type imports already computed by Goa
// service analysis so tool specs can reuse the same aliasing decisions.
func buildServiceImportMap(svc *service.Data) map[string]*codegen.ImportSpec {
	if len(svc.UserTypeImports) == 0 {
		return nil
	}
	imports := make(map[string]*codegen.ImportSpec)
	for _, im := range svc.UserTypeImports {
		if im == nil || im.Path == "" {
			continue
		}
		alias := im.Name
		if alias == "" {
			alias = path.Base(im.Path)
		}
		imports[alias] = im
	}
	return imports
}

// newToolData resolves one tool expression into the template data consumed by
// executor/spec generation, including method binding metadata when applicable.
func newToolData(ts *ToolsetData, expr *agentsExpr.ToolExpr, servicesData *service.ServicesData) *ToolData {
	// ts is guaranteed non-nil by construction (collectToolsets/newToolsetData)
	// and ts.QualifiedName is always set there.
	qualified := fmt.Sprintf("%s.%s", ts.Name, expr.Name)

	// Check if this tool is exported by an agent (agent-as-tool pattern)
	isExported := ts.Kind == ToolsetKindExported && ts.Agent != nil
	var exportingAgentID string
	if isExported {
		exportingAgentID = ts.Agent.ID
	}

	tool := &ToolData{
		Name:               expr.Name,
		ConstName:          codegen.Goify(expr.Name, true),
		Description:        expr.Description,
		QualifiedName:      qualified,
		Title:              naming.HumanizeTitle(defaultString(expr.Title, expr.Name)),
		Tags:               slices.Clone(expr.Tags),
		Meta:               map[string][]string(expr.Meta),
		Args:               expr.Args,
		Return:             expr.Return,
		Toolset:            ts,
		IsExportedByAgent:  isExported,
		ExportingAgentID:   exportingAgentID,
		CallHintTemplate:   expr.CallHintTemplate,
		ResultHintTemplate: expr.ResultHintTemplate,
		InjectedFields:     expr.InjectedFields,
		Bounds:             boundsData(expr.Bounds, expr.Method),
		TerminalRun:        expr.TerminalRun,
		ResultReminder:     expr.ResultReminder,
	}
	tool.ServerData = serverDataData(expr.ServerData, qualified)
	if expr.Confirmation != nil {
		tool.Confirmation = &ToolConfirmationData{
			Title:                expr.Confirmation.Title,
			PromptTemplate:       expr.Confirmation.PromptTemplate,
			DeniedResultTemplate: expr.Confirmation.DeniedResultTemplate,
		}
	}
	if expr.ExportPassthrough != nil {
		tool.PassthroughService = expr.ExportPassthrough.TargetService
		tool.PassthroughMethod = expr.ExportPassthrough.TargetMethod
	}
	if expr.Method == nil {
		return tool
	}
	tool.IsMethodBacked = true
	// Populate exact payload/result type names using Goa service metadata.
	if servicesData == nil || ts.SourceService == nil {
		return tool
	}
	sd := servicesData.Get(ts.SourceService.Name)
	if sd == nil {
		return tool
	}
	for _, md := range sd.Methods {
		if md.Name != expr.Method.Name {
			continue
		}
		tool.MethodGoName = md.VarName
		tool.MethodPayloadTypeName = md.Payload
		tool.MethodResultTypeName = md.Result

		// Resolve fully-qualified type references using Goa name scope.
		// If the payload/result user types do not specify a custom package
		// (no struct:pkg:path), Goa leaves PayloadRef/ResultRef unqualified
		// because the service code is generated in the same package.
		// Our generated code may live in a different package; qualify
		// local types with the source service package using the service
		// name scope to avoid manual string surgery.
		me := mustFindMethodExpr(servicesData.Root, sd.Name, md.Name)
		if me != nil && me.Payload.Type != goaexpr.Empty {
			// Expose attribute for template default adapter generation.
			tool.MethodPayloadAttr = me.Payload
			if md.PayloadLoc != nil && md.PayloadLoc.PackageName() != "" {
				tool.MethodPayloadTypeRef = md.PayloadRef
			} else {
				// Use Goa's NameScope to compute the fully-qualified type reference.
				// sd.Scope is guaranteed non-nil by Goa's service data construction.
				tool.MethodPayloadTypeRef = sd.Scope.GoFullTypeRef(me.Payload, sd.PkgName)
			}
		}
		if me != nil && me.Result.Type != goaexpr.Empty {
			tool.MethodResultAttr = me.Result
			if md.ResultLoc != nil && md.ResultLoc.PackageName() != "" {
				tool.MethodResultTypeRef = md.ResultRef
			} else {
				// Use Goa's NameScope to compute the fully-qualified type reference.
				// sd.Scope is guaranteed non-nil by Goa's service data construction.
				tool.MethodResultTypeRef = sd.Scope.GoFullTypeRef(me.Result, sd.PkgName)
			}
		}
		// Capture user type locations when specified via struct:pkg:path.
		tool.MethodPayloadLoc = md.PayloadLoc
		tool.MethodResultLoc = md.ResultLoc
		break
	}
	if tool.MethodGoName == "" {
		panic(fmt.Sprintf(
			"method %q not found in service %q for tool %q",
			expr.Method.Name,
			ts.SourceService.Name,
			tool.QualifiedName,
		))
	}
	// Derive HasResult from tool.Return or bound method result.
	tool.HasResult = (tool.Return != nil && tool.Return.Type != goaexpr.Empty) || (tool.MethodResultAttr != nil && tool.MethodResultAttr.Type != goaexpr.Empty)
	// Compute aliasing flags for payload and result against method types when bound.
	if tool.IsMethodBacked {
		tool.PayloadAliasesMethod = ToolAttrAliasesMethod(tool.Args, tool.MethodPayloadAttr)
		if tool.HasResult {
			tool.ResultAliasesMethod = ToolAttrAliasesMethod(tool.Return, tool.MethodResultAttr)
		}
	}
	return tool
}

// pagingData converts the optional DSL paging contract into generator data.
func pagingData(p *agentsExpr.ToolPagingExpr) *ToolPagingData {
	if p == nil {
		return nil
	}
	return &ToolPagingData{
		CursorField:     p.CursorField,
		NextCursorField: p.NextCursorField,
	}
}

// boundsData projects tool bounds metadata and, when a bound method is known,
// captures the concrete result fields that implement those bounds.
func boundsData(bounds *agentsExpr.ToolBoundsExpr, method *goaexpr.MethodExpr) *ToolBoundsData {
	if bounds == nil {
		return nil
	}
	data := &ToolBoundsData{
		Paging: pagingData(bounds.Paging),
	}
	if method == nil {
		return data
	}
	data.Projection = &ToolBoundsProjectionData{
		Returned:       boundsFieldData(method.Result, "returned"),
		Total:          boundsFieldData(method.Result, "total"),
		Truncated:      boundsFieldData(method.Result, "truncated"),
		RefinementHint: boundsFieldData(method.Result, "refinement_hint"),
	}
	if bounds.Paging != nil && bounds.Paging.NextCursorField != "" {
		data.Projection.NextCursor = boundsFieldData(method.Result, bounds.Paging.NextCursorField)
	}
	return data
}

// boundsFieldData resolves one result-field projection used by tool bounds.
func boundsFieldData(result *goaexpr.AttributeExpr, name string) *ToolBoundsFieldData {
	if result == nil || result.Type == nil || result.Type == goaexpr.Empty {
		return nil
	}
	field := result.Find(name)
	if field == nil || field.Type == nil || field.Type == goaexpr.Empty {
		return nil
	}
	return &ToolBoundsFieldData{
		Name:     codegen.Goify(name, true),
		Required: result.IsRequired(name),
	}
}

// serverDataData materializes optional server-data sidecars attached to one
// tool, including inferred descriptions when the DSL leaves them blank.
func serverDataData(exprs []*agentsExpr.ServerDataExpr, qualified string) []*ServerDataData {
	if len(exprs) == 0 {
		return nil
	}
	out := make([]*ServerDataData, 0, len(exprs))
	for _, sd := range exprs {
		item := &ServerDataData{
			Kind:     defaultString(sd.Kind, qualified),
			Audience: sd.Audience,
			Schema:   sd.Schema,
		}
		item.Description = strings.TrimSpace(sd.Description)
		if item.Description == "" {
			item.Description = serverDataDescription(sd.Schema)
		}
		if sd.Source != nil {
			item.MethodResultField = strings.TrimSpace(sd.Source.MethodResultField)
		}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// serverDataDescription returns a human-facing description for an optional
// server-data schema attribute. It prefers the attribute Description set in the
// DSL and falls back to the underlying user type description when needed.
func serverDataDescription(att *goaexpr.AttributeExpr) string {
	if att == nil {
		return ""
	}
	if att.Description != "" {
		return att.Description
	}
	ut, ok := att.Type.(goaexpr.UserType)
	if !ok || ut == nil {
		return ""
	}
	uattr := ut.Attribute()
	if uattr == nil {
		return ""
	}
	return uattr.Description
}

// mustFindMethodExpr locates the Goa method expression for the given service
// and method names. It returns nil when the service is absent and otherwise
// relies on Goa evaluation guarantees for method lookup.
func mustFindMethodExpr(root *goaexpr.RootExpr, serviceName, methodName string) *goaexpr.MethodExpr {
	svc := root.Service(serviceName)
	if svc == nil {
		return nil
	}
	return svc.Method(methodName)
}

// mcpToolsetConstName derives a stable agent-local identifier for an external
// MCP toolset. Explicit Service/Suite/Alias separators preserve the provider
// namespace so different partitions cannot collapse into the same Go name.
func mcpToolsetConstName(agentGoName, serviceName, suiteName, toolsetSlug string) string {
	constName := fmt.Sprintf(
		"%s%sService%sSuite",
		agentGoName,
		codegen.Goify(serviceName, true),
		codegen.Goify(suiteName, true),
	)
	if toolsetSlug != naming.SanitizeToken(suiteName, "toolset") {
		constName += codegen.Goify(toolsetSlug, true) + "Alias"
	}
	return constName + "ToolsetID"
}
