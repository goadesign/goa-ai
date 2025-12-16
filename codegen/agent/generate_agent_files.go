package codegen

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

// agentFiles collects all files generated for a single agent.
func agentFiles(agent *AgentData) []*codegen.File {
	files := []*codegen.File{
		agentImplFile(agent),
		agentConfigFile(agent),
		agentRegistryFile(agent),
	}
	// Emit per-toolset specs packages + aggregator.
	if tsFiles := agentPerToolsetSpecsFiles(agent); len(tsFiles) > 0 {
		files = append(files, tsFiles...)
		if agg := agentSpecsAggregatorFile(agent); agg != nil {
			files = append(files, agg)
		}
		if jsonFile := agentSpecsJSONFile(agent); jsonFile != nil {
			files = append(files, jsonFile)
		}
	}
	files = append(files, agentToolsFiles(agent)...)
	files = append(files, agentToolsConsumerFiles(agent)...)
	// Emit adapter transforms for method-backed tools (under generated toolset package).
	files = append(files, internalAdapterTransformsFiles(agent)...)
	// Do not emit service toolset registrations; executors map tool payloads to service methods.
	files = append(files, mcpExecutorFiles(agent)...)
	// Emit typed helpers for Used toolsets (method-backed) to align planner UX.
	files = append(files, usedToolsFiles(agent)...)
	// Emit default service executor factories for method-backed Used toolsets.
	files = append(files, serviceExecutorFiles(agent)...)

	var filtered []*codegen.File
	for _, f := range files {
		if f != nil {
			filtered = append(filtered, f)
		}
	}
	return filtered
}

// agentRouteRegisterFile emits Register<Agent>Route(ctx, rt) so caller processes can
// register route-only metadata for cross-process composition.
//
// agentRouteRegisterFile removed: agent-tools embed strong-contract route metadata in
// their generated toolset registrations, so no separate route registry is required.

// agentPerToolsetSpecsFiles emits types/codecs/specs under specs/<toolset>/ using
// short, tool-local type names to avoid collisions between toolsets.
func agentPerToolsetSpecsFiles(agent *AgentData) []*codegen.File {
	var out []*codegen.File
	emitted := make(map[string]struct{})
	for _, ts := range agent.AllToolsets {
		// Handle registry-backed toolsets separately - they have no compile-time tools
		// but need runtime discovery code.
		if ts.IsRegistryBacked && ts.Registry != nil {
			if ts.SpecsDir == "" {
				continue
			}
			if _, dup := emitted[ts.SpecsDir]; dup {
				continue
			}
			emitted[ts.SpecsDir] = struct{}{}
			// Generate registry toolset specs with runtime discovery
			regFiles := registryToolsetSpecsFiles(agent, ts)
			out = append(out, regFiles...)
			continue
		}

		if len(ts.Tools) == 0 || ts.SpecsDir == "" {
			continue
		}
		// Deduplicate per-toolset emission by SpecsDir to avoid generating the
		// same codecs/specs multiple times when the toolset appears in multiple
		// groups (e.g., Uses and an external binding) or through data merges.
		if ts.SpecsDir != "" {
			if _, dup := emitted[ts.SpecsDir]; dup {
				continue
			}
			emitted[ts.SpecsDir] = struct{}{}
		}
		// Use the provider/owning service for specs generation when known;
		// fall back to the agent service otherwise.
		svc := ts.SourceService
		if svc == nil {
			svc = agent.Service
		}
		data, err := buildToolSpecsDataFor(agent.Genpkg, svc, ts.Tools)
		if err != nil || data == nil {
			continue
		}
		// types.go
		if pure := data.pureTypes(); len(pure) > 0 {
			sections := []*codegen.SectionTemplate{
				codegen.Header(agent.StructName+" tool types", ts.SpecsPackageName, data.typeImports()),
				{
					Name:    "tool-spec-types",
					Source:  agentsTemplates.Read(toolTypesFileT),
					Data:    toolTypesFileData{Types: pure},
					FuncMap: templateFuncMap(),
				},
			}
			out = append(out, &codegen.File{Path: filepath.Join(ts.SpecsDir, "types.go"), SectionTemplates: sections})
		}
		// unions.go
		if len(data.Unions) > 0 {
			unionImports := []*codegen.ImportSpec{
				codegen.SimpleImport("encoding/json"),
				codegen.SimpleImport("fmt"),
				codegen.GoaImport(""),
			}
			unionImports = append(unionImports, data.typeImports()...)
			unionSections := []*codegen.SectionTemplate{
				codegen.Header(agent.StructName+" tool union types", ts.SpecsPackageName, unionImports),
				{
					Name:    "tool-spec-union-types",
					Source:  agentsTemplates.Read(toolUnionTypesFileT),
					Data:    toolUnionTypesFileData{Unions: data.Unions},
					FuncMap: templateFuncMap(),
				},
			}
			out = append(out, &codegen.File{Path: filepath.Join(ts.SpecsDir, "unions.go"), SectionTemplates: unionSections})
		}
		if len(data.tools) > 0 {
			// codecs.go
			codecsSections := []*codegen.SectionTemplate{
				codegen.Header(agent.StructName+" tool codecs", ts.SpecsPackageName, data.codecsImports()),
				{
					Name:    "tool-spec-codecs",
					Source:  agentsTemplates.Read(toolCodecsFileT),
					Data:    toolCodecsFileData{Types: data.typesList(), Tools: data.tools},
					FuncMap: templateFuncMap(),
				},
			}
			out = append(out, &codegen.File{Path: filepath.Join(ts.SpecsDir, "codecs.go"), SectionTemplates: codecsSections})
			// specs.go
			specImports := []*codegen.ImportSpec{
				{Path: "sort"},
				{Path: "goa.design/goa-ai/runtime/agent/policy"},
				{Path: "goa.design/goa-ai/runtime/agent/tools"},
			}
			specSections := []*codegen.SectionTemplate{
				codegen.Header(agent.StructName+" tool specs", ts.SpecsPackageName, specImports),
				{Name: "tool-specs", Source: agentsTemplates.Read(toolSpecFileT), Data: toolSpecFileData{PackageName: ts.SpecsPackageName, Tools: data.tools, Types: data.typesList()}, FuncMap: templateFuncMap()},
			}
			out = append(out, &codegen.File{Path: filepath.Join(ts.SpecsDir, "specs.go"), SectionTemplates: specSections})
			// Specs-level transforms are no longer generated; mapping is explicit in adapters.
		}
	}
	return out
}

// registryToolsetSpecsFileData holds template data for registry toolset specs.
type registryToolsetSpecsFileData struct {
	PackageName   string
	QualifiedName string
	ServiceName   string
	Registry      *RegistryToolsetMeta
}

// registryToolsetSpecsFiles generates the specs files for a registry-backed toolset.
// Unlike local toolsets, registry toolsets have no compile-time tool definitions;
// instead, they generate placeholder structures and runtime discovery code that
// populates the specs when the agent starts.
func registryToolsetSpecsFiles(agent *AgentData, ts *ToolsetData) []*codegen.File {
	if ts == nil || ts.Registry == nil || ts.SpecsDir == "" {
		return nil
	}

	var out []*codegen.File

	data := registryToolsetSpecsFileData{
		PackageName:   ts.SpecsPackageName,
		QualifiedName: ts.QualifiedName,
		ServiceName:   ts.ServiceName,
		Registry:      ts.Registry,
	}

	// specs.go - contains runtime discovery and validation code
	specImports := []*codegen.ImportSpec{
		{Path: "context"},
		{Path: "encoding/json"},
		{Path: "fmt"},
		{Path: "regexp"},
		{Path: "sort"},
		{Path: "strings"},
		{Path: "sync"},
		{Path: "goa.design/goa-ai/runtime/agent/policy"},
		{Path: "goa.design/goa-ai/runtime/agent/tools"},
	}
	specSections := []*codegen.SectionTemplate{
		codegen.Header(agent.StructName+" registry toolset specs", ts.SpecsPackageName, specImports),
		{
			Name:    "registry-toolset-specs",
			Source:  agentsTemplates.Read(registryToolsetSpecsFileT),
			Data:    data,
			FuncMap: templateFuncMap(),
		},
	}
	out = append(out, &codegen.File{
		Path:             filepath.Join(ts.SpecsDir, "specs.go"),
		SectionTemplates: specSections,
	})

	return out
}

// agentSpecsJSONFile emits specs/tool_schemas.json for an agent, capturing the
// JSON schemas for all tools declared across its toolsets. The file aggregates
// payload, result, and optional sidecar schemas in a backend-agnostic format
// that can be consumed by frontends or other tooling without depending on
// generated Go types.
//
// The JSON structure is:
//
//	{
//	  "tools": [
//	    {
//	      "id": "toolset.tool",
//	      "service": "svc",
//	      "toolset": "toolset",
//	      "title": "Title",
//	      "description": "Description",
//	      "tags": ["tag"],
//	      "payload": {
//	        "name": "PayloadType",
//	        "schema": { /* JSON Schema */ }
//	      },
//	      "result": {
//	        "name": "ResultType",
//	        "schema": { /* JSON Schema */ }
//	      },
//	      "sidecar": {
//	        "name": "SidecarType",
//	        "schema": { /* JSON Schema */ }
//	      }
//	    }
//	  ]
//	}
//
// Schemas are emitted only when available; tools without payload, result, or
// sidecar schemas still appear with name metadata so callers can rely on a
// stable catalogue.
func agentSpecsJSONFile(agent *AgentData) *codegen.File {
	data, err := buildToolSpecsData(agent)
	if err != nil {
		// Schema generation failures indicate a broken design or codegen bug and
		// must surface loudly so callers do not observe partial or drifting
		// tool catalogues. Fail generation instead of silently omitting schemas.
		panic(fmt.Errorf("goa-ai: tool schema generation failed for agent %q: %w", agent.Name, err))
	}
	if data == nil {
		return nil
	}
	if len(data.tools) == 0 {
		return nil
	}

	type typeSchema struct {
		Name   string          `json:"name"`
		Schema json.RawMessage `json:"schema,omitempty"`
	}

	type confirmationSchema struct {
		Title                string `json:"title,omitempty"`
		PromptTemplate       string `json:"prompt_template"`
		DeniedResultTemplate string `json:"denied_result_template"`
	}

	type toolSchema struct {
		ID           string              `json:"id"`
		Service      string              `json:"service"`
		Toolset      string              `json:"toolset"`
		Title        string              `json:"title,omitempty"`
		Description  string              `json:"description,omitempty"`
		Tags         []string            `json:"tags,omitempty"`
		Confirmation *confirmationSchema `json:"confirmation,omitempty"`
		Payload      *typeSchema         `json:"payload,omitempty"`
		Result       *typeSchema         `json:"result,omitempty"`
		Sidecar      *typeSchema         `json:"sidecar,omitempty"`
	}

	out := struct {
		Tools []toolSchema `json:"tools"`
	}{
		Tools: make([]toolSchema, 0, len(data.tools)),
	}

	for _, t := range data.tools {
		if t == nil {
			continue
		}

		entry := toolSchema{
			ID:          t.Name,
			Service:     t.Service,
			Toolset:     t.Toolset,
			Title:       t.Title,
			Description: t.Description,
		}
		if len(t.Tags) > 0 {
			tags := make([]string, len(t.Tags))
			copy(tags, t.Tags)
			entry.Tags = tags
		}

		if td := t.Payload; td != nil && td.TypeName != "" {
			ts := typeSchema{
				Name: td.TypeName,
			}
			if len(td.SchemaJSON) > 0 {
				ts.Schema = json.RawMessage(td.SchemaJSON)
			}
			entry.Payload = &ts
		}

		if td := t.Result; td != nil && td.TypeName != "" {
			ts := typeSchema{
				Name: td.TypeName,
			}
			if len(td.SchemaJSON) > 0 {
				ts.Schema = json.RawMessage(td.SchemaJSON)
			}
			entry.Result = &ts
		}

		if td := t.Sidecar; td != nil && td.TypeName != "" {
			ts := typeSchema{
				Name: td.TypeName,
			}
			if len(td.SchemaJSON) > 0 {
				ts.Schema = json.RawMessage(td.SchemaJSON)
			}
			entry.Sidecar = &ts
		}

		if c := t.Confirmation; c != nil {
			entry.Confirmation = &confirmationSchema{
				Title:                c.Title,
				PromptTemplate:       c.PromptTemplate,
				DeniedResultTemplate: c.DeniedResultTemplate,
			}
		}

		out.Tools = append(out.Tools, entry)
	}

	if len(out.Tools) == 0 {
		return nil
	}

	payload, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil
	}
	// Ensure a trailing newline for POSIX-friendly files and cleaner diffs.
	payload = append(payload, '\n')

	sections := []*codegen.SectionTemplate{
		{
			Name:   "tool-schemas-json",
			Source: "{{ . }}",
			Data:   string(payload),
		},
	}
	path := filepath.Join(agent.Dir, "specs", "tool_schemas.json")
	return &codegen.File{
		Path:             path,
		SectionTemplates: sections,
	}
}

// internalAdapterTransformsFiles emits best-effort transform helpers for method-backed tools
// under gen/<service>/agents/<agent>/<toolset>/transforms.go. These are adapter-facing
// utilities that initialize service payloads from tool payloads and tool results from
// service results.
func internalAdapterTransformsFiles(agent *AgentData) []*codegen.File {
	out := make([]*codegen.File, 0, len(agent.AllToolsets))
	for _, ts := range agent.AllToolsets {
		if len(ts.Tools) == 0 || ts.SpecsDir == "" {
			continue
		}
		// Build data from specs to find tool-local payload/result type names.
		// Use the provider/owning service when known for type resolution.
		svc := ts.SourceService
		if svc == nil {
			svc = agent.Service
		}
		data, err := buildToolSpecsDataFor(agent.Genpkg, svc, ts.Tools)
		if err != nil || data == nil {
			continue
		}
		// Resolve service import alias/path for method types. Use a NameScope to
		// guarantee alias uniqueness within this file (for example, when the
		// service and toolset packages are both named "todos").
		svcAlias := servicePkgAlias(svc)
		svcImport := joinImportPath(agent.Genpkg, svc.PathName)
		specsAlias := ts.SpecsPackageName
		specsImportPath := ts.SpecsImportPath
		aliasScope := codegen.NewNameScope()
		svcAlias = aliasScope.Unique(svcAlias)
		// Prefer a deterministic "specs" suffix when the base alias collides.
		specsAlias = aliasScope.Unique(specsAlias, "specs")

		// Single NameScope per emitted file to ensure consistent, conflict‑free refs
		scope := codegen.NewNameScope()
		var fns []transformFuncData
		extraImports := make(map[string]*codegen.ImportSpec)
		// File-level helper functions aggregated across all transforms (deduplicated).
		fileHelpers := make([]*codegen.TransformFunctionData, 0)
		helperKeys := make(map[string]struct{})

		for _, t := range ts.Tools {
			if !t.IsMethodBacked || t.MethodPayloadAttr == nil || t.MethodResultAttr == nil {
				continue
			}
			// Locate tool payload/result/sidecar type metadata by type name convention.
			var toolPayload, toolResult, toolSidecar *typeData
			wantPayload := codegen.Goify(t.Name, true) + "Payload"
			wantResult := codegen.Goify(t.Name, true) + "Result"
			wantSidecar := codegen.Goify(t.Name, true) + "Sidecar"
			for _, td := range data.typesList() {
				if td.TypeName == wantPayload {
					toolPayload = td
				}
				if td.TypeName == wantResult {
					toolResult = td
				}
				if td.TypeName == wantSidecar {
					toolSidecar = td
				}
			}
			// Init<GoName>MethodPayload: tool payload (specs) -> service method payload
			if toolPayload != nil && t.Args != nil && t.Args.Type != goaexpr.Empty && t.MethodPayloadAttr != nil && t.MethodPayloadAttr.Type != goaexpr.Empty {
				// Only when shapes are compatible.
				if err := codegen.IsCompatible(t.Args.Type, t.MethodPayloadAttr.Type, "in", "out"); err == nil {
					// imports from source and target attributes
					for _, im := range gatherAttributeImports(agent.Genpkg, t.Args) {
						if im != nil && im.Path != "" {
							extraImports[im.Path] = im
						}
					}
					for _, im := range gatherAttributeImports(agent.Genpkg, t.MethodPayloadAttr) {
						if im != nil && im.Path != "" {
							extraImports[im.Path] = im
						}
					}
					// Do not force pointer semantics; let Goa default rules apply
					srcCtx := codegen.NewAttributeContextForConversion(false, false, false, specsAlias, scope)
					tgtCtx := codegen.NewAttributeContextForConversion(false, false, false, svcAlias, scope)
					body, helpers, err := codegen.GoTransform(t.Args, t.MethodPayloadAttr, "in", "out", srcCtx, tgtCtx, "", false)
					if err == nil && body != "" {
						// Accumulate helpers at file scope (deduplicated).
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
						// Build a local alias type in the specs package for the tool payload and compute full ref.
						var argBase *goaexpr.AttributeExpr
						if ut, ok := t.Args.Type.(goaexpr.UserType); ok && ut != nil {
							argBase = ut.Attribute()
						} else {
							argBase = t.Args
						}
						// Build a local alias user type for the tool payload. Do not propagate struct:pkg:path
						// from the base attribute so initializers/casts use the specs package alias.
						dupArg := *argBase
						if dupArg.Meta != nil {
							delete(dupArg.Meta, "struct:pkg:path")
						}
						localArgAttr := &goaexpr.AttributeExpr{Type: &goaexpr.UserTypeExpr{AttributeExpr: &dupArg, TypeName: toolPayload.TypeName}}
						paramRef := scope.GoFullTypeRef(localArgAttr, specsAlias)
						// Compute fully-qualified service payload ref for the bound method
						// payload using the precomputed MethodPayloadTypeRef derived from
						// Goa's service metadata (sd.Scope.GoFullTypeRef). This avoids any
						// string rewriting of type references and keeps transforms aligned
						// with the actual service method signature.
						serviceRef := t.MethodPayloadTypeRef
						if serviceRef == "" {
							panic(fmt.Sprintf(
								"agent codegen: missing MethodPayloadTypeRef for method-backed tool %q (service %q, method %q)",
								t.QualifiedName,
								svc.Name,
								t.MethodGoName,
							))
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
			// Init<GoName>ToolResult: service method result -> tool result (specs)
			if toolResult != nil && t.Return != nil && t.Return.Type != goaexpr.Empty && t.MethodResultAttr != nil && t.MethodResultAttr.Type != goaexpr.Empty {
				// Use the TOOL Return shape as the base target shape so that server-only
				// fields present only on the service result (e.g., evidence, calls) are
				// not exposed in the tool-visible result type.
				var baseAttr *goaexpr.AttributeExpr
				if ut, ok := t.Return.Type.(goaexpr.UserType); ok && ut != nil {
					baseAttr = ut.Attribute()
				} else {
					baseAttr = t.Return
				}
				// Only when shapes are compatible (method result -> tool return).
				if err := codegen.IsCompatible(t.MethodResultAttr.Type, baseAttr.Type, "in", "out"); err == nil {
					for _, im := range gatherAttributeImports(agent.Genpkg, t.MethodResultAttr) {
						if im != nil && im.Path != "" {
							extraImports[im.Path] = im
						}
					}
					for _, im := range gatherAttributeImports(agent.Genpkg, t.Return) {
						if im != nil && im.Path != "" {
							extraImports[im.Path] = im
						}
					}
					// Do not force pointer semantics; let Goa default rules apply
					srcCtx := codegen.NewAttributeContextForConversion(false, false, false, svcAlias, scope)
					// Build a local alias user type for the tool result. Do not propagate struct:pkg:path
					// from the base attribute so initializers/casts use the specs package alias.
					dupRes := *baseAttr
					if dupRes.Meta != nil {
						delete(dupRes.Meta, "struct:pkg:path")
					}
					// Seed the NameScope with desired names for service-local nested user types
					// so transform helpers use the emitted local alias names
					var seedLocalNames func(a *goaexpr.AttributeExpr)
					seedLocalNames = func(a *goaexpr.AttributeExpr) {
						if a == nil || a.Type == nil || a.Type == goaexpr.Empty {
							return
						}
						switch dt := a.Type.(type) {
						case goaexpr.UserType:
							if loc := codegen.UserTypeLocation(dt); loc == nil {
								// Determine public name from the user type expression.
								switch u := dt.(type) {
								case *goaexpr.UserTypeExpr:
									_ = scope.HashedUnique(dt, codegen.Goify(u.TypeName, true), "")
								case *goaexpr.ResultTypeExpr:
									_ = scope.HashedUnique(dt, codegen.Goify(u.TypeName, true), "")
								}
							}
							seedLocalNames(dt.Attribute())
						case *goaexpr.Array:
							seedLocalNames(dt.ElemType)
						case *goaexpr.Map:
							seedLocalNames(dt.KeyType)
							seedLocalNames(dt.ElemType)
						case *goaexpr.Object:
							for _, nat := range *dt {
								seedLocalNames(nat.Attribute)
							}
						}
					}
					seedLocalNames(&dupRes)
					// Rewrite nested service-local user types to reference local specs aliases.
					rewritten := rewriteNestedLocalUserTypes(&dupRes)
					targetUT := &goaexpr.UserTypeExpr{AttributeExpr: rewritten, TypeName: toolResult.TypeName}
					targetAttr := &goaexpr.AttributeExpr{Type: targetUT}
					// Target (tool result) should be a value type in specs; do not request pointer semantics here.
					tgtCtx := codegen.NewAttributeContextForConversion(false, false, false, specsAlias, scope)
					body, helpers, err := codegen.GoTransform(t.MethodResultAttr, targetAttr, "in", "out", srcCtx, tgtCtx, "", false)
					if err == nil && body != "" {
						// Accumulate helpers at file scope (deduplicated).
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
						// For bounded tools whose result type exposes the simple cardinality
						// fields (returned, total, truncated, refinement_hint), append a small
						// semantic postlude that initializes the canonical Bounds helper
						// field from those individual fields on out. The canonical bounds
						// shape is synthesized in specs_builder.go and is represented in the
						// generated result type as:
						//
						//   Bounds *struct {
						//       Returned       int
						//       Total          *int
						//       Truncated      bool
						//       RefinementHint *string
						//   }
						//
						// More complex bounded results (for example, multi-dimensional views
						// such as GetTimeSeriesResult with ReturnedSeries/ReturnedSamples)
						// cannot be derived generically; those tools must populate Bounds
						// explicitly via their service result shapes instead.
						if t.BoundedResult && toolResult.ImplementsBounds {
							obj := goaexpr.AsObject(baseAttr.Type)
							hasReturned := false
							hasTotal := false
							hasTruncated := false
							hasHint := false
							if obj != nil {
								for _, nat := range *obj {
									switch nat.Name {
									case "returned", "Returned":
										hasReturned = true
									case "total", "Total":
										hasTotal = true
									case "truncated", "Truncated":
										hasTruncated = true
									case "refinement_hint", "RefinementHint":
										hasHint = true
									}
								}
							}
							if hasReturned && hasTotal && hasTruncated && hasHint {
								boundsBody := `
    if out != nil {
        bounds := &struct {
            Returned       int     ` + "`json:\"returned\"`" + `
            Total          *int    ` + "`json:\"total\"`" + `
            Truncated      bool    ` + "`json:\"truncated\"`" + `
            RefinementHint *string ` + "`json:\"refinement_hint\"`" + `
        }{}
        if out.Returned != nil {
            bounds.Returned = *out.Returned
        }
        if out.Total != nil {
            bounds.Total = out.Total
        }
        if out.Truncated != nil {
            bounds.Truncated = *out.Truncated
        }
        if out.RefinementHint != nil {
            bounds.RefinementHint = out.RefinementHint
        }
        out.Bounds = bounds
    }
`
								body += boundsBody
							}
						}
						// Compute correct result type reference (including composites) and
						// qualify with specs alias.
						resRef := scope.GoFullTypeRef(targetAttr, specsAlias)
						// Compute fully-qualified service result ref. Prefer the
						// precomputed MethodResultTypeRef (which already accounts for
						// external locators such as types.*). When the result type is
						// local to the service package (no MethodResultLoc), re‑compute
						// using the service alias so the parameter type matches the
						// import alias even when the toolset and service package names
						// collide (e.g., service "todos" with toolset "todos").
						serviceResRef := t.MethodResultTypeRef
						if (t.MethodResultLoc == nil || t.MethodResultLoc.PackageName() == "") && svcAlias != "" {
							if ref := scope.GoFullTypeRef(t.MethodResultAttr, svcAlias); ref != "" {
								serviceResRef = ref
							}
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
			// Init<GoName>SidecarFromMethodResult: service method result -> sidecar (specs)
			if toolSidecar != nil && t.Artifact != nil && t.Artifact.Type != goaexpr.Empty && t.MethodResultAttr != nil && t.MethodResultAttr.Type != goaexpr.Empty {
				wrapHandled := false
				// Fast-path: if the sidecar user type is a single-field wrapper whose
				// field type matches the method result type, synthesize a direct
				// wrapper transform instead of relying on structural field matches.
				if ut, ok := t.Artifact.Type.(goaexpr.UserType); ok && ut != nil {
					if obj := goaexpr.AsObject(ut.Attribute().Type); obj != nil && len(*obj) == 1 {
						// Check the lone field is assignable from the method result type.
						for _, natt := range *obj {
							fieldName := natt.Name
							if err := codegen.IsCompatible(t.MethodResultAttr.Type, natt.Attribute.Type, "in", "out"); err == nil {
								// imports from source and target attributes
								for _, im := range gatherAttributeImports(agent.Genpkg, t.MethodResultAttr) {
									if im != nil && im.Path != "" {
										extraImports[im.Path] = im
									}
								}
								for _, im := range gatherAttributeImports(agent.Genpkg, t.Artifact) {
									if im != nil && im.Path != "" {
										extraImports[im.Path] = im
									}
								}
								// Build a synthetic user type for the sidecar so we can use
								// GoFullTypeRef to compute the proper type reference with
								// correct pointer semantics (no string surgery).
								// Make sure any nested user types that collide with tool-facing
								// names are renamed to the same unique helper names used by the
								// generated specs package (e.g., GetTimeSeriesResult -> GetTimeSeriesResult22).
								artifactAtt := t.Artifact
								if data.Scope != nil && data.types != nil {
									artifactAtt = rewriteCollidingNestedUserTypes(artifactAtt, data.Scope, data.types)
								}
								sidecarUT := &goaexpr.UserTypeExpr{
									AttributeExpr: artifactAtt,
									TypeName:      toolSidecar.TypeName,
								}
								sidecarAttr := &goaexpr.AttributeExpr{Type: sidecarUT}
								sidecarRef := scope.GoFullTypeRef(sidecarAttr, specsAlias)
								sidecarTypeName := scope.GoFullTypeName(sidecarAttr, specsAlias)
								fieldNameGo := codegen.Goify(fieldName, true)
								// Keep parameter type aligned with Init<GoName>ToolResult.
								serviceResRef := t.MethodResultTypeRef
								if (t.MethodResultLoc == nil || t.MethodResultLoc.PackageName() == "") && svcAlias != "" {
									if ref := scope.GoFullTypeRef(t.MethodResultAttr, svcAlias); ref != "" {
										serviceResRef = ref
									}
								}

								// Generate Init<GoName>ToolArtifact: service method result -> sidecar artifact field type.
								// For wrappers we need to convert the method result to the wrapper field type
								// (not the tool-facing bounded result).
								artifactFnName := "Init" + codegen.Goify(t.Name, true) + "ToolArtifact"
								if fieldUT, ok := natt.Attribute.Type.(goaexpr.UserType); ok && fieldUT != nil && data.Scope != nil {
									// Resolve the unique helper type name used in the specs package.
									var baseName string
									switch u := fieldUT.(type) {
									case *goaexpr.UserTypeExpr:
										baseName = codegen.Goify(u.TypeName, true)
									case *goaexpr.ResultTypeExpr:
										baseName = codegen.Goify(u.TypeName, true)
									}
									uniqueName := baseName
									if toolSidecar.Def != "" {
										if tn, ok := parseSidecarArtifactTypeName(toolSidecar.Def); ok && tn != "" {
											uniqueName = tn
										}
									} else if baseName != "" {
										uniqueName = data.Scope.HashedUnique(fieldUT, baseName)
									}
									baseAttr := fieldUT.Attribute()
									if baseAttr != nil && baseAttr.Type == goaexpr.Empty {
										baseAttr = &goaexpr.AttributeExpr{Type: &goaexpr.Object{}}
									}
									fieldToolUT := &goaexpr.UserTypeExpr{
										AttributeExpr: stripStructPkgMeta(baseAttr),
										TypeName:      uniqueName,
									}
									fieldToolAttr := &goaexpr.AttributeExpr{Type: fieldToolUT}
									srcCtx := codegen.NewAttributeContextForConversion(false, false, false, svcAlias, scope)
									tgtCtx := codegen.NewAttributeContextForConversion(false, false, false, specsAlias, scope)
									artifactBody, helpers, terr := codegen.GoTransform(t.MethodResultAttr, fieldToolAttr, "in", "out", srcCtx, tgtCtx, "", false)
									if terr == nil && artifactBody != "" {
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
										artifactRef := scope.GoFullTypeRef(fieldToolAttr, specsAlias)
										fns = append(fns, transformFuncData{
											Name:          artifactFnName,
											ParamTypeRef:  serviceResRef,
											ResultTypeRef: artifactRef,
											Body:          artifactBody,
											Helpers:       nil,
										})
									}
								}
								// Body: wrap the entire method result into the lone sidecar field
								// using Init<GoName>ToolArtifact so the full method result is preserved
								// in the UI-only artifact.
								body := fmt.Sprintf(
									"out = &%s{\n\t%s: %s(in),\n}\n",
									sidecarTypeName,
									fieldNameGo,
									artifactFnName,
								)
								fns = append(fns, transformFuncData{
									Name:          "Init" + codegen.Goify(t.Name, true) + "SidecarFromMethodResult",
									ParamTypeRef:  serviceResRef,
									ResultTypeRef: sidecarRef,
									Body:          body,
									Helpers:       nil,
								})
								wrapHandled = true
								break
							}
						}
					}
				}
				if !wrapHandled {
					if err := codegen.IsCompatible(t.MethodResultAttr.Type, t.Artifact.Type, "in", "out"); err == nil {
						for _, im := range gatherAttributeImports(agent.Genpkg, t.MethodResultAttr) {
							if im != nil && im.Path != "" {
								extraImports[im.Path] = im
							}
						}
						for _, im := range gatherAttributeImports(agent.Genpkg, t.Artifact) {
							if im != nil && im.Path != "" {
								extraImports[im.Path] = im
							}
						}
						srcCtx := codegen.NewAttributeContextForConversion(false, false, false, svcAlias, scope)
						// Build a local alias user type for the sidecar. Do not propagate struct:pkg:path
						// from the base attribute so initializers/casts use the specs package alias.
						var metaBase *goaexpr.AttributeExpr
						if ut, ok := t.Artifact.Type.(goaexpr.UserType); ok && ut != nil {
							metaBase = ut.Attribute()
						} else {
							metaBase = t.Artifact
						}
						dupMeta := *metaBase
						if dupMeta.Meta != nil {
							delete(dupMeta.Meta, "struct:pkg:path")
						}
						rewrittenMeta := rewriteNestedLocalUserTypes(&dupMeta)
						metaUT := &goaexpr.UserTypeExpr{AttributeExpr: rewrittenMeta, TypeName: toolSidecar.TypeName}
						metaAttr := &goaexpr.AttributeExpr{Type: metaUT}
						tgtCtx := codegen.NewAttributeContextForConversion(false, false, false, specsAlias, scope)
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
							metaRef := scope.GoFullTypeRef(metaAttr, specsAlias)
							// Keep parameter type aligned with Init<GoName>ToolResult.
							serviceResRef := t.MethodResultTypeRef
							if (t.MethodResultLoc == nil || t.MethodResultLoc.PackageName() == "") && svcAlias != "" {
								if ref := scope.GoFullTypeRef(t.MethodResultAttr, svcAlias); ref != "" {
									serviceResRef = ref
								}
							}
							fns = append(fns, transformFuncData{
								Name:          "Init" + codegen.Goify(t.Name, true) + "SidecarFromMethodResult",
								ParamTypeRef:  serviceResRef,
								ResultTypeRef: metaRef,
								Body:          body,
								Helpers:       nil,
							})
						}
					}
				}
			}
		}
		if len(fns) == 0 {
			continue
		}
		// Assemble imports: service, specs, and any additional referenced packages
		imports := []*codegen.ImportSpec{{Name: svcAlias, Path: svcImport}, {Path: specsImportPath, Name: specsAlias}}
		for p, im := range extraImports {
			if p == svcImport || p == ts.SpecsImportPath {
				continue
			}
			imports = append(imports, im)
		}
		sections := []*codegen.SectionTemplate{
			codegen.Header(ts.Name+" adapter transforms", ts.PathName, imports),
			{Name: "tool-transforms", Source: agentsTemplates.Read(toolTransformsFileT), Data: transformsFileData{Functions: fns, Helpers: fileHelpers}},
		}
		// Place transforms alongside other generated toolset files (service_executor.go, used_tools.go).
		// Example: gen/<service>/agents/<agent>/<toolset>/transforms.go
		path := filepath.Join(ts.Dir, "transforms.go")
		out = append(out, &codegen.File{Path: path, SectionTemplates: sections})
	}
	return out
}
