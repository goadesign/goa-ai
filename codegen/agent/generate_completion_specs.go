// Package codegen writes generated completion result types, JSON functions, and the
// functions that run direct completions.
package codegen

import (
	"fmt"
	"path/filepath"
	"sort"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	completionSpecsData struct {
		*toolSpecsData
		completions []*completionEntry
	}

	completionEntry struct {
		Name        string
		ConstName   string
		SpecFunc    string
		Example     string
		Complete    string
		Stream      string
		Description string
		Result      *typeData
	}
)

// completionSpecsFiles writes one completion package for each service that
// declares completion results.
func completionSpecsFiles(data *GeneratorData, planned *toolSpecsPlan) ([]*codegen.File, error) {
	if data == nil || len(data.Services) == 0 {
		return nil, nil
	}

	const (
		packageName      = "completions"
		dirName          = "completions"
		transportDirName = "http"
		transportPkgName = "http"
	)

	out := make([]*codegen.File, 0, len(data.Services)*6)
	for _, svc := range data.Services {
		if svc == nil || svc.Service == nil || len(svc.Completions) == 0 {
			continue
		}
		packagePlan := planned.completions[svc.Service.Name]
		if packagePlan == nil {
			return nil, fmt.Errorf("service %q completion package was not planned", svc.Service.Name)
		}
		specsData := packagePlan.completion
		if specsData == nil {
			return nil, fmt.Errorf("service %q has no planned completion specs data", svc.Service.Name)
		}
		dir := filepath.Join(codegen.Gendir, svc.Service.PathName, dirName)
		transportTypes := make([]*typeData, 0)
		for _, td := range specsData.typesList() {
			if td != nil && td.TransportDef != "" {
				transportTypes = append(transportTypes, td)
			}
		}
		if len(transportTypes) > 0 {
			timports := packagePlan.fileImports.transportTypes.Imports()
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

			if validateImports := packagePlan.fileImports.transportValidate.Imports(); len(validateImports) > 0 {
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
			}
			if len(specsData.TransportUnions) > 0 {
				unionImports := packagePlan.fileImports.transportUnions.Imports()
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
				codegen.Header(svc.Service.Name+" completion types", packageName, packagePlan.fileImports.publicTypes.Imports()),
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
			unionImports := packagePlan.fileImports.publicUnions.Imports()
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

		codecImports := packagePlan.fileImports.publicCodecs.Imports()
		codecsSections := []*codegen.SectionTemplate{
			codegen.Header(svc.Service.Name+" completion codecs", packageName, codecImports),
			{
				Name:   "completion-spec-codecs",
				Source: agentsTemplates.Read(toolCodecsFileT),
				Data: toolCodecsFileData{
					Types:                  specsData.typesList(),
					JSONDocumentValidators: specsData.JSONDocumentValidators,
					JSONValidators:         specsData.JSONValidators,
					Helpers:                specsData.CodecTransformHelpers,
					EmitToolLookups:        false,
				},
				FuncMap: templateFuncMap(),
			},
		}
		out = append(out, &codegen.File{
			Path:             filepath.Join(dir, "codecs.go"),
			SectionTemplates: codecsSections,
		})

		specImports := packagePlan.fileImports.publicSpecs.Imports()
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

// buildCompletionSpecsDataForPackage builds one service's completion results,
// schemas, and JSON functions with the saved package names.
func buildCompletionSpecsDataForPackage(genpkg string, svc *service.Data, completions []*CompletionData, planned *toolSpecsPackagePlan, api *goaexpr.APIExpr) (*completionSpecsData, error) {
	if svc == nil || len(completions) == 0 {
		return nil, nil
	}
	data := &completionSpecsData{toolSpecsData: newToolSpecsData(genpkg, svc)}
	builder := newToolSpecBuilder(svc, planned, api)
	for _, completion := range completions {
		if completion == nil {
			continue
		}
		names := planned.completionNames[completion.Name]
		result, err := builder.typeFor(newCompletionContractTypeOwner(svc, completion), completion.Result, usageResult)
		if err != nil {
			return nil, err
		}
		entry := &completionEntry{
			Name:        completion.Name,
			ConstName:   names.constant.Name(),
			SpecFunc:    names.spec.Name(),
			Example:     names.example.Name(),
			Complete:    names.complete.Name(),
			Stream:      names.streamComplete.Name(),
			Description: completion.Description,
			Result:      result,
		}
		completion.ConstName = entry.ConstName
		completion.SpecFunc = entry.SpecFunc
		completion.ExampleFunc = entry.Example
		completion.CompleteFunc = entry.Complete
		completion.StreamFunc = entry.Stream
		data.completions = append(data.completions, entry)
		data.addType(result)
	}
	data.Scope = builder.helperScope
	data.Unions = builder.unionTypes()
	data.TransportUnions = builder.transportUnionTypes()
	data.CodecTransformHelpers = builder.codecTransformHelpers
	data.JSONValidators = materializeJSONValidators(planned.jsonValidators)
	data.JSONDocumentValidators = materializeJSONDocumentValidators(planned.jsonDocumentValidators)
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
