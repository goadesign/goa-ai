// Package codegen adds the text-or-blob rule to generated MCP resource decoders.
// Goa's normal unions include a discriminator, but MCP resource contents use
// field presence instead. The generated decoder rejects ambiguous content before
// returning it to the application; generated servers choose one field from the
// authored service result type.
package codegen

import (
	"fmt"
	"path/filepath"

	"goa.design/goa/v3/codegen"
	goahttpcodegen "goa.design/goa/v3/http/codegen"
)

// applyResourceContentValidation adds the flat MCP content rule to the existing
// Goa validators, preserving their type names, field checks and error paths.
func applyResourceContentValidation(files []*codegen.File, services []*plannedMCPService) error {
	paths := make(map[string]bool)
	for _, service := range services {
		if len(service.adapterData.Resources) == 0 {
			continue
		}
		paths[filepath.ToSlash(filepath.Join(codegen.Gendir, "jsonrpc", service.adapterData.mcpPathName, "client", "types.go"))] = false
	}
	for _, file := range files {
		filePath := filepath.ToSlash(file.Path)
		if _, ok := paths[filePath]; !ok {
			continue
		}
		header := findHeaderSection(file)
		if header == nil {
			return fmt.Errorf("MCP resource client %q has no source header", file.Path)
		}
		codegen.AddImport(header, codegen.SimpleImport("encoding/base64"))
		for _, section := range file.SectionTemplates {
			if section.Name != "client-validate" {
				continue
			}
			data, ok := section.Data.(*goahttpcodegen.TypeData)
			if !ok {
				return fmt.Errorf("MCP client validator in %q has unexpected data %T", file.Path, section.Data)
			}
			if data.Name != "ResourceContent" {
				continue
			}
			data.ValidateDef += resourceContentValidation(`"body"`)
			if data.NestedValidatorDeclaration != nil {
				data.NestedValidateDef += resourceContentValidation("path")
			}
			paths[filePath] = true
		}
	}
	for filePath, found := range paths {
		if !found {
			return fmt.Errorf("goa did not generate the MCP resource content validator in %q", filePath)
		}
	}
	return nil
}

// resourceContentValidation rejects content with neither or both representation
// fields. Empty strings remain present content, as required for empty resources.
func resourceContentValidation(errorPath string) string {
	return fmt.Sprintf(`
// Resource contents choose exactly one representation, including empty content.
if (body.Text == nil) == (body.Blob == nil) {
	err = goa.MergeErrors(err, goa.InvalidFieldTypeError(%s + ".contents", "text/blob", "exactly one of text or blob"))
}
if body.Blob != nil {
	if _, decodeErr := base64.StdEncoding.DecodeString(*body.Blob); decodeErr != nil {
		err = goa.MergeErrors(err, goa.PermanentError("invalid_resource_content", "%%s.blob must contain base64 data", %s))
	}
}
`, errorPath, errorPath)
}
