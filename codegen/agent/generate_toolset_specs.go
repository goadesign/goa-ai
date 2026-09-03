// Package codegen writes the public and HTTP packages for generated toolsets. The
// package data and all Go names are built before these files are written.
package codegen

import (
	"path/filepath"
	"slices"

	internaladmission "goa.design/goa-ai/internal/toolregistry/admission"
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
	// NeedsInject reports whether a service method tool needs call information
	// while filling fields marked by Inject().
	NeedsInject bool
}

// toolsetSpecsFiles writes each tool package once. A package can contain public
// types, JSON functions, tool descriptions, service conversion functions, and
// an HTTP subpackage used while decoding JSON. Registry toolsets write only
// their tool descriptions.
func toolsetSpecsFiles(plan *toolSpecsPlan) []*codegen.File {
	if plan == nil {
		return nil
	}

	var out []*codegen.File
	dirs := make([]string, 0, len(plan.byDir))
	for dir := range plan.byDir {
		dirs = append(dirs, dir)
	}
	slices.Sort(dirs)

	for _, dir := range dirs {
		packagePlan := plan.byDir[dir]
		ts := packagePlan.render
		if ts == nil {
			continue
		}
		if ts.IsRegistryBacked && ts.Registry != nil {
			out = append(out, toolsetRegistrySpecsFiles(ts, packagePlan)...)
			continue
		}
		if len(ts.Tools) == 0 {
			continue
		}
		specsData := ts.specs

		const (
			transportDirName = "http"
			transportPkgName = "http"
		)

		// http/types.go (+ unions) transport-only package used by codecs for decode+validation.
		transportTypes := make([]*typeData, 0)
		for _, td := range specsData.typesList() {
			if td != nil && td.TransportDef != "" {
				transportTypes = append(transportTypes, td)
			}
		}
		if len(transportTypes) > 0 && ts.SpecsImportPath != "" {
			timports := packagePlan.fileImports.transportTypes.Imports()
			transportSections := []*codegen.SectionTemplate{
				codegen.Header(ts.Name+" tool transport types", transportPkgName, timports),
				{
					Name:    "tool-transport-types",
					Source:  agentsTemplates.Read(toolTransportTypesFileT),
					Data:    toolTransportTypesFileData{Types: transportTypes},
					FuncMap: templateFuncMap(),
				},
			}
			out = append(out, &codegen.File{Path: filepath.Join(ts.SpecsDir, transportDirName, "types.go"), SectionTemplates: transportSections})
			if validateImports := packagePlan.fileImports.transportValidate.Imports(); len(validateImports) > 0 {
				validateSections := []*codegen.SectionTemplate{
					codegen.Header(ts.Name+" tool transport validators", transportPkgName, validateImports),
					{
						Name:    "tool-transport-validate",
						Source:  agentsTemplates.Read(toolTransportValidateFileT),
						Data:    toolTransportTypesFileData{Types: transportTypes},
						FuncMap: templateFuncMap(),
					},
				}
				out = append(out, &codegen.File{Path: filepath.Join(ts.SpecsDir, transportDirName, "validate.go"), SectionTemplates: validateSections})
			}
			if len(specsData.TransportUnions) > 0 {
				unionImports := packagePlan.fileImports.transportUnions.Imports()
				unionSections := []*codegen.SectionTemplate{
					codegen.Header(ts.Name+" tool transport union types", transportPkgName, unionImports),
					{
						Name:    "tool-transport-union-types",
						Source:  agentsTemplates.Read(toolUnionTypesFileT),
						Data:    toolUnionTypesFileData{Unions: specsData.TransportUnions},
						FuncMap: templateFuncMap(),
					},
				}
				out = append(out, &codegen.File{Path: filepath.Join(ts.SpecsDir, transportDirName, "unions.go"), SectionTemplates: unionSections})
			}
		}
		// types.go
		if pure := specsData.pureTypes(); len(pure) > 0 {
			sections := []*codegen.SectionTemplate{
				codegen.Header(ts.Name+" tool types", ts.SpecsPackageName, packagePlan.fileImports.publicTypes.Imports()),
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
			unionImports := packagePlan.fileImports.publicUnions.Imports()
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
			types := specsData.typesList()
			// codecs.go
			codecImports := packagePlan.fileImports.publicCodecs.Imports()
			codecsSections := []*codegen.SectionTemplate{
				codegen.Header(ts.Name+" tool codecs", ts.SpecsPackageName, codecImports),
				{
					Name:   "tool-spec-codecs",
					Source: agentsTemplates.Read(toolCodecsFileT),
					Data: toolCodecsFileData{
						Types:                  types,
						Tools:                  specsData.tools,
						JSONDocumentValidators: specsData.JSONDocumentValidators,
						JSONValidators:         specsData.JSONValidators,
						EmitToolLookups:        true,
						Helpers:                specsData.CodecTransformHelpers,
					},
					FuncMap: templateFuncMap(),
				},
			}
			out = append(out, &codegen.File{Path: filepath.Join(ts.SpecsDir, "codecs.go"), SectionTemplates: codecsSections})
			// specs.go
			specImports := packagePlan.fileImports.publicSpecs.Imports()
			specSections := []*codegen.SectionTemplate{
				codegen.Header(ts.Name+" tool specs", ts.SpecsPackageName, specImports),
				{
					Name:   "tool-specs",
					Source: agentsTemplates.Read(toolSpecFileT),
					Data: toolSpecFileData{
						PackageName:        ts.SpecsPackageName,
						SchemaFingerprints: generatedToolsetSchemaFingerprints(packagePlan.registrationRoutes, specsData.tools),
						Tools:              specsData.tools,
						Types:              specsData.typesList(),
						RequiredLabels:     ts.RequiredLabels,
					},
					FuncMap: templateFuncMap(),
				},
			}
			out = append(out, &codegen.File{Path: filepath.Join(ts.SpecsDir, "specs.go"), SectionTemplates: specSections})
			// inject.go fills fields supplied by the server.
			if toolsNeedInject(ts.Tools) {
				injectSections := []*codegen.SectionTemplate{
					codegen.Header(ts.Name+" tool injection", ts.SpecsPackageName, packagePlan.fileImports.publicInject.Imports()),
					{
						Name:    "tool-inject",
						Source:  agentsTemplates.Read(toolInjectFileT),
						Data:    toolInjectFileData{Tools: ts.Tools},
						FuncMap: templateFuncMap(),
					},
				}
				out = append(out, &codegen.File{Path: filepath.Join(ts.SpecsDir, "inject.go"), SectionTemplates: injectSections})
			}
		}

		if f := toolsetAdapterTransformsFile(ts); f != nil {
			out = append(out, f)
		}
		if f := toolsetProviderFile(ts); f != nil {
			out = append(out, f)
		}
	}

	return out
}

// generatedToolsetSchemaFingerprints computes every registration schema
// identity during generation. Runtime code only selects the route it serves.
func generatedToolsetSchemaFingerprints(routes []string, entries []*toolEntry) []*toolsetSchemaFingerprintData {
	tools := make([]internaladmission.ToolSchema, len(entries))
	for i, entry := range entries {
		description := entry.Description
		var payloadSchema, executionPayloadSchema, resultSchema []byte
		if entry.Payload != nil {
			payloadSchema = entry.Payload.SchemaJSON
			executionPayloadSchema = entry.Payload.ExecutionSchemaJSON
		}
		if entry.Result != nil {
			resultSchema = entry.Result.SchemaJSON
		}
		tools[i] = internaladmission.ToolSchema{
			Name:                   entry.Name,
			Description:            &description,
			Tags:                   entry.Tags,
			PayloadSchema:          payloadSchema,
			ExecutionPayloadSchema: executionPayloadSchema,
			ResultSchema:           resultSchema,
		}
	}
	fingerprints := make([]*toolsetSchemaFingerprintData, len(routes))
	for i, route := range routes {
		fingerprints[i] = &toolsetSchemaFingerprintData{
			Toolset: route,
			Fingerprint: internaladmission.SchemaFingerprint(internaladmission.Schema{
				Name:  route,
				Tools: tools,
			}),
		}
	}
	return fingerprints
}

func toolsetProviderFile(ts *ToolsetData) *codegen.File {
	if ts == nil || ts.SpecsDir == "" || ts.SourceService == nil || ts.IsRegistryBacked {
		return nil
	}
	hasMethods := false
	for _, t := range ts.Tools {
		if t == nil || !t.IsMethodBacked {
			continue
		}
		hasMethods = true
	}
	if !hasMethods {
		return nil
	}
	needsInject := methodToolsNeedInject(ts.Tools)
	sections := []*codegen.SectionTemplate{
		codegen.Header(ts.Name+" tool provider", ts.SpecsPackageName, ts.specs.providerImports),
		{
			Name:   "tool-provider",
			Source: agentsTemplates.Read(toolProviderFileT),
			Data: toolProviderFileData{
				PackageName:    ts.SpecsPackageName,
				ServiceTypeRef: ts.specs.serviceTypeRef,
				Tools:          ts.Tools,
				NeedsInject:    needsInject,
			},
			FuncMap: templateFuncMap(),
		},
	}
	return &codegen.File{
		Path:             filepath.Join(ts.SpecsDir, "provider.go"),
		SectionTemplates: sections,
	}
}

func toolsetRegistrySpecsFiles(ts *ToolsetData, plan *toolSpecsPackagePlan) []*codegen.File {
	if ts == nil || ts.Registry == nil || ts.SpecsDir == "" {
		return nil
	}

	specImports := plan.fileImports.publicSpecs.Imports()
	sections := []*codegen.SectionTemplate{
		codegen.Header(ts.Name+" registry toolset specs", ts.SpecsPackageName, specImports),
		{
			Name:   "registry-toolset-specs",
			Source: agentsTemplates.Read(registryToolsetSpecsFileT),
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
