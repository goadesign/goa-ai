package codegen

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"goa.design/goa/v3/codegen"
)

type registryToolsetSpecsFileData struct {
	PackageName   string
	QualifiedName string
	ServiceName   string
	Registry      *RegistryToolsetMeta
}

type toolProviderFileData struct {
	PackageName    string
	ServiceTypeRef string
	Tools          []*ToolData
}

// toolsetSpecsFiles emits toolset-owned packages (types, unions, codecs, specs,
// and optional transforms) for all toolsets reachable from the generator input.
//
// The function groups toolset references by `SpecsDir` so each toolset's
// tool-facing specs/codecs are emitted exactly once at the chosen owner anchor
// (service-owned toolsets under `gen/<service>/toolsets/<toolset>`, and
// agent-exported toolsets under `gen/<service>/agents/<agent>/exports/<toolset>`).
//
// For each distinct toolset package it emits:
//   - `types.go` for tool payload/result/sidecar types (when any are needed)
//   - `unions.go` for sum-type unions referenced by the tool types (when any)
//   - `codecs.go` for canonical JSON encoding/decoding and validation helpers
//   - `specs.go` for runtime tool discovery metadata and schemas
//   - `transforms.go` when method-backed tools can be adapted via GoTransform
//
// Registry-backed toolsets are handled separately and emit only `specs.go`
// (registry adapters provide the codec and execution wiring).
//
// The function returns nil when there are no services or no eligible toolsets.
func toolsetSpecsFiles(data *GeneratorData) []*codegen.File {
	if data == nil || len(data.Services) == 0 {
		return nil
	}

	type specCandidate struct {
		agent  *AgentData
		toolset *ToolsetData
	}
	candidatesByDir := make(map[string][]specCandidate)
	for _, svc := range data.Services {
		for _, ag := range svc.Agents {
			for _, ts := range ag.AllToolsets {
				if ts == nil || ts.SpecsDir == "" {
					continue
				}
				candidatesByDir[ts.SpecsDir] = append(candidatesByDir[ts.SpecsDir], specCandidate{
					agent:  ag,
					toolset: ts,
				})
			}
		}
	}

	var out []*codegen.File
	dirs := make([]string, 0, len(candidatesByDir))
	for dir := range candidatesByDir {
		dirs = append(dirs, dir)
	}
	slices.Sort(dirs)

	for _, dir := range dirs {
		cands := candidatesByDir[dir]
		if len(cands) == 0 {
			continue
		}
		// Pick a canonical toolset view deterministically.
		slices.SortFunc(cands, func(a, b specCandidate) int {
			// Prefer exported toolsets when present: they are agent-owned.
			if a.toolset.Kind != b.toolset.Kind {
				if a.toolset.Kind == ToolsetKindExported {
					return -1
				}
				if b.toolset.Kind == ToolsetKindExported {
					return 1
				}
				return strings.Compare(string(a.toolset.Kind), string(b.toolset.Kind))
			}
			// Prefer lexicographically smallest owning agent id for determinism.
			if a.agent != nil && b.agent != nil {
				if d := strings.Compare(a.agent.ID, b.agent.ID); d != 0 {
					return d
				}
			}
			return strings.Compare(a.toolset.Name, b.toolset.Name)
		})
		ts := cands[0].toolset
		if ts.IsRegistryBacked && ts.Registry != nil {
			out = append(out, toolsetRegistrySpecsFiles(ts)...)
			continue
		}
		if len(ts.Tools) == 0 {
			continue
		}
		svc := ts.SourceService
		specsData, err := buildToolSpecsDataFor(data.Genpkg, svc, ts.Tools)
		if err != nil || specsData == nil {
			continue
		}
		// types.go
		if pure := specsData.pureTypes(); len(pure) > 0 {
			sections := []*codegen.SectionTemplate{
				codegen.Header(ts.Name+" tool types", ts.SpecsPackageName, specsData.typeImports()),
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
		if len(specsData.Unions) > 0 {
			unionImports := []*codegen.ImportSpec{
				codegen.SimpleImport("encoding/json"),
				codegen.SimpleImport("fmt"),
				codegen.GoaImport(""),
			}
			unionImports = append(unionImports, specsData.typeImports()...)
			unionSections := []*codegen.SectionTemplate{
				codegen.Header(ts.Name+" tool union types", ts.SpecsPackageName, unionImports),
				{
					Name:    "tool-spec-union-types",
					Source:  agentsTemplates.Read(toolUnionTypesFileT),
					Data:    toolUnionTypesFileData{Unions: specsData.Unions},
					FuncMap: templateFuncMap(),
				},
			}
			out = append(out, &codegen.File{Path: filepath.Join(ts.SpecsDir, "unions.go"), SectionTemplates: unionSections})
		}
		if len(specsData.tools) > 0 {
			// codecs.go
			codecsSections := []*codegen.SectionTemplate{
				codegen.Header(ts.Name+" tool codecs", ts.SpecsPackageName, specsData.codecsImports()),
				{
					Name:    "tool-spec-codecs",
					Source:  agentsTemplates.Read(toolCodecsFileT),
					Data:    toolCodecsFileData{Types: specsData.typesList(), Tools: specsData.tools},
					FuncMap: templateFuncMap(),
				},
			}
			out = append(out, &codegen.File{Path: filepath.Join(ts.SpecsDir, "codecs.go"), SectionTemplates: codecsSections})
			// specs.go
			specImports := []*codegen.ImportSpec{
				{Path: "goa.design/goa-ai/runtime/agent/policy"},
				{Path: "goa.design/goa-ai/runtime/agent/tools"},
			}
			specSections := []*codegen.SectionTemplate{
				codegen.Header(ts.Name+" tool specs", ts.SpecsPackageName, specImports),
				{Name: "tool-specs", Source: agentsTemplates.Read(toolSpecFileT), Data: toolSpecFileData{PackageName: ts.SpecsPackageName, Tools: specsData.tools, Types: specsData.typesList()}, FuncMap: templateFuncMap()},
			}
			out = append(out, &codegen.File{Path: filepath.Join(ts.SpecsDir, "specs.go"), SectionTemplates: specSections})
		}

		if f := toolsetAdapterTransformsFile(data.Genpkg, ts); f != nil {
			out = append(out, f)
		}
		if f := toolsetProviderFile(data.Genpkg, ts); f != nil {
			out = append(out, f)
		}
	}

	return out
}

func toolsetProviderFile(genpkg string, ts *ToolsetData) *codegen.File {
	if ts == nil || ts.SpecsDir == "" || ts.SourceService == nil || ts.IsRegistryBacked {
		return nil
	}
	hasMethods := false
	for _, t := range ts.Tools {
		if t != nil && t.IsMethodBacked {
			hasMethods = true
			break
		}
	}
	if !hasMethods {
		return nil
	}
	serviceImportPath := joinImportPath(genpkg, ts.SourceService.PathName)
	if serviceImportPath == "" {
		return nil
	}
	imports := []*codegen.ImportSpec{
		codegen.SimpleImport("context"),
		codegen.SimpleImport("fmt"),
		{Path: "goa.design/goa-ai/runtime/toolregistry"},
		{Name: ts.SourceService.PkgName, Path: serviceImportPath},
	}
	sections := []*codegen.SectionTemplate{
		codegen.Header(ts.Name+" tool provider", ts.SpecsPackageName, imports),
		{
			Name:    "tool-provider",
			Source:  agentsTemplates.Read(toolProviderFileT),
			Data: toolProviderFileData{
				PackageName:    ts.SpecsPackageName,
				ServiceTypeRef: fmt.Sprintf("%s.Service", ts.SourceService.PkgName),
				Tools:          ts.Tools,
			},
			FuncMap: templateFuncMap(),
		},
	}
	return &codegen.File{
		Path:             filepath.Join(ts.SpecsDir, "provider.go"),
		SectionTemplates: sections,
	}
}

func toolsetRegistrySpecsFiles(ts *ToolsetData) []*codegen.File {
	if ts == nil || ts.Registry == nil || ts.SpecsDir == "" {
		return nil
	}

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
	sections := []*codegen.SectionTemplate{
		codegen.Header(ts.Name+" registry toolset specs", ts.SpecsPackageName, specImports),
		{
			Name:    "registry-toolset-specs",
			Source:  agentsTemplates.Read(registryToolsetSpecsFileT),
			Data: registryToolsetSpecsFileData{
				PackageName:   ts.SpecsPackageName,
				QualifiedName: ts.QualifiedName,
				ServiceName:   ts.ServiceName,
				Registry:      ts.Registry,
			},
			FuncMap: templateFuncMap(),
		},
	}
	return []*codegen.File{
		{
			Path:             filepath.Join(ts.SpecsDir, "specs.go"),
			SectionTemplates: sections,
		},
	}
}


