// Package codegen generates private consumer codecs and their JSON transport types.
// These files import the provider's payload and leave its generated files intact.
package codegen

import (
	"path/filepath"

	"goa.design/goa-ai/codegen/internal/codec"
	"goa.design/goa-ai/codegen/internal/jsonshape"
	"goa.design/goa/v3/codegen"
)

// consumerInputFiles renders only consumer-owned decoding files. The provider's
// payload definitions and full execution codecs remain in the provider package.
func consumerInputFiles(bindings []*consumerInputPlan) ([]*codegen.File, error) {
	var files []*codegen.File
	for _, binding := range bindings {
		bindingFile, err := consumerBindingFile(binding)
		if err != nil {
			return nil, err
		}
		files = append(files, bindingFile)
		data := binding.specs
		planned := binding.packages
		types := data.typesList()
		var transportTypes []*typeData
		for _, typ := range types {
			if typ.TransportDef != "" {
				transportTypes = append(transportTypes, typ)
			}
		}
		files = append(files, &codegen.File{
			Path: filepath.Join(binding.dir, "codecs.go"),
			SectionTemplates: []*codegen.SectionTemplate{
				codegen.Header("Consumer input codecs", "inputbinding", planned.fileImports.publicCodecs.Imports()),
				{
					Name:   "consumer-input-codecs",
					Source: agentsTemplates.Read(toolCodecsFileT) + jsonshape.ValidatorsSource + codec.StrictJSONReaderSource,
					Data: toolCodecsFileData{
						Types:                  types,
						JSONDocumentValidators: data.JSONDocumentValidators,
						JSONValidators:         data.JSONValidators,
						Helpers:                data.CodecTransformHelpers,
					},
					FuncMap: templateFuncMap(),
				},
			},
		})
		files = append(files, &codegen.File{
			Path: filepath.Join(binding.dir, "http", "types.go"),
			SectionTemplates: []*codegen.SectionTemplate{
				codegen.Header("Consumer input JSON types", "http", planned.fileImports.transportTypes.Imports()),
				{Name: "consumer-input-transport-types", Source: agentsTemplates.Read(toolTransportTypesFileT), Data: toolTransportTypesFileData{Types: transportTypes}, FuncMap: templateFuncMap()},
			},
		})
		if imports := planned.fileImports.transportValidate.Imports(); len(imports) > 0 {
			files = append(files, &codegen.File{
				Path: filepath.Join(binding.dir, "http", "validate.go"),
				SectionTemplates: []*codegen.SectionTemplate{
					codegen.Header("Consumer input JSON validation", "http", imports),
					{Name: "consumer-input-transport-validation", Source: agentsTemplates.Read(toolTransportValidateFileT), Data: toolTransportTypesFileData{Types: transportTypes}, FuncMap: templateFuncMap()},
				},
			})
		}
		if len(data.TransportUnions) > 0 {
			files = append(files, &codegen.File{
				Path:             filepath.Join(binding.dir, "http", "unions.go"),
				SectionTemplates: unionTypeSections(codegen.Header("Consumer input JSON unions", "http", planned.fileImports.transportUnions.Imports()), "consumer-input-transport-unions", data.TransportUnions),
			})
		}
	}
	return files, nil
}
