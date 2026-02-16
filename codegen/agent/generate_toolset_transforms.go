package codegen

import (
	"fmt"
	"path"
	"path/filepath"
	"slices"

	"goa.design/goa-ai/codegen/naming"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

// toolsetAdapterTransformsFile emits a `transforms.go` file in the toolset-owned
// specs package.
//
// The emitted helpers adapt between:
//   - tool payload/result types (local aliases in the specs package), and
//   - the bound Goa method payload/result types (qualified via the owning service).
//
// This file is emitted only for method-backed tools whose shapes are compatible
// for transformation via Goa's `codegen.GoTransform`. When no transforms are
// needed/possible, the function returns nil.
func toolsetAdapterTransformsFile(genpkg string, ts *ToolsetData) *codegen.File {
	if ts == nil || ts.SpecsDir == "" || len(ts.Tools) == 0 {
		return nil
	}
	svc := ts.SourceService
	if svc == nil {
		return nil
	}

	// Build data from specs to find tool-local payload/result type names.
	specs, err := buildToolSpecsDataFor(genpkg, svc, ts.Tools)
	if err != nil || specs == nil {
		return nil
	}

	scope := specs.Scope
	if scope == nil {
		panic(fmt.Sprintf("agent codegen: nil specs NameScope for toolset %q", ts.QualifiedName))
	}

	svcAlias := servicePkgAlias(svc)
	svcImport := joinImportPath(genpkg, svc.PathName)

	var fns []transformFuncData
	extraImports := make(map[string]*codegen.ImportSpec)
	fileHelpers := make([]*codegen.TransformFunctionData, 0)
	helperKeys := make(map[string]struct{})

	for _, t := range ts.Tools {
		if t == nil || !t.IsMethodBacked || t.MethodPayloadAttr == nil || t.MethodResultAttr == nil {
			continue
		}

		// Locate tool payload/result/optional server-data type metadata by type name convention.
		var toolPayload, toolResult, toolServerData *typeData
		wantPayload := codegen.Goify(t.Name, true) + "Payload"
		wantResult := codegen.Goify(t.Name, true) + "Result"
		wantServerData := codegen.Goify(t.Name, true) + "ServerData"
		for _, td := range specs.typesList() {
			if td.TypeName == wantPayload {
				toolPayload = td
			}
			if td.TypeName == wantResult {
				toolResult = td
			}
			if td.TypeName == wantServerData {
				toolServerData = td
			}
		}

		// Init<GoName>MethodPayload: tool payload (specs, public type) -> service method payload
		if toolPayload != nil && toolPayload.PublicType != nil && t.Args != nil && t.Args.Type != expr.Empty && t.MethodPayloadAttr != nil && t.MethodPayloadAttr.Type != expr.Empty {
			if err := codegen.IsCompatible(t.Args.Type, t.MethodPayloadAttr.Type, "in", "out"); err == nil {
				for _, im := range gatherAttributeImports(genpkg, t.MethodPayloadAttr) {
					if im != nil && im.Path != "" {
						extraImports[im.Path] = im
					}
				}
				for _, im := range gatherAttributeImports(genpkg, toolPayload.PublicType) {
					if im != nil && im.Path != "" {
						extraImports[im.Path] = im
					}
				}

				localArgAttr := toolPayload.PublicType
				// Both the tool payload and service payload are service-level shapes.
				srcCtx := codegen.NewAttributeContextForConversion(false, false, true, "", scope)
				tgtCtx := codegen.NewAttributeContextForConversion(false, false, true, svcAlias, scope)
				body, helpers, err := codegen.GoTransform(localArgAttr, t.MethodPayloadAttr, "in", "out", srcCtx, tgtCtx, "", false)
				if err == nil && body != "" {
					for _, h := range helpers {
						if h == nil {
							continue
						}
						key := h.Name + "|" + h.ParamTypeRef + "|" + h.ResultTypeRef
						if _, ok := helperKeys[key]; ok {
							continue
						}
						helperKeys[key] = struct{}{}
						fileHelpers = append(fileHelpers, h)
					}
					paramRef := scope.GoFullTypeRef(localArgAttr, "")
					serviceRef := t.MethodPayloadTypeRef
					if serviceRef == "" {
						panic(fmt.Sprintf("agent codegen: missing MethodPayloadTypeRef for method-backed tool %q", t.QualifiedName))
					}
					fns = append(fns, transformFuncData{
						Name:          "Init" + codegen.Goify(t.Name, true) + "MethodPayload",
						ParamTypeRef:  paramRef,
						ResultTypeRef: serviceRef,
						Body:          body,
						Helpers:       nil,
					})
				}
			}
		}

		// Init<GoName>ToolResult: service method result -> tool result (specs, public type)
		if toolResult != nil && toolResult.PublicType != nil && t.Return != nil && t.Return.Type != expr.Empty && t.MethodResultAttr != nil && t.MethodResultAttr.Type != expr.Empty {
			// Use the TOOL Return shape as the base target shape so server-only fields
			// present only on the service result are not exposed in the tool result.
			var baseAttr *expr.AttributeExpr
			if ut, ok := t.Return.Type.(expr.UserType); ok && ut != nil {
				baseAttr = ut.Attribute()
			} else {
				baseAttr = t.Return
			}
			if err := codegen.IsCompatible(t.MethodResultAttr.Type, baseAttr.Type, "in", "out"); err == nil {
				for _, im := range gatherAttributeImports(genpkg, t.MethodResultAttr) {
					if im != nil && im.Path != "" {
						extraImports[im.Path] = im
					}
				}
				for _, im := range gatherAttributeImports(genpkg, t.Return) {
					if im != nil && im.Path != "" {
						extraImports[im.Path] = im
					}
				}

				srcCtx := codegen.NewAttributeContextForConversion(false, false, true, svcAlias, scope)
				targetAttr := toolResult.PublicType
				tgtCtx := codegen.NewAttributeContextForConversion(false, false, true, "", scope)
				body, helpers, err := codegen.GoTransform(t.MethodResultAttr, targetAttr, "in", "out", srcCtx, tgtCtx, "", false)
				if err == nil && body != "" {
					for _, h := range helpers {
						if h == nil {
							continue
						}
						key := h.Name + "|" + h.ParamTypeRef + "|" + h.ResultTypeRef
						if _, ok := helperKeys[key]; ok {
							continue
						}
						helperKeys[key] = struct{}{}
						fileHelpers = append(fileHelpers, h)
					}
					resRef := scope.GoFullTypeRef(targetAttr, "")

					serviceResRef := t.MethodResultTypeRef
					if serviceResRef == "" {
						panic(fmt.Sprintf("agent codegen: missing MethodResultTypeRef for method-backed tool %q", t.QualifiedName))
					}
					fns = append(fns, transformFuncData{
						Name:          "Init" + codegen.Goify(t.Name, true) + "ToolResult",
						ParamTypeRef:  serviceResRef,
						ResultTypeRef: resRef,
						Body:          body,
						Helpers:       nil,
					})
				}
			}
		}

		// Init<GoName>ServerDataFromMethodResult: service method result -> optional server-data (specs, public type)
		if toolServerData != nil && toolServerData.PublicType != nil && t.OptionalServerData != nil && t.OptionalServerData.Schema != nil && t.OptionalServerData.Schema.Type != expr.Empty && t.MethodResultAttr != nil && t.MethodResultAttr.Type != expr.Empty {
			if err := codegen.IsCompatible(t.MethodResultAttr.Type, t.OptionalServerData.Schema.Type, "in", "out"); err == nil {
				for _, im := range gatherAttributeImports(genpkg, t.MethodResultAttr) {
					if im != nil && im.Path != "" {
						extraImports[im.Path] = im
					}
				}
				for _, im := range gatherAttributeImports(genpkg, toolServerData.PublicType) {
					if im != nil && im.Path != "" {
						extraImports[im.Path] = im
					}
				}

				srcCtx := codegen.NewAttributeContextForConversion(false, false, true, svcAlias, scope)
				metaAttr := toolServerData.PublicType
				tgtCtx := codegen.NewAttributeContextForConversion(false, false, true, "", scope)
				body, helpers, err := codegen.GoTransform(t.MethodResultAttr, metaAttr, "in", "out", srcCtx, tgtCtx, "", false)
				if err == nil && body != "" {
					for _, h := range helpers {
						if h == nil {
							continue
						}
						key := h.Name + "|" + h.ParamTypeRef + "|" + h.ResultTypeRef
						if _, ok := helperKeys[key]; ok {
							continue
						}
						helperKeys[key] = struct{}{}
						fileHelpers = append(fileHelpers, h)
					}
					metaRef := scope.GoFullTypeRef(metaAttr, "")
					serviceResRef := t.MethodResultTypeRef
					fns = append(fns, transformFuncData{
						Name:          "Init" + codegen.Goify(t.Name, true) + "ServerDataFromMethodResult",
						ParamTypeRef:  serviceResRef,
						ResultTypeRef: metaRef,
						Body:          body,
						Helpers:       nil,
					})
				}
			}
		}
	}

	if len(fns) == 0 {
		return nil
	}

	// Assemble imports: service and any additional referenced packages.
	imports := []*codegen.ImportSpec{
		{Name: svcAlias, Path: svcImport},
	}
	usedAliases := map[string]struct{}{
		svcAlias: {},
	}
	paths := make([]string, 0, len(extraImports))
	for p := range extraImports {
		if p == "" || p == svcImport {
			continue
		}
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		im := extraImports[p]
		if im == nil || im.Path == "" {
			continue
		}
		im2 := *im
		if im2.Name != "" {
			// Preserve explicit aliases (typically derived from Goa locators) so
			// type references like "types.Foo" remain correct. If a collision occurs,
			// it's a generator bug: fail loudly.
			if _, ok := usedAliases[im2.Name]; ok {
				panic(fmt.Sprintf("agent codegen: import alias collision for %q (path %q)", im2.Name, im2.Path))
			}
			usedAliases[im2.Name] = struct{}{}
		} else {
			alias := naming.SanitizeToken(path.Base(im2.Path), "pkg")
			im2.Name = uniqueImportAlias(usedAliases, alias)
		}
		imports = append(imports, &im2)
	}

	sections := []*codegen.SectionTemplate{
		codegen.Header(ts.Name+" adapter transforms", ts.SpecsPackageName, imports),
		{
			Name:   "tool-transforms",
			Source: agentsTemplates.Read(toolTransformsFileT),
			Data: transformsFileData{
				Functions: fns,
				Helpers:   fileHelpers,
			},
		},
	}
	return &codegen.File{
		Path:             filepath.Join(ts.SpecsDir, "transforms.go"),
		SectionTemplates: sections,
	}
}

func uniqueImportAlias(used map[string]struct{}, base string) string {
	if base == "" {
		base = "pkg"
	}
	alias := base
	for i := 2; ; i++ {
		if _, ok := used[alias]; !ok {
			used[alias] = struct{}{}
			return alias
		}
		alias = fmt.Sprintf("%s%d", base, i)
	}
}
