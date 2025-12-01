package codegen

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	agentsExpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// rewriteNestedLocalUserTypes walks the attribute and replaces service-local user
// types (types without an explicit struct:pkg:path locator) with local user types
// that use the same public type names. This ensures that transforms targeting
// specs-local aliases reference the emitted helper types (e.g., App, AppInput)
// instead of inventing new names.
func rewriteNestedLocalUserTypes(att *goaexpr.AttributeExpr) *goaexpr.AttributeExpr {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return att
	}
	switch dt := att.Type.(type) {
	case goaexpr.UserType:
		// Preserve external locators; rewrite only service-local user types.
		if loc := codegen.UserTypeLocation(dt); loc != nil && loc.RelImportPath != "" {
			// Recurse into attribute to update children.
			return &goaexpr.AttributeExpr{Type: dt}
		}
		// Compute the local public name from the user type.
		name := ""
		var base *goaexpr.AttributeExpr
		switch u := dt.(type) {
		case *goaexpr.UserTypeExpr:
			name = u.TypeName
			base = u.Attribute()
		case *goaexpr.ResultTypeExpr:
			name = u.TypeName
			base = u.Attribute()
		default:
			return att
		}
		// Recurse into the underlying attribute, do not propagate struct:pkg:path.
		var dup goaexpr.AttributeExpr
		if base != nil {
			dup = *base
			if dup.Meta != nil {
				delete(dup.Meta, "struct:pkg:path")
			}
		}
		return &goaexpr.AttributeExpr{Type: &goaexpr.UserTypeExpr{
			AttributeExpr: rewriteNestedLocalUserTypes(&dup),
			TypeName:      name,
		}}
	case *goaexpr.Array:
		return &goaexpr.AttributeExpr{Type: &goaexpr.Array{ElemType: rewriteNestedLocalUserTypes(dt.ElemType)}}
	case *goaexpr.Map:
		return &goaexpr.AttributeExpr{Type: &goaexpr.Map{
			KeyType:  rewriteNestedLocalUserTypes(dt.KeyType),
			ElemType: rewriteNestedLocalUserTypes(dt.ElemType),
		}}
	case *goaexpr.Object:
		obj := &goaexpr.Object{}
		for _, nat := range *dt {
			var dup *goaexpr.AttributeExpr
			if nat.Attribute != nil {
				dup = rewriteNestedLocalUserTypes(nat.Attribute)
			}
			*obj = append(*obj, &goaexpr.NamedAttributeExpr{
				Name:      nat.Name,
				Attribute: dup,
			})
		}
		return &goaexpr.AttributeExpr{Type: obj, Description: att.Description, Docs: att.Docs, Validation: att.Validation}
	case *goaexpr.Union:
		// Leave unions unchanged.
		return att
	default:
		return att
	}
}

type toolSpecFileData struct {
	PackageName string
	Tools       []*toolEntry
	Types       []*typeData
}

type toolTypesFileData struct {
	Types []*typeData
}

type toolCodecsFileData struct {
	Types []*typeData
	Tools []*toolEntry
}

type toolSpecsAggregateData struct {
	Toolsets []*ToolsetData
}

type agentToolsetFileData struct {
	PackageName string
	Toolset     *ToolsetData
}

type agentToolsetConsumerFileData struct {
	Agent         *AgentData
	Toolset       *ToolsetData
	ProviderAlias string
}

type serviceToolsetFileData struct {
	PackageName     string
	Agent           *AgentData
	Toolset         *ToolsetData
	ServicePkgAlias string
}

// transforms metadata used by tool_transforms.go.tpl
type transformFuncData struct {
	Name          string
	ParamTypeRef  string
	ResultTypeRef string
	Body          string
	Helpers       []*codegen.TransformFunctionData
}

type transformsFileData struct {
	HeaderComment string
	PackageName   string
	Imports       []*codegen.ImportSpec
	Functions     []transformFuncData
	// Helpers contains a file-level, de-duplicated list of helper transform
	// functions referenced by any of the Functions bodies. Rendering helpers at
	// the file scope avoids duplicate helper definitions when multiple
	// transforms share the same nested conversions.
	Helpers []*codegen.TransformFunctionData
}

// Generate is the code generation entry point for the agents plugin. It is called
// by the Goa code generation framework during the `goa gen` command execution.
//
// The function scans the provided DSL roots for agent declarations, transforms them
// into template-ready data structures, and generates all necessary Go files for each
// agent. Generated artifacts include:
//   - agent.go: agent struct and constructor
//   - config.go: configuration types and validation
//   - workflow.go: workflow definition and handler
//   - activities.go: activity definitions (plan, resume, execute_tool)
//   - registry.go: engine registration helpers
//   - specs/<toolset>/: per-toolset specifications, types, and codecs
//   - specs/specs.go: agent-level aggregator of all toolset specs
//   - agenttools/: helpers for exported toolsets (agent-as-tool pattern)
//   - (optional) service_toolset.go: executor-first registration for method-backed toolsets
//
// Parameters:
//   - genpkg: Go import path to the generated code root (e.g., "myapp/gen")
//   - roots: evaluated DSL roots; must include both goaexpr.RootExpr (for services)
//     and agentsExpr.Root (for agents)
//   - files: existing generated files from other Goa plugins; agent files are appended
//
// Returns the input files slice with agent-generated files appended. If no agents are
// declared in the DSL, returns the input slice unchanged. Returns an error if:
//   - The agents root cannot be located in roots
//   - A service referenced by an agent is not found
//   - Template rendering fails
//   - Tool spec generation fails
//
// Generated files follow the structure:
//
//	gen/<service>/agents/<agent>/*.go
//	gen/<service>/agents/<agent>/specs/<toolset>/*.go
//	gen/<service>/agents/<agent>/specs/specs.go
//	gen/<service>/agents/<agent>/agenttools/<toolset>/helpers.go
//	# Note: service_toolset.go is not emitted in the current generator path; registrations
//	# are built at application boundaries via runtime APIs. The template and builder remain
//	# for potential future use.
//
// The function is safe to call multiple times during generation but expects DSL
// evaluation to be complete before invocation.
func Generate(genpkg string, roots []eval.Root, files []*codegen.File) ([]*codegen.File, error) {
	data, err := buildGeneratorData(genpkg, roots)
	if err != nil {
		return nil, err
	}
	if len(data.Services) == 0 {
		return files, nil
	}

	var generated []*codegen.File
	for _, svc := range data.Services {
		for _, agent := range svc.Agents {
			afiles := agentFiles(agent)
			generated = append(generated, afiles...)
			// Adapter stubs are only generated during the `goa example` phase.
		}
	}

	// Emit contextual quickstart README at module root unless disabled via DSL.
	if !agentsExpr.Root.DisableAgentDocs {
		if qf := quickstartReadmeFile(data); qf != nil {
			generated = append(generated, qf)
		}
	}

	return append(files, generated...), nil
}

// GenerateExample appends a service-local bootstrap helper and planner stub(s)
// so developers can run agents inside the service process with no manual wiring.
//
// Behavior:
//   - For each service that declares at least one agent, emits:
//   - cmd/<service>/agents_bootstrap.go
//   - cmd/<service>/agents_planner_<agent>.go (one per agent)
//   - Patches cmd/<service>/main.go to call bootstrapAgents(ctx) at process start.
//
// The function is idempotent over the in-memory file list provided by Goa’s example
// pipeline. It does not modify gen/ output; it only adds/patches service-side files.
func GenerateExample(genpkg string, roots []eval.Root, files []*codegen.File) ([]*codegen.File, error) {
	data, err := buildGeneratorData(genpkg, roots)
	if err != nil {
		return nil, err
	}
	if len(data.Services) == 0 {
		return files, nil
	}

	// Emit application-owned scaffold under internal/agents/; do not patch main.
	moduleBase := moduleBaseImport(genpkg)
	for _, svc := range data.Services {
		if len(svc.Agents) == 0 {
			continue
		}
		if f := emitInternalBootstrap(svc, moduleBase); f != nil {
			files = append(files, f)
		}
		for _, ag := range svc.Agents {
			if f := emitPlannerInternalStub(moduleBase, ag); f != nil {
				files = append(files, f)
			}
			// Internal executor stubs under internal/agents/<agent>/toolsets/<toolset>/
			for _, ts := range ag.AllToolsets {
				var has bool
				for _, t := range ts.Tools {
					if t.IsMethodBacked {
						has = true
						break
					}
				}
				if !has {
					continue
				}
				if f := emitExecutorInternalStub(ag, ts); f != nil {
					files = append(files, f)
				}
			}
		}
	}
	return files, nil
}

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

// agentRouteRegisterFile emits Register<Agent>Route(ctx, rt) so caller processes
// can register route-only metadata and enable ExecuteAgentInline across processes.
// agentRouteRegisterFile removed: routes piggyback on toolset registration.

// agentPerToolsetSpecsFiles emits types/codecs/specs under specs/<toolset>/ using
// short, tool-local type names to avoid collisions between toolsets.
func agentPerToolsetSpecsFiles(agent *AgentData) []*codegen.File {
	var out []*codegen.File
	emitted := make(map[string]struct{})
	for _, ts := range agent.AllToolsets {
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
				{Name: "tool-spec-types", Source: agentsTemplates.Read(toolTypesFileT), Data: toolTypesFileData{Types: pure}},
			}
			out = append(out, &codegen.File{Path: filepath.Join(ts.SpecsDir, "types.go"), SectionTemplates: sections})
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
			specImports := []*codegen.ImportSpec{{Path: "sort"}, {Path: "goa.design/goa-ai/runtime/agent/policy"}, {Path: "goa.design/goa-ai/runtime/agent/tools"}}
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

	type toolSchema struct {
		ID          string      `json:"id"`
		Service     string      `json:"service"`
		Toolset     string      `json:"toolset"`
		Title       string      `json:"title,omitempty"`
		Description string      `json:"description,omitempty"`
		Tags        []string    `json:"tags,omitempty"`
		Payload     *typeSchema `json:"payload,omitempty"`
		Result      *typeSchema `json:"result,omitempty"`
		Sidecar     *typeSchema `json:"sidecar,omitempty"`
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
			Source: string(payload),
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
		// Resolve service import alias/path for method types
		svcAlias := servicePkgAlias(svc)
		svcImport := joinImportPath(agent.Genpkg, svc.PathName)
		// Use the actual specs package name so GoTransform qualifier matches (e.g., atlas_read).
		specsAlias := ts.SpecsPackageName
		specsImportPath := ts.SpecsImportPath
		// Avoid alias collisions when the toolset package name matches the service
		// package name (for example, service "todos" with toolset "todos").
		if svcAlias == specsAlias {
			svcAlias += "svc"
		}

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
					srcCtx := codegen.NewAttributeContextForConversion(false, false, false, ts.SpecsPackageName, scope)
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
						// Compute fully-qualified service payload ref using the service
						// alias so that the result type always matches the service
						// package, even when the toolset package name matches the
						// service package name (e.g., service "todos" with toolset
						// "todos").
						serviceRef := scope.GoFullTypeRef(t.MethodPayloadAttr, svcAlias)
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
						// Compute correct result type reference (including composites) and qualify with specs alias.
						resRef := scope.GoFullTypeRef(targetAttr, specsAlias)
						// Use precomputed fully-qualified service result ref to handle external imports (e.g., types.*).
						serviceResRef := t.MethodResultTypeRef
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
			if toolSidecar != nil && t.Sidecar != nil && t.Sidecar.Type != goaexpr.Empty && t.MethodResultAttr != nil && t.MethodResultAttr.Type != goaexpr.Empty {
				wrapHandled := false
				// Fast-path: if the sidecar user type is a single-field wrapper whose
				// field type matches the method result type, synthesize a direct
				// wrapper transform instead of relying on structural field matches.
				if ut, ok := t.Sidecar.Type.(goaexpr.UserType); ok && ut != nil {
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
								for _, im := range gatherAttributeImports(agent.Genpkg, t.Sidecar) {
									if im != nil && im.Path != "" {
										extraImports[im.Path] = im
									}
								}
								// Build fully-qualified sidecar wrapper type name in the specs package.
								sidecarType := fmt.Sprintf("%s.%s", specsAlias, toolSidecar.TypeName)
								serviceResRef := t.MethodResultTypeRef
								// Body: wrap the entire method result into the lone sidecar field
								// using the already-generated Init<GoName>ToolResult helper to
								// convert the service result into the specs-level result type.
								body := fmt.Sprintf(
									"out = &%s{\n\t%s: Init%sToolResult(in),\n}\n",
									sidecarType,
									codegen.Goify(fieldName, true),
									codegen.Goify(t.Name, true),
								)
								fns = append(fns, transformFuncData{
									Name:          "Init" + codegen.Goify(t.Name, true) + "SidecarFromMethodResult",
									ParamTypeRef:  serviceResRef,
									ResultTypeRef: "*" + sidecarType,
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
					if err := codegen.IsCompatible(t.MethodResultAttr.Type, t.Sidecar.Type, "in", "out"); err == nil {
						for _, im := range gatherAttributeImports(agent.Genpkg, t.MethodResultAttr) {
							if im != nil && im.Path != "" {
								extraImports[im.Path] = im
							}
						}
						for _, im := range gatherAttributeImports(agent.Genpkg, t.Sidecar) {
							if im != nil && im.Path != "" {
								extraImports[im.Path] = im
							}
						}
						srcCtx := codegen.NewAttributeContextForConversion(false, false, false, svcAlias, scope)
						// Build a local alias user type for the sidecar. Do not propagate struct:pkg:path
						// from the base attribute so initializers/casts use the specs package alias.
						var metaBase *goaexpr.AttributeExpr
						if ut, ok := t.Sidecar.Type.(goaexpr.UserType); ok && ut != nil {
							metaBase = ut.Attribute()
						} else {
							metaBase = t.Sidecar
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
							serviceResRef := t.MethodResultTypeRef
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

// agentSpecsAggregatorFile emits specs/specs.go that aggregates Specs and metadata
// from all specs/<toolset> packages into a single package for convenience.
func agentSpecsAggregatorFile(agent *AgentData) *codegen.File {
	// Build import list: runtime + per-toolset packages
	imports := []*codegen.ImportSpec{
		{Path: "embed", Name: "_"},
		{Path: "encoding/json"},
		{Path: "sort"},
		{Path: "goa.design/goa-ai/runtime/agent/policy"},
		{Path: "goa.design/goa-ai/runtime/agent/tools"},
	}
	added := make(map[string]struct{})
	toolsets := make([]*ToolsetData, 0, len(agent.AllToolsets))
	for _, ts := range agent.AllToolsets {
		if len(ts.Tools) == 0 || ts.SpecsImportPath == "" {
			continue
		}
		if _, ok := added[ts.SpecsImportPath]; ok {
			continue
		}
		imports = append(imports, &codegen.ImportSpec{Path: ts.SpecsImportPath, Name: ts.SpecsPackageName})
		added[ts.SpecsImportPath] = struct{}{}
		toolsets = append(toolsets, ts)
	}
	if len(toolsets) == 0 {
		return nil
	}
	sections := []*codegen.SectionTemplate{
		codegen.Header(agent.StructName+" aggregated tool specs", "specs", imports),
		{Name: "tool-specs-aggregate", Source: agentsTemplates.Read(toolSpecsAggregateT), Data: toolSpecsAggregateData{Toolsets: toolsets}},
	}
	return &codegen.File{Path: filepath.Join(agent.Dir, "specs", "specs.go"), SectionTemplates: sections}
}

func agentImplFile(agent *AgentData) *codegen.File {
	imports := []*codegen.ImportSpec{
		{Path: "errors"},
		{Path: "strings"},
		{Path: "context"},
		{Path: "goa.design/goa-ai/runtime/agent/engine"},
		{Path: "goa.design/goa-ai/runtime/agent", Name: "agent"},
		{Path: "goa.design/goa-ai/runtime/agent/runtime", Name: "runtime"},
		{Path: "goa.design/goa-ai/runtime/agent/planner"},
	}
	sections := []*codegen.SectionTemplate{
		codegen.Header(agent.StructName+" implementation", agent.PackageName, imports),
		{
			Name:   "agent-impl",
			Source: agentsTemplates.Read(agentFileT),
			Data:   agent,
		},
	}
	return &codegen.File{Path: filepath.Join(agent.Dir, "agent.go"), SectionTemplates: sections}
}

func agentConfigFile(agent *AgentData) *codegen.File {
	imports := []*codegen.ImportSpec{
		{Path: "errors"},
		{Path: "goa.design/goa-ai/runtime/agent/planner"},
	}
	// Import model client when a compress-history policy is configured so the
	// generated config can reference model.Client in the HistoryModel field.
	if agent.RunPolicy.History != nil && agent.RunPolicy.History.Mode == "compress" {
		imports = append(imports,
			&codegen.ImportSpec{Path: "goa.design/goa-ai/runtime/agent/model", Name: "model"},
		)
	}
	// Determine whether fmt is needed. The config Validate() uses fmt.Errorf for
	// missing method-backed toolset dependencies and for MCP callers.
	needsFmt := false
	if len(agent.MCPToolsets) > 0 {
		needsFmt = true
		imports = append(imports,
			&codegen.ImportSpec{Name: "mcpruntime", Path: "goa.design/goa-ai/runtime/mcp"},
		)
	}
	// Scan toolsets to see if any tool is method-backed; if so, fmt is also required.
	if !needsFmt {
		for _, ts := range agent.AllToolsets {
			for _, t := range ts.Tools {
				if t.IsMethodBacked {
					needsFmt = true
					break
				}
			}
			if needsFmt {
				break
			}
		}
	}
	if needsFmt {
		imports = append(imports, &codegen.ImportSpec{Path: "fmt"})
	}
	// Import toolset packages that define method-backed tools so config can reference their Config types.
	for _, ts := range agent.AllToolsets {
		has := false
		for _, t := range ts.Tools {
			if t.IsMethodBacked {
				has = true
				break
			}
		}
		if has && ts.PackageImportPath != "" {
			imports = append(imports, &codegen.ImportSpec{Path: ts.PackageImportPath, Name: ts.PackageName})
		}
	}
	sections := []*codegen.SectionTemplate{
		codegen.Header(agent.StructName+" config", agent.PackageName, imports),
		{
			Name:    "agent-config",
			Source:  agentsTemplates.Read(configFileT),
			Data:    agent,
			FuncMap: templateFuncMap(),
		},
	}
	return &codegen.File{Path: filepath.Join(agent.Dir, "config.go"), SectionTemplates: sections}
}

func agentRegistryFile(agent *AgentData) *codegen.File {
	imports := []*codegen.ImportSpec{
		{Path: "context"},
		{Path: "errors"},
		{Path: "fmt"},
		{Path: "goa.design/goa-ai/runtime/agent/engine"},
		{Path: "goa.design/goa-ai/runtime/agent/planner"},
		{Path: "goa.design/goa-ai/runtime/agent/runtime", Name: "agentsruntime"},
	}
	// fmt needed for error messages in registry (used in both MCP and Used toolsets paths)
	hasExternal := false
	for _, ts := range agent.AllToolsets {
		if ts.Expr != nil && ts.Expr.External {
			hasExternal = true
			break
		}
	}
	// Import toolset packages that have method-backed tools so we can call their registration helpers.
	for _, ts := range agent.AllToolsets {
		// Import for method-backed (app-supplied executor) or external MCP (local executor)
		needs := false
		for _, t := range ts.Tools {
			if t.IsMethodBacked {
				needs = true
				break
			}
		}
		if ts.Expr != nil && ts.Expr.External {
			needs = true
		}
		if needs && ts.PackageImportPath != "" {
			imports = append(imports, &codegen.ImportSpec{Path: ts.PackageImportPath, Name: ts.PackageName})
		}
	}
	// Import tools when non-external Used toolsets are present without agenttools
	// helpers; registry templates use tools.Ident for DSL-provided call/result
	// hint templates on these method-backed toolsets.
	needsTools := false
	for _, ts := range agent.UsedToolsets {
		if ts.Expr != nil && ts.Expr.External {
			continue
		}
		if ts.AgentToolsImportPath != "" {
			continue
		}
		needsTools = true
		break
	}
	if needsTools {
		imports = append(imports,
			&codegen.ImportSpec{Path: "goa.design/goa-ai/runtime/agent/tools"},
			&codegen.ImportSpec{Path: "goa.design/goa-ai/runtime/agent/runtime/hints", Name: "hints"},
		)
	}
	if needsTimeImport(agent) {
		imports = append(imports, &codegen.ImportSpec{Path: "time"})
	}
	if len(agent.Tools) > 0 {
		imports = append(imports, &codegen.ImportSpec{Path: agent.ToolSpecsImportPath, Name: agent.ToolSpecsPackage})
	}
	sections := []*codegen.SectionTemplate{
		codegen.Header(agent.StructName+" registry", agent.PackageName, imports),
		{
			Name:   "agent-registry",
			Source: agentsTemplates.Read(registryFileT),
			Data: struct {
				*AgentData
				HasExternalMCP bool
			}{AgentData: agent, HasExternalMCP: hasExternal},
			FuncMap: templateFuncMap(),
		},
	}
	return &codegen.File{
		Path:             filepath.Join(agent.Dir, "registry.go"),
		SectionTemplates: sections,
	}
}

func activityNeedsTime(act ActivityArtifact) bool {
	return act.Timeout > 0 || act.RetryPolicy.InitialInterval > 0
}

func agentActivitiesNeedTimeImport(agent *AgentData) bool {
	for _, act := range agent.Runtime.Activities {
		if activityNeedsTime(act) {
			return true
		}
	}
	return false
}

func needsTimeImport(agent *AgentData) bool {
	if agent.RunPolicy.TimeBudget > 0 {
		return true
	}
	return agentActivitiesNeedTimeImport(agent)
}

func agentToolsFiles(agent *AgentData) []*codegen.File {
	if len(agent.ExportedToolsets) == 0 {
		return nil
	}
	files := make([]*codegen.File, 0, len(agent.ExportedToolsets))
	for _, ts := range agent.ExportedToolsets {
		if ts.AgentToolsDir == "" {
			continue
		}
		data := agentToolsetFileData{PackageName: ts.AgentToolsPackage, Toolset: ts}
		imports := []*codegen.ImportSpec{
			{Path: "goa.design/goa-ai/runtime/agent/runtime", Name: "runtime"},
			{Path: "goa.design/goa-ai/runtime/agent", Name: "agent"},
			{Path: "goa.design/goa-ai/runtime/agent/tools"},
			{Path: "goa.design/goa-ai/runtime/agent/runtime/hints", Name: "hints"},
			{Path: "goa.design/goa-ai/runtime/agent/planner"},
			// Per-toolset specs package for typed payloads
			{Path: ts.SpecsImportPath, Name: ts.SpecsPackageName + "specs"},
		}
		sections := []*codegen.SectionTemplate{
			codegen.Header(ts.Name+" agent tools", ts.AgentToolsPackage, imports),
			{
				Name:    "agent-tools",
				Source:  agentsTemplates.Read(agentToolsFileT),
				Data:    data,
				FuncMap: templateFuncMap(),
			},
		}
		path := filepath.Join(ts.AgentToolsDir, "helpers.go")
		files = append(files, &codegen.File{
			Path:             path,
			SectionTemplates: sections,
		})
	}
	return files
}

// agentToolsConsumerFiles emits thin helpers in the consumer agent package that
// delegate to provider-side agenttools.NewRegistration helpers for toolsets
// exported by other agents. These helpers improve ergonomics for the agent-as-tool
// pattern without hard-coding aggregators or prompts in the generator.
func agentToolsConsumerFiles(agent *AgentData) []*codegen.File {
	if len(agent.UsedToolsets) == 0 {
		return nil
	}
	files := make([]*codegen.File, 0, len(agent.UsedToolsets))
	for _, ts := range agent.UsedToolsets {
		// Only emit helpers when the toolset is backed by an exported agent and
		// we have a provider agenttools package to call into.
		if ts.AgentToolsImportPath == "" || len(ts.Tools) == 0 {
			continue
		}
		data := agentToolsetConsumerFileData{
			Agent:         agent,
			Toolset:       ts,
			ProviderAlias: ts.AgentToolsPackage,
		}
		imports := []*codegen.ImportSpec{
			{Path: "goa.design/goa-ai/runtime/agent/runtime", Name: "runtime"},
			{Path: ts.AgentToolsImportPath, Name: ts.AgentToolsPackage},
		}
		sections := []*codegen.SectionTemplate{
			codegen.Header(
				ts.Name+" agent toolset client",
				agent.PackageName,
				imports,
			),
			{
				Name:    "agent-tools-consumer",
				Source:  agentsTemplates.Read(agentToolsConsumerT),
				Data:    data,
				FuncMap: templateFuncMap(),
			},
		}
		path := filepath.Join(agent.Dir, ts.PathName+"_agenttools_client.go")
		files = append(files, &codegen.File{
			Path:             path,
			SectionTemplates: sections,
		})
	}
	return files
}

// mcpExecutorFiles emits per-external-toolset MCP executors that adapt runtime
// ToolCallExecutor to an mcpruntime.Caller using generated codecs.
func mcpExecutorFiles(agent *AgentData) []*codegen.File {
	out := make([]*codegen.File, 0, len(agent.AllToolsets))
	for _, ts := range agent.AllToolsets {
		if ts.Expr == nil || !ts.Expr.External {
			continue
		}
		data := serviceToolsetFileData{PackageName: ts.PackageName, Agent: agent, Toolset: ts}
		imports := []*codegen.ImportSpec{
			{Path: "context"},
			{Path: "encoding/json"},
			{Path: "strings"},
			{Path: "goa.design/goa-ai/runtime/agent/planner"},
			{Path: "goa.design/goa-ai/runtime/agent/runtime", Name: "runtime"},
			{Path: "goa.design/goa-ai/runtime/agent/telemetry"},
			{Path: "goa.design/goa-ai/runtime/mcp", Name: "mcpruntime"},
			// Per-toolset specs package (codecs + schemas)
			{Path: ts.SpecsImportPath, Name: ts.SpecsPackageName},
		}
		sections := []*codegen.SectionTemplate{
			codegen.Header(ts.Name+" MCP executor", ts.PackageName, imports),
			{
				Name:    "mcp-executor",
				Source:  agentsTemplates.Read(mcpExecutorFileT),
				Data:    data,
				FuncMap: templateFuncMap(),
			},
		}
		path := filepath.Join(ts.Dir, "mcp_executor.go")
		out = append(out, &codegen.File{Path: path, SectionTemplates: sections})
	}
	return out
}

// usedToolsFiles emits typed call builders and type aliases for method-backed Used toolsets
// to align UX with agent-as-tool helpers.
func usedToolsFiles(agent *AgentData) []*codegen.File {
	if len(agent.MethodBackedToolsets) == 0 {
		return nil
	}
	files := make([]*codegen.File, 0, len(agent.MethodBackedToolsets))
	for _, ts := range agent.MethodBackedToolsets {
		// Only emit when specs are present
		if ts.SpecsImportPath == "" || len(ts.Tools) == 0 {
			continue
		}
		data := agentToolsetFileData{PackageName: ts.PackageName, Toolset: ts}
		imports := []*codegen.ImportSpec{
			{Path: "goa.design/goa-ai/runtime/agent/tools"},
			{Path: "goa.design/goa-ai/runtime/agent/planner"},
			// Per-toolset specs package for typed payloads
			{Path: ts.SpecsImportPath, Name: ts.SpecsPackageName + "specs"},
		}
		sections := []*codegen.SectionTemplate{
			codegen.Header(ts.Name+" used tool helpers", ts.PackageName, imports),
			{
				Name:    "used-tools",
				Source:  agentsTemplates.Read(usedToolsFileT),
				Data:    data,
				FuncMap: templateFuncMap(),
			},
		}
		path := filepath.Join(ts.Dir, "used_tools.go")
		files = append(files, &codegen.File{Path: path, SectionTemplates: sections})
	}
	return files
}

// serviceExecutorFiles emits per-toolset service executors that adapt runtime
// ToolCallExecutor to user-provided callers using generated codecs and optional mappers.
func serviceExecutorFiles(agent *AgentData) []*codegen.File {
	if len(agent.MethodBackedToolsets) == 0 {
		return nil
	}
	files := make([]*codegen.File, 0, len(agent.MethodBackedToolsets))
	for _, ts := range agent.MethodBackedToolsets {
		if ts.Expr == nil || len(ts.Tools) == 0 {
			continue
		}
		svcAlias := ""
		svcPkgName := ""
		if svc := ts.SourceService; svc != nil {
			svcAlias = servicePkgAlias(svc)
			svcPkgName = svc.PkgName
			// Avoid alias collisions when the toolset specs package name matches
			// the service package name (for example, service "todos" with toolset
			// "todos").
			if svcAlias == ts.SpecsPackageName {
				svcAlias += "svc"
			}
		}
		// Ensure method payload/result type refs use the same alias as the
		// imported service client package (svcAlias) so assertions in the
		// executor compile even when the specs package shares the original
		// service PkgName (e.g., "todos").
		if svcAlias != "" && svcPkgName != "" {
			oldPrefix := svcPkgName + "."
			newPrefix := svcAlias + "."
			for _, t := range ts.Tools {
				if t.MethodPayloadTypeRef != "" {
					t.MethodPayloadTypeRef = strings.ReplaceAll(
						t.MethodPayloadTypeRef,
						oldPrefix,
						newPrefix,
					)
				}
				if t.MethodResultTypeRef != "" {
					t.MethodResultTypeRef = strings.ReplaceAll(
						t.MethodResultTypeRef,
						oldPrefix,
						newPrefix,
					)
				}
			}
		}
		data := serviceToolsetFileData{
			PackageName:     ts.PackageName,
			Agent:           agent,
			Toolset:         ts,
			ServicePkgAlias: svcAlias,
		}
		imports := []*codegen.ImportSpec{
			{Path: "context"},
			{Path: "encoding/json"},
			{Path: "errors"},
			{Path: "fmt"},
			{Path: "strings"},
			{Path: "goa.design/goa-ai/runtime/agent/planner"},
			{Path: "goa.design/goa-ai/runtime/agent/runtime", Name: "runtime"},
			{Path: "goa.design/goa-ai/runtime/agent/tools"},
			{Path: ts.SpecsImportPath, Name: ts.SpecsPackageName},
		}
		if svc := ts.SourceService; svc != nil {
			// Import the service client package (e.g. gen/atlas_data)
			clientPath := filepath.Join(agent.Genpkg, svc.PathName)
			// Check for slash/backslash issues if Genpkg has slashes
			clientPath = strings.ReplaceAll(clientPath, "\\", "/")
			imports = append(imports, &codegen.ImportSpec{Path: clientPath, Name: svcAlias})
		}
		sections := []*codegen.SectionTemplate{
			codegen.Header(ts.Name+" service executor", ts.PackageName, imports),
			{
				Name:    "service-executor",
				Source:  agentsTemplates.Read(serviceExecutorFileT),
				Data:    data,
				FuncMap: templateFuncMap(),
			},
		}
		path := filepath.Join(ts.Dir, "service_executor.go")
		files = append(files, &codegen.File{Path: path, SectionTemplates: sections})
	}
	return files
}

// Note: we intentionally avoid parsing type references to infer imports. All
// needed user type imports come from Goa's UserTypeLocation captured in ToolData.

// moduleBaseImport returns the module base import path by stripping trailing /gen from genpkg.
func moduleBaseImport(genpkg string) string {
	base := strings.TrimSuffix(genpkg, "/")
	for strings.HasSuffix(base, "/gen") {
		base = strings.TrimSuffix(base, "/gen")
	}
	return base
}

// emitInternalBootstrap emits internal/agents/bootstrap/bootstrap.go with a simple New(ctx) bootstrap.
func emitInternalBootstrap(svc *ServiceAgentsData, moduleBase string) *codegen.File {
	if svc == nil || len(svc.Agents) == 0 {
		return nil
	}
	imports := []*codegen.ImportSpec{
		{Path: "context"},
		{Path: "goa.design/goa-ai/runtime/agent/runtime", Name: "agentsruntime"},
	}
	needsMCP := svc.HasMCP
	if needsMCP {
		imports = append(imports, &codegen.ImportSpec{Path: "fmt"})
		imports = append(imports, &codegen.ImportSpec{Path: "flag"})
		imports = append(imports, &codegen.ImportSpec{Name: "mcpruntime", Path: "goa.design/goa-ai/runtime/mcp"})
	}
	// Import generated agent registration packages and per-agent planner packages.
	type toolsetImport struct{ Alias, Path string }
	type agentImport struct {
		Alias, Path, PlannerAlias, PlannerPath string
		Toolsets                               []toolsetImport
		Agent                                  *AgentData
	}
	agents := make([]agentImport, 0, len(svc.Agents))
	for _, ag := range svc.Agents {
		imports = append(imports, &codegen.ImportSpec{Path: ag.ImportPath, Name: ag.PackageName})
		palias := "planner" + ag.PathName
		ppath := filepath.ToSlash(filepath.Join(moduleBase, "internal", "agents", ag.PathName, "planner"))
		imports = append(imports, &codegen.ImportSpec{Path: ppath, Name: palias})
		// Import internal toolset executor packages for method-backed toolsets.
		var tsImports []toolsetImport
		for _, ts := range ag.MethodBackedToolsets {
			tpath := filepath.ToSlash(filepath.Join(moduleBase, "internal", "agents", ag.PathName, "toolsets", ts.PathName))
			talias := "toolset" + ag.PathName + ts.PathName
			imports = append(imports, &codegen.ImportSpec{Path: tpath, Name: talias})
			tsImports = append(tsImports, toolsetImport{Alias: talias, Path: tpath})
		}
		agents = append(agents, agentImport{Alias: ag.PackageName, Path: ag.ImportPath, PlannerAlias: palias, PlannerPath: ppath, Toolsets: tsImports, Agent: ag})
	}
	path := filepath.Join("internal", "agents", "bootstrap", "bootstrap.go")
	sections := []*codegen.SectionTemplate{
		codegen.Header("Agents bootstrap (internal)", "bootstrap", imports),
		{
			Name:   "bootstrap-internal",
			Source: agentsTemplates.Read(bootstrapInternalT),
			Data: struct {
				Service *ServiceAgentsData
				Agents  []agentImport
			}{svc, agents},
			FuncMap: templateFuncMap(),
		},
	}
	return &codegen.File{Path: path, SectionTemplates: sections}
}

// emitPlannerInternalStub emits internal/agents/<agent>/planner/planner.go with a tiny planner.
func emitPlannerInternalStub(_ string, ag *AgentData) *codegen.File {
	if ag == nil {
		return nil
	}
	imports := []*codegen.ImportSpec{
		{Path: "context"},
		{Path: "goa.design/goa-ai/runtime/agent/planner"},
	}
	sections := []*codegen.SectionTemplate{
		codegen.Header("Planner stub for "+ag.StructName, "planner", imports),
		{Name: "planner-internal-stub", Source: agentsTemplates.Read(plannerInternalStubT), Data: ag},
	}
	path := filepath.Join("internal", "agents", ag.PathName, "planner", "planner.go")
	return &codegen.File{Path: path, SectionTemplates: sections}
}

// emitExecutorInternalStub emits internal/agents/<agent>/toolsets/<toolset>/execute.go stub for method-backed tools.
func emitExecutorInternalStub(ag *AgentData, ts *ToolsetData) *codegen.File {
	src := ts.SourceService
	if src == nil {
		return nil
	}
	// Build imports: agent package for registration.
	agentImport := &codegen.ImportSpec{Path: ag.ImportPath, Name: ag.PackageName}
	imports := []*codegen.ImportSpec{
		codegen.SimpleImport("context"),
		codegen.SimpleImport("errors"),
		agentImport,
		{Path: "goa.design/goa-ai/runtime/agent/runtime"},
		{Path: "goa.design/goa-ai/runtime/agent/planner"},
	}
	// Import specs package for typed payloads and transforms.
	specsAlias := ts.SpecsPackageName + "specs"
	imports = append(imports, &codegen.ImportSpec{Path: ts.SpecsImportPath, Name: specsAlias})

	// Build tool switch metadata
	type execTool struct {
		ID               string
		GoName           string
		PayloadUnmarshal string
		PayloadType      string
	}
	// Pre-allocate for method-backed tools
	count := 0
	for _, t := range ts.Tools {
		if t.IsMethodBacked {
			count++
		}
	}
	tools := make([]execTool, 0, count)
	for _, t := range ts.Tools {
		if !t.IsMethodBacked {
			continue
		}
		g := codegen.Goify(t.Name, true)
		tools = append(tools, execTool{
			ID:               t.Name,
			GoName:           g,
			PayloadUnmarshal: "Unmarshal" + g + "Payload",
			PayloadType:      g + "Payload",
		})
	}
	sections := []*codegen.SectionTemplate{
		codegen.Header(ts.Name+" executor stub for "+ag.StructName, ts.PathName, imports),
		{
			Name:   "example-executor-stub",
			Source: agentsTemplates.Read(exampleExecutorStubT),
			Data: struct {
				Agent       *AgentData
				Toolset     *ToolsetData
				AgentImport *codegen.ImportSpec
				SpecsAlias  string
				Tools       []execTool
			}{ag, ts, agentImport, specsAlias, tools},
			FuncMap: templateFuncMap(),
		},
	}
	path := filepath.Join("internal", "agents", ag.PathName, "toolsets", ts.PathName, "execute.go")
	return &codegen.File{Path: path, SectionTemplates: sections}
}

// quickstartReadmeFile builds the contextual quickstart README at the module root.
// The file is named AGENTS_QUICKSTART.md and is generated unless disabled via DSL.
func quickstartReadmeFile(data *GeneratorData) *codegen.File {
	if data == nil || len(data.Services) == 0 {
		return nil
	}
	sections := []*codegen.SectionTemplate{
		{
			Name:    "agents-quickstart",
			Source:  agentsTemplates.Read(quickstartReadmeT),
			Data:    data,
			FuncMap: templateFuncMap(),
		},
	}
	return &codegen.File{Path: "AGENTS_QUICKSTART.md", SectionTemplates: sections}
}
