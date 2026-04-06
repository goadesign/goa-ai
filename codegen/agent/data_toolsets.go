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
	"slices"
	"strings"

	ir "goa.design/goa-ai/codegen/ir"
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
	refs []*ir.ToolsetRef,
	servicesData *service.ServicesData,
) ([]*ToolsetData, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	toolsets := make([]*ToolsetData, 0, len(refs))
	for _, ref := range refs {
		ts, err := newToolsetData(agent, ref, servicesData)
		if err != nil {
			return nil, err
		}
		toolsets = append(toolsets, ts)
	}
	slices.SortFunc(toolsets, func(a, b *ToolsetData) int {
		return strings.Compare(a.Name, b.Name)
	})
	return toolsets, nil
}

// newToolsetData resolves one DSL toolset into the generator's canonical
// ToolsetData shape, including provider ownership, local package paths, and the
// concrete tool metadata needed by downstream templates.
func newToolsetData(
	agent *AgentData,
	ref *ir.ToolsetRef,
	servicesData *service.ServicesData,
) (*ToolsetData, error) {
	expr := ref.Expr
	toolsetSlug := ref.Slug
	serviceName := ref.ServiceName
	sourceServiceName := ref.SourceServiceName
	var sourceService *service.Data
	if ref.SourceService != nil {
		sourceService = ref.SourceService.Goa
	} else if servicesData != nil && sourceServiceName != "" {
		sourceService = servicesData.Get(sourceServiceName)
	}
	var imports map[string]*codegen.ImportSpec
	if sourceService != nil {
		imports = buildServiceImportMap(sourceService)
	}
	ts := &ToolsetData{
		Expr:                 expr,
		Name:                 ref.Name,
		Title:                naming.HumanizeTitle(ref.Name),
		Description:          ref.Description,
		Tags:                 slices.Clone(ref.Tags),
		ServiceName:          serviceName,
		SourceServiceName:    sourceServiceName,
		SourceService:        sourceService,
		QualifiedName:        ref.QualifiedName,
		TaskQueue:            ref.TaskQueue,
		Kind:                 toolsetKindFromIR(ref.Kind),
		Agent:                agent,
		PathName:             toolsetSlug,
		PackageName:          ref.PackageName,
		SourceServiceImports: imports,
		PackageImportPath:    ref.PackageImportPath,
		Dir:                  ref.Dir,
		SpecsPackageName:     ref.SpecsPackageName,
		SpecsImportPath:      ref.SpecsImportPath,
		SpecsDir:             ref.SpecsDir,
		AgentToolsPackage:    ref.AgentToolsPackage,
		AgentToolsDir:        ref.AgentToolsDir,
		AgentToolsImportPath: ref.AgentToolsImportPath,
	}
	isMCPBacked := ref.Provider != nil && ref.Provider.Kind == agentsExpr.ProviderMCP

	// Handle toolset based on provider type.
	switch {
	case ref.Provider != nil && ref.Provider.Kind == agentsExpr.ProviderRegistry:
		ts.IsRegistryBacked = true
		if ref.Provider.Registry != nil {
			registry := ref.Provider.Registry
			ts.Registry = &RegistryToolsetMeta{
				RegistryName:             registry.RegistryName,
				ToolsetName:              registry.ToolsetName,
				Version:                  registry.Version,
				QualifiedName:            registry.QualifiedName,
				RegistryClientImportPath: registry.RegistryClientImportPath,
				RegistryClientAlias:      registry.RegistryClientAlias,
			}
		}
		// Registry toolsets have no compile-time tools; they are discovered at runtime.
		// The Tools slice remains empty; specs generation will create placeholder
		// structures that are populated via runtime discovery.

	case isMCPBacked:
		if ref.Provider == nil || ref.Provider.MCP == nil {
			return nil, fmt.Errorf("toolset %q is MCP-backed but missing MCP metadata", expr.Name)
		}
		mcpMeta := ref.Provider.MCP
		ts.MCP = &MCPToolsetMeta{
			ServiceName:   mcpMeta.ServiceName,
			SuiteName:     mcpMeta.SuiteName,
			Source:        mcpMeta.Source,
			QualifiedName: mcpMeta.QualifiedName,
			ConstName:     mcpMeta.ConstName,
		}
		switch ts.MCP.Source {
		case agentsExpr.MCPSourceGoa:
			if !populateMCPToolset(ts) {
				return nil, fmt.Errorf(
					"toolset %q could not resolve Goa-defined MCP toolset %q on service %q",
					expr.Name,
					expr.Provider.MCPToolset,
					expr.Provider.MCPService,
				)
			}
		case agentsExpr.MCPSourceInline:
			for _, toolExpr := range expr.Tools {
				tool, err := newToolData(ts, toolExpr, servicesData)
				if err != nil {
					return nil, err
				}
				ts.Tools = append(ts.Tools, tool)
			}
			slices.SortFunc(ts.Tools, func(a, b *ToolData) int {
				return strings.Compare(a.Name, b.Name)
			})
		default:
			return nil, fmt.Errorf("toolset %q has unknown MCP schema source %d", expr.Name, ts.MCP.Source)
		}

	default:
		for _, toolExpr := range expr.Tools {
			tool, err := newToolData(ts, toolExpr, servicesData)
			if err != nil {
				return nil, err
			}
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

	return ts, nil
}

func toolsetKindFromIR(kind ir.ToolsetRefKind) ToolsetKind {
	switch kind {
	case ir.ToolsetRefKindUsed:
		return ToolsetKindUsed
	case ir.ToolsetRefKindExported:
		return ToolsetKindExported
	default:
		panic(fmt.Sprintf("unknown toolset ref kind %q", kind))
	}
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
func newToolData(ts *ToolsetData, expr *agentsExpr.ToolExpr, servicesData *service.ServicesData) (*ToolData, error) {
	// ts is guaranteed non-nil by construction (collectToolsets/newToolsetData)
	// and ts.QualifiedName is always set there.
	qualified := fmt.Sprintf("%s.%s", ts.Name, expr.Name)

	// Check if this tool is exported by an agent (agent-as-tool pattern)
	isExported := ts.Kind == ToolsetKindExported && ts.Agent != nil
	var exportingAgentID string
	if isExported {
		exportingAgentID = ts.Agent.ID
	}
	title := expr.Name
	if expr.Title != "" {
		title = expr.Title
	}

	tool := &ToolData{
		Name:               expr.Name,
		ConstName:          codegen.Goify(expr.Name, true),
		Description:        expr.Description,
		QualifiedName:      qualified,
		Title:              naming.HumanizeTitle(title),
		Tags:               mergedToolTags(ts, expr),
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
	tool.ServerData = serverDataData(expr.ServerData)
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
		return tool, nil
	}
	tool.IsMethodBacked = true
	// Populate exact payload/result type names using Goa service metadata.
	if servicesData == nil || ts.SourceService == nil {
		return nil, fmt.Errorf("method-backed tool %q requires source service metadata", tool.QualifiedName)
	}
	sd := servicesData.Get(ts.SourceService.Name)
	if sd == nil {
		return nil, fmt.Errorf(
			"method-backed tool %q could not resolve source service %q",
			tool.QualifiedName,
			ts.SourceService.Name,
		)
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
		return nil, fmt.Errorf(
			"method-backed tool %q could not resolve bound method %q on service %q",
			tool.QualifiedName,
			expr.Method.Name,
			ts.SourceService.Name,
		)
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
	return tool, nil
}

// mergedToolTags returns the stable union of toolset-level and tool-level tags.
//
// Contract:
//   - Toolset tags apply to every tool declared in that toolset.
//   - Tool-level tags may add extra metadata without needing to repeat shared
//     capability tags on every tool.
//   - Output order is deterministic: toolset tags first, then tool-only tags.
func mergedToolTags(ts *ToolsetData, expr *agentsExpr.ToolExpr) []string {
	if len(ts.Tags) == 0 && len(expr.Tags) == 0 {
		return nil
	}
	tags := make([]string, 0, len(ts.Tags)+len(expr.Tags))
	seen := make(map[string]struct{}, len(ts.Tags)+len(expr.Tags))
	for _, tag := range ts.Tags {
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	for _, tag := range expr.Tags {
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	return tags
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
func serverDataData(exprs []*agentsExpr.ServerDataExpr) []*ServerDataData {
	if len(exprs) == 0 {
		return nil
	}
	out := make([]*ServerDataData, 0, len(exprs))
	for _, sd := range exprs {
		item := &ServerDataData{
			Kind:     sd.Kind,
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
