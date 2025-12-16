package codegen

import (
	"fmt"
	"sort"
	"strings"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	goaexpr "goa.design/goa/v3/expr"
)

// buildToolSpecsData builds tool specs metadata for the given agent.
func buildToolSpecsData(agent *AgentData) (*toolSpecsData, error) {
	return buildToolSpecsDataFor(agent.Genpkg, agent.Service, agent.Tools)
}

// buildToolSpecsDataFor builds specs/types/codecs data for the provided tool
// slice using the owning service as the context for type/import resolution.
func buildToolSpecsDataFor(genpkg string, svc *service.Data, tools []*ToolData) (*toolSpecsData, error) {
	data := newToolSpecsData(genpkg, svc)
	builder := newToolSpecBuilder(genpkg, svc)
	for _, tool := range tools {
		scope := builder.scopeForTool()
		goName := codegen.Goify(tool.Name, true)
		// Reserve the tool ID constant name *before* materializing any type
		// definitions so nested helper types (HashedUnique) can avoid colliding
		// with it (e.g., a nested user type named "Answer").
		constName := scope.Unique(goName)

		payload, err := builder.typeFor(tool, tool.Args, usagePayload)
		if err != nil {
			return nil, err
		}
		result, err := builder.typeFor(tool, tool.Return, usageResult)
		if err != nil {
			return nil, err
		}
		var sidecar *typeData
		if tool.Artifact != nil && tool.Artifact.Type != goaexpr.Empty {
			sidecar, err = builder.typeFor(tool, tool.Artifact, usageSidecar)
			if err != nil {
				return nil, err
			}
		}
		entry := &toolEntry{
			// Name is the qualified tool ID used at runtime (toolset.tool).
			Name:              tool.QualifiedName,
			GoName:            goName,
			ConstName:         constName,
			Title:             tool.Title,
			Service:           serviceName(tool),
			Toolset:           toolsetName(tool),
			Description:       tool.Description,
			Tags:              tool.Tags,
			IsExportedByAgent: tool.IsExportedByAgent,
			ExportingAgentID:  tool.ExportingAgentID,
			Payload:           payload,
			Result:            result,
			Sidecar:           sidecar,
			BoundedResult:     tool.BoundedResult,
			ResultReminder:    tool.ResultReminder,
			Confirmation:      tool.Confirmation,
		}
		data.addTool(entry)
	}
	data.Scope = builder.helperScope
	data.Unions = builder.unionTypes()
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

// addTool adds the tool entry and its types to the specs data.
func (d *toolSpecsData) addTool(entry *toolEntry) {
	d.tools = append(d.tools, entry)
	d.addType(entry.Payload)
	d.addType(entry.Result)
	if entry.Sidecar != nil {
		d.addType(entry.Sidecar)
	}
}

// addType adds type metadata to the specs data, de-duplicating by cache key.
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

// typesList returns the types in deterministic generation order.
func (d *toolSpecsData) typesList() []*typeData {
	return d.order
}

// pureTypes returns the subset of types that need a Go type definition emitted.
func (d *toolSpecsData) pureTypes() []*typeData {
	var out []*typeData
	for _, info := range d.order {
		if info.NeedType {
			out = append(out, info)
		}
	}
	return out
}

// needsGoaImport reports whether any generated type requires goa runtime helpers
// (validation helpers).
func (d *toolSpecsData) needsGoaImport() bool {
	for _, info := range d.order {
		if info.Validation != "" || info.JSONValidation != "" {
			return true
		}
	}
	return false
}

// validationCodeWithContext wraps goa ValidationCode so that any panic carries
// enough context (tool name, usage, and local context) to pinpoint generator
// bugs. It does not attempt to recover; violations are treated as hard errors.
func validationCodeWithContext(
	att *goaexpr.AttributeExpr,
	put goaexpr.UserType,
	attCtx *codegen.AttributeContext,
	req, alias, view bool,
	target string,
	tool *ToolData,
	usage typeUsage,
	ctx string,
) string {
	defer func() {
		if r := recover(); r != nil {
			panic(fmt.Sprintf(
				"agent/specs_builder: ValidationCode panic for tool %q (usage=%s, ctx=%s, target=%s): %v",
				tool.QualifiedName,
				usage,
				ctx,
				target,
				r,
			))
		}
	}()
	return codegen.ValidationCode(att, put, attCtx, req, alias, view, target)
}

// assertNoNilTypes walks the given attribute and panics when it encounters a
// nil AttributeExpr or a nil Type. It also follows user types so that synthetic
// helpers respect the same invariants as Goa-evaluated DSL:
//
//  1. Every AttributeExpr has a non-nil Type.
//  2. Every user type has a non-nil AttributeExpr with a non-nil Type.
//
// Violations are treated as generator bugs and must be fixed at the
// construction site rather than papered over with defensive checks.
func assertNoNilTypes(att *goaexpr.AttributeExpr, tool *ToolData, usage typeUsage, ctx string) {
	if att == nil {
		panic(fmt.Sprintf(
			"agent/specs_builder: nil AttributeExpr for tool %q (usage=%s, ctx=%s)",
			tool.QualifiedName,
			usage,
			ctx,
		))
	}
	seen := make(map[*goaexpr.AttributeExpr]struct{})
	var walk func(prefix string, a *goaexpr.AttributeExpr)
	walk = func(prefix string, a *goaexpr.AttributeExpr) {
		if a == nil {
			panic(fmt.Sprintf(
				"agent/specs_builder: nil AttributeExpr at %q for tool %q (usage=%s, ctx=%s)",
				prefix,
				tool.QualifiedName,
				usage,
				ctx,
			))
		}
		if _, ok := seen[a]; ok {
			return
		}
		seen[a] = struct{}{}
		if a.Type == nil {
			panic(fmt.Sprintf(
				"agent/specs_builder: nil Type at %q for tool %q (usage=%s, ctx=%s)",
				prefix,
				tool.QualifiedName,
				usage,
				ctx,
			))
		}
		switch dt := a.Type.(type) {
		case goaexpr.UserType:
			uat := dt.Attribute()
			if uat == nil || uat.Type == nil {
				panic(fmt.Sprintf(
					"agent/specs_builder: user type %T with nil attribute/type at %q for tool %q (usage=%s, ctx=%s)",
					dt,
					prefix,
					tool.QualifiedName,
					usage,
					ctx,
				))
			}
			walk(prefix, uat)
		case *goaexpr.Object:
			for _, nat := range *dt {
				if nat == nil {
					panic(fmt.Sprintf(
						"agent/specs_builder: nil NamedAttributeExpr in object at %q for tool %q (usage=%s, ctx=%s)",
						prefix,
						tool.QualifiedName,
						usage,
						ctx,
					))
				}
				name := nat.Name
				path := name
				if prefix != "" {
					path = prefix + "." + name
				}
				walk(path, nat.Attribute)
			}
		case *goaexpr.Array:
			walk(prefix+"[]", dt.ElemType)
		case *goaexpr.Map:
			walk(prefix+"{}", dt.ElemType)
		case *goaexpr.Union:
			for n, v := range dt.Values {
				walk(fmt.Sprintf("%s#%d", prefix, n), v.Attribute)
			}
		}
	}
	walk("", att)
}

// needsUnicodeImport reports whether generated validations reference unicode/utf8.
func (d *toolSpecsData) needsUnicodeImport() bool {
	for _, info := range d.order {
		if (info.Validation != "" && strings.Contains(info.Validation, "utf8.")) ||
			(info.JSONValidation != "" && strings.Contains(info.JSONValidation, "utf8.")) {
			return true
		}
	}
	return false
}

// typeImports returns the imports required by the generated tool types file.
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

// codecsImports returns the imports required by the generated tool codecs file.
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
	// Sidecar helpers depend on planner.ToolResult when any tool declares
	// a sidecar type.
	for _, t := range d.tools {
		if t != nil && t.Sidecar != nil {
			base = append(base, codegen.SimpleImport("goa.design/goa-ai/runtime/agent/planner"))
			break
		}
	}
	if needsGoa {
		base = append(base, codegen.GoaImport(""))
	}
	// Keep strings import last to match golden expectations.
	base = append(base, codegen.SimpleImport("strings"))
	return base
}
