// Package codegen builds the information needed to write each agent toolset. It reads
// the evaluated tool definitions, finds their Goa services, and sorts the tools
// before templates write the generated files.
package codegen

import (
	"fmt"
	"slices"
	"strings"

	ir "goa.design/goa-ai/codegen/ir"
	"goa.design/goa-ai/codegen/naming"
	agentsExpr "goa.design/goa-ai/expr/agent"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	goaexpr "goa.design/goa/v3/expr"
)

// collectToolsets builds and sorts one group of toolsets used or exported by an
// agent.
func collectToolsets(
	agent *AgentData,
	refs []*ir.ToolsetRef,
	servicesData *service.ServicesData,
	mcpRoot *mcpexpr.RootExpr,
) ([]*ToolsetData, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	toolsets := make([]*ToolsetData, 0, len(refs))
	for _, ref := range refs {
		ts, err := newToolsetData(agent, ref, servicesData, mcpRoot)
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

// newToolsetData reads one toolset definition and returns the values used to
// write its generated files.
func newToolsetData(
	agent *AgentData,
	ref *ir.ToolsetRef,
	servicesData *service.ServicesData,
	mcpRoot *mcpexpr.RootExpr,
) (*ToolsetData, error) {
	return buildToolsetData(agent, ref, ref.Expr, ref.Definition.Expr, servicesData, mcpRoot)
}

// buildToolsetData combines the metadata shown for one reference with the
// reusable tool contract declared by its definition. The reference supplies
// the service and provider needed to generate code.
func buildToolsetData(
	agent *AgentData,
	ref *ir.ToolsetRef,
	expr, contract *agentsExpr.ToolsetExpr,
	servicesData *service.ServicesData,
	mcpRoot *mcpexpr.RootExpr,
) (*ToolsetData, error) {
	toolsetSlug := ref.Slug
	serviceName := ref.ServiceName
	sourceServiceName := ref.SourceServiceName
	var sourceService *service.Data
	if ref.SourceService != nil {
		sourceService = servicesData.Get(ref.SourceService.Name)
	}
	agentToolsRef := ref
	if ref.SourceExport != nil {
		agentToolsRef = ref.SourceExport
	}
	agentToolAgentID := ""
	if agentToolsRef.Agent != nil && agentToolsRef.AgentToolsImportPath != "" {
		agentToolAgentID = agentToolsRef.Agent.ID
	}
	ts := &ToolsetData{
		definition:           ref.Definition,
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
		PackageImportPath:    ref.PackageImportPath,
		Dir:                  ref.Dir,
		SpecsPackageName:     ref.SpecsPackageName,
		SpecsImportPath:      ref.SpecsImportPath,
		SpecsDir:             ref.SpecsDir,
		AgentToolsPackage:    agentToolsRef.AgentToolsPackage,
		AgentToolsDir:        agentToolsRef.AgentToolsDir,
		AgentToolsImportPath: agentToolsRef.AgentToolsImportPath,
		AgentToolAgentID:     agentToolAgentID,
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
		if ref.Provider.MCP == nil {
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
			if !populateMCPToolset(mcpRoot, ts) {
				return nil, fmt.Errorf(
					"toolset %q could not resolve Goa-defined MCP toolset %q on service %q",
					expr.Name,
					expr.Provider.MCPToolset,
					expr.Provider.MCPService,
				)
			}
		case agentsExpr.MCPSourceInline:
			for _, toolExpr := range contract.Tools {
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
		for _, toolExpr := range contract.Tools {
			tool, err := newToolData(ts, toolExpr, servicesData)
			if err != nil {
				return nil, err
			}
			ts.Tools = append(ts.Tools, tool)
		}
		slices.SortFunc(ts.Tools, func(a, b *ToolData) int {
			return strings.Compare(a.Name, b.Name)
		})
		// A tool bound to a service method needs functions that copy its values.
		for _, t := range ts.Tools {
			if t.IsMethodBacked {
				ts.NeedsAdapter = true
				break
			}
		}
		ts.RequiredLabels = requiredLabels(ts.Tools)
	}

	return ts, nil
}

// newDefinitionToolsetData builds one defining toolset's route-free specs using
// the saved service and provider context.
func newDefinitionToolsetData(
	definition *ir.Toolset,
	servicesData *service.ServicesData,
	mcpRoot *mcpexpr.RootExpr,
) (*ToolsetData, error) {
	reference := definition.Owner.Ref
	if reference == nil {
		return nil, fmt.Errorf("toolset %q has no complete owner reference", definition.Name)
	}
	var agent *AgentData
	if reference.Agent != nil {
		agent = &AgentData{
			Name:    reference.Agent.Name,
			ID:      reference.Agent.ID,
			Service: servicesData.Get(reference.Agent.Service.Name),
		}
	}
	return buildToolsetData(agent, reference, definition.Expr, definition.Expr, servicesData, mcpRoot)
}

func toolsetKindFromIR(kind ir.ToolsetRefKind) ToolsetKind {
	switch kind {
	case ir.ToolsetRefKindUsed:
		return ToolsetKindUsed
	case ir.ToolsetRefKindExported:
		return ToolsetKindExported
	case ir.ToolsetRefKindServiceExport:
		return ToolsetKindServiceExport
	default:
		panic(fmt.Sprintf("unknown toolset ref kind %q", kind))
	}
}

// newToolData reads one tool definition and returns the values used to write
// its generated files.
func newToolData(ts *ToolsetData, expr *agentsExpr.ToolExpr, servicesData *service.ServicesData) (*ToolData, error) {
	qualified := fmt.Sprintf("%s.%s", ts.Name, expr.Name)

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
		CallHintTemplate:   expr.CallHintTemplate,
		ResultHintTemplate: expr.ResultHintTemplate,
		InjectedFields:     expr.InjectedFields,
		Bounds:             boundsData(expr.Bounds, expr.Method),
		TerminalRun:        expr.TerminalRun,
		Bookkeeping:        expr.Bookkeeping,
		ResultReminder:     expr.ResultReminder,
	}
	tool.HasResult = tool.Return != nil && tool.Return.Type != goaexpr.Empty
	tool.ModelHiddenPayloadFields = modelHiddenPayloadFields(expr)
	// Resolve each injected field from the complete public input. The private
	// JSON input hides these fields separately.
	tool.Injected = buildInjectedFields(tool.Args, tool.InjectedFields)
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
	tool.method = expr.Method
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

		me := expr.Method
		if me != nil && me.Payload.Type != goaexpr.Empty {
			tool.MethodPayloadAttr = me.Payload
			tool.HasMethodPayload = true
		}
		if me != nil && me.Result.Type != goaexpr.Empty {
			tool.MethodResultAttr = me.Result
			tool.HasMethodResult = true
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
	// A bound method may return a value even when the tool does not declare one.
	tool.HasResult = tool.HasResult || (tool.MethodResultAttr != nil && tool.MethodResultAttr.Type != goaexpr.Empty)
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
func pagingData(tool *agentsExpr.ToolExpr, p *agentsExpr.ToolPagingExpr) *ToolPagingData {
	if p == nil {
		return nil
	}
	data := &ToolPagingData{
		ContinueTool:    continuationToolIdent(tool, continuationToolName(tool, p)),
		CursorField:     modelJSONName(p.CursorField),
		NextCursorField: modelJSONName(p.NextCursorField),
	}
	if source := dedicatedContinuationSource(tool); source != nil {
		data.SourceTool = continuationToolIdent(source, source.Name)
		data.ReplayPayload = !isCursorOnlyContinuation(tool)
	}
	return data
}

// continuationToolName returns the tool selected by a model-visible continue
// action. A dedicated continuation advances itself after its first page.
func continuationToolName(tool *agentsExpr.ToolExpr, paging *agentsExpr.ToolPagingExpr) string {
	if paging.ContinueTool != "" {
		return paging.ContinueTool
	}
	if isDedicatedContinuation(tool) {
		return tool.Name
	}
	return ""
}

// isDedicatedContinuation reports whether another bounded tool delegates its
// continuation action to tool.
func isDedicatedContinuation(tool *agentsExpr.ToolExpr) bool {
	return dedicatedContinuationSource(tool) != nil
}

// dedicatedContinuationSource returns the single sibling query advanced by
// tool. Expression validation rejects ambiguous continuation targets.
func dedicatedContinuationSource(tool *agentsExpr.ToolExpr) *agentsExpr.ToolExpr {
	if tool == nil || tool.Toolset == nil || tool.Bounds == nil || tool.Bounds.Paging == nil {
		return nil
	}
	for _, candidate := range tool.Toolset.Tools {
		if candidate.Bounds != nil && candidate.Bounds.Paging != nil && candidate.Bounds.Paging.ContinueTool == tool.Name {
			return candidate
		}
	}
	return nil
}

// isCursorOnlyContinuation reports whether the continuation cursor carries all
// query state needed by its executor.
func isCursorOnlyContinuation(tool *agentsExpr.ToolExpr) bool {
	obj := effectiveObject(tool.Args)
	return obj != nil && len(*obj) == 1 && (*obj)[0].Name == tool.Bounds.Paging.CursorField
}

// continuationModelHiddenFields returns every root payload field for a
// dedicated continuation action. The model chooses only whether to continue;
// runtime execution supplies both retained query arguments and paging state.
func continuationModelHiddenFields(tool *agentsExpr.ToolExpr) []string {
	obj := effectiveObject(tool.Args)
	fields := make([]string, 0, len(*obj))
	for _, field := range *obj {
		fields = append(fields, field.Name)
	}
	return fields
}

// modelHiddenPayloadFields returns fields filled by generated runtime code and
// therefore omitted from the JSON accepted from the model.
func modelHiddenPayloadFields(tool *agentsExpr.ToolExpr) []string {
	fields := slices.Clone(tool.InjectedFields)
	if isDedicatedContinuation(tool) {
		return append(fields, continuationModelHiddenFields(tool)...)
	}
	if tool.Bounds != nil && tool.Bounds.Paging != nil && tool.Bounds.Paging.ContinueTool != "" {
		return append(fields, tool.Bounds.Paging.CursorField)
	}
	return fields
}

// boundsData projects tool bounds metadata and, when a bound method is known,
// captures the concrete result fields that implement those bounds.
func boundsData(bounds *agentsExpr.ToolBoundsExpr, method *goaexpr.MethodExpr) *ToolBoundsData {
	if bounds == nil {
		return nil
	}
	data := &ToolBoundsData{
		Paging: pagingData(bounds.Tool, bounds.Paging),
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

// continuationToolIdent qualifies a sibling continuation tool in the owning
// toolset so generated runtime metadata uses the same canonical identity as the
// generated tool registry.
func continuationToolIdent(tool *agentsExpr.ToolExpr, name string) string {
	if name == "" {
		return ""
	}
	return tool.Toolset.Name + "." + name
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
		Name:     name,
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
