// Package codegen checks MCP content variants in generated reply decoders.
// Goa's normal unions put the selected fields inside a separate value object.
// MCP keeps media fields beside the type and selects text or binary resource
// contents by field presence. The decoder rejects invalid selected fields before
// returning content to the application.
package codegen

import (
	"fmt"
	"path/filepath"

	"goa.design/goa/v3/codegen"
	goahttpcodegen "goa.design/goa/v3/http/codegen"
)

// applyMCPContentValidation adds the flat MCP content rule to the existing
// Goa validators, preserving their type names, field checks and error paths.
func applyMCPContentValidation(files []*codegen.File, services []*plannedMCPService) error {
	paths := make(map[string]map[string]bool)
	for _, service := range services {
		if len(service.adapterData.Resources) == 0 && len(service.adapterData.Tools) == 0 && len(service.adapterData.StaticPrompts) == 0 && len(service.adapterData.MethodPrompts) == 0 {
			continue
		}
		validators := map[string]bool{"ResourceContent": false}
		if len(service.adapterData.Tools) > 0 || len(service.adapterData.StaticPrompts) > 0 || len(service.adapterData.MethodPrompts) > 0 {
			validators["ContentItem"] = false
		}
		paths[filepath.ToSlash(filepath.Join(codegen.Gendir, "jsonrpc", service.adapterData.mcpPathName, "client", "types.go"))] = validators
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
		codegen.AddImport(header, codegen.SimpleImport("encoding/json"))
		for _, section := range file.SectionTemplates {
			if section.Name != "client-validate" {
				continue
			}
			data, ok := section.Data.(*goahttpcodegen.TypeData)
			if !ok {
				return fmt.Errorf("MCP client validator in %q has unexpected data %T", file.Path, section.Data)
			}
			if _, ok := paths[filePath][data.Name]; !ok {
				continue
			}
			validation := resourceContentValidation
			if data.Name == "ContentItem" {
				validation = contentItemValidation
			}
			data.ValidateDef += validation(`"body"`)
			if data.NestedValidatorDeclaration != nil {
				data.NestedValidateDef += validation("path")
			}
			paths[filePath][data.Name] = true
		}
	}
	for filePath, validators := range paths {
		for name, found := range validators {
			if !found {
				return fmt.Errorf("goa did not generate MCP %s validation in %q", name, filePath)
			}
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
`, errorPath, errorPath) + contentMetadataValidation(errorPath)
}

// contentItemValidation checks fields whose presence depends on the content
// discriminator. Goa's existing validator checks each field's type and values.
func contentItemValidation(errorPath string) string {
	return fmt.Sprintf(`
// The selected content kind determines which fields the peer must supply.
if body.Type != nil {
switch *body.Type {
case "text":
 if body.Text == nil {
  err = goa.MergeErrors(err, goa.MissingFieldError(%s + ".text", "content"))
 }
case "image", "audio":
 if body.Data == nil {
  err = goa.MergeErrors(err, goa.MissingFieldError(%s + ".data", "content"))
 } else if _, decodeErr := base64.StdEncoding.DecodeString(*body.Data); decodeErr != nil {
  err = goa.MergeErrors(err, goa.PermanentError("invalid_content", "%%s.data must contain base64 data", %s))
 }
 if body.MimeType == nil {
  err = goa.MergeErrors(err, goa.MissingFieldError(%s + ".mimeType", "content"))
 }
case "resource_link":
 if body.Name == nil {
  err = goa.MergeErrors(err, goa.MissingFieldError(%s + ".name", "content"))
 }
 if body.URI == nil {
  err = goa.MergeErrors(err, goa.MissingFieldError(%s + ".uri", "content"))
 }
case "resource":
 if body.Resource == nil {
  err = goa.MergeErrors(err, goa.MissingFieldError(%s + ".resource", "content"))
 }
}
}
`, errorPath, errorPath, errorPath, errorPath, errorPath, errorPath, errorPath) + contentMetadataValidation(errorPath)
}

// contentMetadataValidation rejects extension metadata that is not an object.
// Its fields remain encoded JSON because each extension owns their meaning.
func contentMetadataValidation(errorPath string) string {
	return fmt.Sprintf(`
if len(body.Meta) > 0 {
 var metadata map[string]json.RawMessage
 if metadataErr := json.Unmarshal(body.Meta, &metadata); metadataErr != nil || metadata == nil {
  err = goa.MergeErrors(err, goa.InvalidFieldTypeError(%s + "._meta", string(body.Meta), "JSON object"))
 }
}
`, errorPath)
}
