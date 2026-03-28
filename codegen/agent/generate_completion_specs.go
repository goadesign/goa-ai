package codegen

import (
	"path"
	"path/filepath"
	"sort"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
)

type (
	completionSpecsData struct {
		*toolSpecsData
		completions []*completionEntry
	}

	completionEntry struct {
		Name        string
		GoName      string
		ConstName   string
		Description string
		Result      *typeData
	}
)

// completionSpecsFiles emits one service-owned completions package per service
// that declares typed assistant-output contracts.
func completionSpecsFiles(data *GeneratorData) ([]*codegen.File, error) {
	if data == nil || len(data.Services) == 0 {
		return nil, nil
	}

	const (
		packageName       = "completions"
		dirName           = "completions"
		transportDirName  = "http"
		transportPkgName  = "http"
		transportPkgAlias = "toolhttp"
	)

	out := make([]*codegen.File, 0, len(data.Services)*6)
	for _, svc := range data.Services {
		if svc == nil || svc.Service == nil || len(svc.Completions) == 0 {
			continue
		}
		specsData, err := buildCompletionSpecsData(data.Genpkg, svc.Service, svc.Completions)
		if err != nil {
			return nil, err
		}
		if specsData == nil {
			continue
		}
		dir := filepath.Join(codegen.Gendir, svc.Service.PathName, dirName)
		importPath := path.Join(data.Genpkg, svc.Service.PathName, dirName)

		transportTypes := make([]*typeData, 0)
		for _, td := range specsData.typesList() {
			if td != nil && td.TransportDef != "" {
				transportTypes = append(transportTypes, td)
			}
		}
		if len(transportTypes) > 0 {
			timports := specsData.transportTypeImports()
			transportSections := []*codegen.SectionTemplate{
				codegen.Header(svc.Service.Name+" completion transport types", transportPkgName, timports),
				{
					Name:    "completion-transport-types",
					Source:  agentsTemplates.Read(toolTransportTypesFileT),
					Data:    toolTransportTypesFileData{Types: transportTypes},
					FuncMap: templateFuncMap(),
				},
			}
			out = append(out, &codegen.File{
				Path:             filepath.Join(dir, transportDirName, "types.go"),
				SectionTemplates: transportSections,
			})

			validateImports := []*codegen.ImportSpec{
				codegen.SimpleImport("encoding/json"),
				codegen.SimpleImport("fmt"),
				codegen.GoaImport(""),
			}
			validateImports = append(validateImports, timports...)
			if specsData.needsUnicodeImport() {
				validateImports = append(validateImports, codegen.SimpleImport("unicode/utf8"))
			}
			validateSections := []*codegen.SectionTemplate{
				codegen.Header(svc.Service.Name+" completion transport validators", transportPkgName, validateImports),
				{
					Name:    "completion-transport-validate",
					Source:  agentsTemplates.Read(toolTransportValidateFileT),
					Data:    toolTransportTypesFileData{Types: transportTypes},
					FuncMap: templateFuncMap(),
				},
			}
			out = append(out, &codegen.File{
				Path:             filepath.Join(dir, transportDirName, "validate.go"),
				SectionTemplates: validateSections,
			})
			if len(specsData.TransportUnions) > 0 {
				unionImports := make([]*codegen.ImportSpec, 0, 3+len(timports))
				unionImports = append(unionImports,
					codegen.SimpleImport("encoding/json"),
					codegen.SimpleImport("fmt"),
					codegen.GoaImport(""),
				)
				unionImports = append(unionImports, timports...)
				unionSections := []*codegen.SectionTemplate{
					codegen.Header(svc.Service.Name+" completion transport union types", transportPkgName, unionImports),
					{
						Name:    "completion-transport-union-types",
						Source:  agentsTemplates.Read(toolUnionTypesFileT),
						Data:    toolUnionTypesFileData{Unions: specsData.TransportUnions},
						FuncMap: templateFuncMap(),
					},
				}
				out = append(out, &codegen.File{
					Path:             filepath.Join(dir, transportDirName, "unions.go"),
					SectionTemplates: unionSections,
				})
			}
		}

		if pure := specsData.pureTypes(); len(pure) > 0 {
			sections := []*codegen.SectionTemplate{
				codegen.Header(svc.Service.Name+" completion types", packageName, specsData.typeImports()),
				{
					Name:    "completion-spec-types",
					Source:  agentsTemplates.Read(toolTypesFileT),
					Data:    toolTypesFileData{Types: pure},
					FuncMap: templateFuncMap(),
				},
			}
			out = append(out, &codegen.File{
				Path:             filepath.Join(dir, "types.go"),
				SectionTemplates: sections,
			})
		}
		if len(specsData.Unions) > 0 {
			typeImports := specsData.typeImports()
			unionImports := make([]*codegen.ImportSpec, 0, 3+len(typeImports))
			unionImports = append(unionImports,
				codegen.SimpleImport("encoding/json"),
				codegen.SimpleImport("fmt"),
				codegen.GoaImport(""),
			)
			unionImports = append(unionImports, typeImports...)
			unionSections := []*codegen.SectionTemplate{
				codegen.Header(svc.Service.Name+" completion union types", packageName, unionImports),
				{
					Name:    "completion-spec-union-types",
					Source:  agentsTemplates.Read(toolUnionTypesFileT),
					Data:    toolUnionTypesFileData{Unions: specsData.Unions},
					FuncMap: templateFuncMap(),
				},
			}
			out = append(out, &codegen.File{
				Path:             filepath.Join(dir, "unions.go"),
				SectionTemplates: unionSections,
			})
		}

		codecImports := specsData.codecsImports()
		if len(transportTypes) > 0 {
			transportImport := &codegen.ImportSpec{Name: transportPkgAlias, Path: importPath + "/" + transportDirName}
			if len(codecImports) > 0 && codecImports[len(codecImports)-1].Path == "strings" {
				codecImports = append(codecImports[:len(codecImports)-1], append([]*codegen.ImportSpec{transportImport}, codecImports[len(codecImports)-1:]...)...)
			} else {
				codecImports = append(codecImports, transportImport)
			}
		}
		codecsSections := []*codegen.SectionTemplate{
			codegen.Header(svc.Service.Name+" completion codecs", packageName, codecImports),
			{
				Name:   "completion-spec-codecs",
				Source: agentsTemplates.Read(toolCodecsFileT),
				Data: toolCodecsFileData{
					Types:           specsData.typesList(),
					Helpers:         specsData.CodecTransformHelpers,
					EmitToolLookups: false,
				},
				FuncMap: templateFuncMap(),
			},
		}
		out = append(out, &codegen.File{
			Path:             filepath.Join(dir, "codecs.go"),
			SectionTemplates: codecsSections,
		})

		specImports := []*codegen.ImportSpec{
			{Path: "context"},
			{Path: "goa.design/goa-ai/runtime/agent/completion"},
			{Path: "goa.design/goa-ai/runtime/agent/model"},
			{Path: "goa.design/goa-ai/runtime/agent/tools"},
		}
		specSections := []*codegen.SectionTemplate{
			codegen.Header(svc.Service.Name+" completion specs", packageName, specImports),
			{
				Name:   "completion-specs",
				Source: agentsTemplates.Read(completionSpecFileT),
				Data: completionSpecFileData{
					PackageName: packageName,
					Completions: specsData.completions,
					Types:       specsData.typesList(),
				},
				FuncMap: templateFuncMap(),
			},
		}
		out = append(out, &codegen.File{
			Path:             filepath.Join(dir, "specs.go"),
			SectionTemplates: specSections,
		})
	}

	return out, nil
}

// buildCompletionSpecsData reuses the shared contract type builder to
// materialize result types, schemas, unions, and codecs for direct completions
// without cloning the type-generation pipeline.
func buildCompletionSpecsData(genpkg string, svc *service.Data, completions []*CompletionData) (*completionSpecsData, error) {
	if svc == nil || len(completions) == 0 {
		return nil, nil
	}
	data := &completionSpecsData{toolSpecsData: newToolSpecsData(genpkg, svc)}
	builder := newToolSpecBuilder(genpkg, svc)
	for _, completion := range completions {
		if completion == nil {
			continue
		}
		scope := builder.scopeForTool()
		constName := scope.Unique(completion.GoName)
		result, err := builder.typeFor(newCompletionContractTypeOwner(svc, completion), completion.Result, usageResult)
		if err != nil {
			return nil, err
		}
		data.completions = append(data.completions, &completionEntry{
			Name:        completion.Name,
			GoName:      completion.GoName,
			ConstName:   constName,
			Description: completion.Description,
			Result:      result,
		})
		data.addType(result)
	}
	data.Scope = builder.helperScope
	data.Unions = builder.unionTypes()
	data.TransportUnions = builder.transportUnionTypes()
	data.CodecTransformHelpers = builder.codecTransformHelpers
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
	sort.Slice(data.completions, func(i, j int) bool {
		return data.completions[i].Name < data.completions[j].Name
	})
	return data, nil
}

// newCompletionContractTypeOwner projects a completion into the minimal owner
// metadata needed by the shared contract type builder.
func newCompletionContractTypeOwner(svc *service.Data, completion *CompletionData) *contractTypeOwner {
	if svc == nil || completion == nil {
		return nil
	}
	return &contractTypeOwner{
		Kind:          contractTypeOwnerCompletion,
		Name:          completion.Name,
		QualifiedName: svc.Name + "." + completion.Name,
		ScopeName:     svc.Name + ".completions",
	}
}
