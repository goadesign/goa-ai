// Package codegen builds the saved tool names, types, schemas, and JSON functions
// that generated files read.
package codegen

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	goaexpr "goa.design/goa/v3/expr"
)

// buildToolSpecsDataForPackage builds one toolset's files with the package
// names recorded before Goa started writing source.
func buildToolSpecsDataForPackage(genpkg string, svc *service.Data, tools []*ToolData, planned *toolSpecsPackagePlan, api *goaexpr.APIExpr) (*toolSpecsData, error) {
	data := newToolSpecsData(genpkg, svc)
	builder := newToolSpecBuilder(svc, planned, api)
	for _, tool := range tools {
		owner := newToolContractTypeOwner(tool)
		goName := codegen.Goify(tool.Name, true)
		// Read every tool name from the package record made before Goa chose the
		// final names.
		names := planned.tools[tool.Name]
		constName := names.constant.Name()
		tool.ConstName = constName
		if names.inject != nil {
			tool.InjectFunc = names.inject.Name()
			tool.DecodeFunc = names.decode.Name()
		}
		if names.methodPayloadTransform != nil {
			tool.MethodPayloadTransform = names.methodPayloadTransform.Name()
		}
		if names.toolResultTransform != nil {
			tool.ToolResultTransform = names.toolResultTransform.Name()
		}
		if len(names.serverDataTransforms) > 0 {
			for _, serverData := range tool.ServerData {
				if declaration := names.serverDataTransforms[serverData.Kind]; declaration != nil {
					serverData.Transform = declaration.Name()
				}
			}
		}

		payload, err := builder.typeFor(owner, tool.Args, usagePayload)
		if err != nil {
			return nil, err
		}
		if payload != nil && len(tool.Injected) > 0 {
			// Custom executors use this function to decode the input and fill fields
			// supplied by the server.
			payload.InjectDecodeFunc = names.decode.Name()
		}
		var result *typeData
		if tool.HasResult {
			result, err = builder.typeFor(owner, tool.Return, usageResult)
			if err != nil {
				return nil, err
			}
		}
		if payload != nil {
			tool.PayloadTypeName = payload.TypeName
			tool.PayloadCodecName = payload.ExportedCodec
		}
		if result != nil {
			tool.ResultTypeName = result.TypeName
			tool.ResultCodecName = result.ExportedCodec
		}
		serverDataEntries, err := serverDataEntriesForTool(tool, builder)
		if err != nil {
			return nil, err
		}
		for _, serverData := range tool.ServerData {
			if plannedType := names.serverDataTypes[serverData.Kind]; plannedType != nil {
				serverData.CodecName = plannedType.exportedCodec.Name()
			}
		}
		metaPairs := toolMetaPairs(tool.Meta)
		entry := &toolEntry{
			// Name is the qualified tool ID used at runtime (toolset.tool).
			Name:           tool.QualifiedName,
			GoName:         goName,
			ConstName:      constName,
			Title:          tool.Title,
			Description:    tool.Description,
			ServerData:     serverDataEntries,
			Tags:           tool.Tags,
			Meta:           tool.Meta,
			MetaPairs:      metaPairs,
			Payload:        payload,
			Result:         result,
			HasResult:      tool.HasResult,
			Bounds:         tool.Bounds,
			TerminalRun:    tool.TerminalRun,
			Bookkeeping:    tool.Bookkeeping,
			ResultReminder: tool.ResultReminder,
			Confirmation:   tool.Confirmation,
		}
		entry.ConstructorFunc = names.constructor.Name()
		entry.SpecVar = names.spec.Name()
		if names.inject != nil {
			entry.InjectFunc = names.inject.Name()
			entry.DecodeFunc = names.decode.Name()
		}
		if names.methodPayloadTransform != nil {
			entry.MethodPayloadTransform = names.methodPayloadTransform.Name()
		}
		if names.toolResultTransform != nil {
			entry.ToolResultTransform = names.toolResultTransform.Name()
		}
		for _, serverData := range entry.ServerData {
			if transform := names.serverDataTransforms[serverData.Kind]; transform != nil {
				serverData.Transform = transform.Name()
			}
		}
		tool.SpecVar = entry.SpecVar
		entry.TypedToolVar = names.typed.Name()
		if names.canonicalizeServerData != nil {
			entry.CanonicalizeServerDataFunc = names.canonicalizeServerData.Name()
			entry.CanonicalizeServerDataItemFunc = names.canonicalizeServerDataItem.Name()
		}
		data.addTool(entry)
	}
	data.Scope = builder.helperScope
	data.Unions = builder.unionTypes()
	data.TransportUnions = builder.transportUnionTypes()
	data.CodecTransformHelpers = builder.codecTransformHelpers
	data.JSONValidators = materializeJSONValidators(planned.jsonValidators)
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

// newToolContractTypeOwner copies the tool name, toolset name, method result,
// bounds, and hidden input fields used to build its generated types.
func newToolContractTypeOwner(tool *ToolData) *contractTypeOwner {
	if tool == nil {
		return nil
	}
	scopeName := ""
	if tool.Toolset != nil {
		scopeName = tool.Toolset.QualifiedName
	}
	return &contractTypeOwner{
		Kind:                     contractTypeOwnerTool,
		Name:                     tool.Name,
		QualifiedName:            tool.QualifiedName,
		ScopeName:                scopeName,
		PreferMethodResult:       tool.IsMethodBacked,
		MethodResultAttr:         tool.MethodResultAttr,
		Bounds:                   tool.Bounds,
		ModelHiddenPayloadFields: tool.ModelHiddenPayloadFields,
	}
}

func toolMetaPairs(meta map[string][]string) []toolMetaPair {
	if len(meta) == 0 {
		return nil
	}
	keys := make([]string, 0, len(meta))
	for k := range meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]toolMetaPair, 0, len(keys))
	for _, k := range keys {
		out = append(out, toolMetaPair{
			Key:    k,
			Values: slices.Clone(meta[k]),
		})
	}
	return out
}

// addTool adds the tool entry and its types to the specs data.
func (d *toolSpecsData) addTool(entry *toolEntry) {
	d.tools = append(d.tools, entry)
	d.addType(entry.Payload)
	d.addType(entry.Result)
	for _, sd := range entry.ServerData {
		if sd == nil {
			continue
		}
		d.addType(sd.Type)
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

// pureTypes returns the types that need a Go definition.
func (d *toolSpecsData) pureTypes() []*typeData {
	var out []*typeData
	for _, info := range d.order {
		if info.NeedType {
			out = append(out, info)
		}
	}
	return out
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
	owner *contractTypeOwner,
	usage typeUsage,
	ctx string,
) string {
	defer func() {
		if r := recover(); r != nil {
			panic(fmt.Sprintf(
				"agent/specs_builder: ValidationCode panic for %s %q (usage=%s, ctx=%s, target=%s): %v",
				owner.Kind,
				owner.QualifiedName,
				usage,
				ctx,
				target,
				r,
			))
		}
	}()
	return codegen.ValidationCode(att, put, attCtx, req, alias, view, target)
}

// assertNoNilTypes walks att and stops generation when an attribute or its type
// is missing. It also checks the contents of named user types.
//
//  1. Every AttributeExpr has a non-nil Type.
//  2. Every user type has a non-nil AttributeExpr with a non-nil Type.
//
// A failure here is a generator bug in the code that built the attribute.
func assertNoNilTypes(att *goaexpr.AttributeExpr, owner *contractTypeOwner, usage typeUsage, ctx string) {
	if att == nil {
		panic(fmt.Sprintf(
			"agent/specs_builder: nil AttributeExpr for %s %q (usage=%s, ctx=%s)",
			owner.Kind,
			owner.QualifiedName,
			usage,
			ctx,
		))
	}
	seen := make(map[*goaexpr.AttributeExpr]struct{})
	var walk func(prefix string, a *goaexpr.AttributeExpr)
	walk = func(prefix string, a *goaexpr.AttributeExpr) {
		if a == nil {
			panic(fmt.Sprintf(
				"agent/specs_builder: nil AttributeExpr at %q for %s %q (usage=%s, ctx=%s)",
				prefix,
				owner.Kind,
				owner.QualifiedName,
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
				"agent/specs_builder: nil Type at %q for %s %q (usage=%s, ctx=%s)",
				prefix,
				owner.Kind,
				owner.QualifiedName,
				usage,
				ctx,
			))
		}
		switch dt := a.Type.(type) {
		case goaexpr.UserType:
			uat := dt.Attribute()
			if uat == nil || uat.Type == nil {
				panic(fmt.Sprintf(
					"agent/specs_builder: user type %T with nil attribute/type at %q for %s %q (usage=%s, ctx=%s)",
					dt,
					prefix,
					owner.Kind,
					owner.QualifiedName,
					usage,
					ctx,
				))
			}
			walk(prefix, uat)
		case *goaexpr.Object:
			for _, nat := range *dt {
				if nat == nil {
					panic(fmt.Sprintf(
						"agent/specs_builder: nil NamedAttributeExpr in object at %q for %s %q (usage=%s, ctx=%s)",
						prefix,
						owner.Kind,
						owner.QualifiedName,
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

func serverDataEntriesForTool(tool *ToolData, builder *toolSpecBuilder) ([]*serverDataEntry, error) {
	if tool == nil || len(tool.ServerData) == 0 {
		return nil, nil
	}
	owner := newToolContractTypeOwner(tool)
	out := make([]*serverDataEntry, 0, len(tool.ServerData))
	for _, sd := range tool.ServerData {
		if sd == nil || strings.TrimSpace(sd.Kind) == "" {
			continue
		}
		if sd.Schema == nil || sd.Schema.Type == nil || sd.Schema.Type == goaexpr.Empty {
			return nil, fmt.Errorf("tool %q: ServerData(%q) missing schema", tool.QualifiedName, sd.Kind)
		}
		td, err := builder.buildTypeInfo(owner, sd.Schema, usageServerData, sd.Kind)
		if err != nil {
			return nil, err
		}
		out = append(out, &serverDataEntry{
			Kind:        sd.Kind,
			Audience:    sd.Audience,
			Description: sd.Description,
			Type:        td,
		})
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
