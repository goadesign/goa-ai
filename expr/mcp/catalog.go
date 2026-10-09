// Package mcp checks authored catalog methods before generation. The application
// returns visible operation names or typed resource descriptors and a page
// cursor. Generated adapters retain authored schemas and invoke the configured
// catalog endpoints with their native authorization and input contracts.
package mcp

import (
	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

const resourceCatalogCollection = "resources"

// validateCatalogs checks each catalog against its own declared operation set.
// Catalog methods cannot return unfinished work or choose new runtime schemas.
func (m *MCPExpr) validateCatalogs(verr *eval.ValidationErrors) {
	for _, catalog := range []struct {
		method    *expr.MethodExpr
		field     string
		available bool
	}{
		{m.ToolCatalog, "tools", len(m.Tools) > 0},
		{m.PromptCatalog, "prompts", len(m.Prompts)+len(m.MethodPrompts) > 0},
		{m.ResourceCatalog, resourceCatalogCollection, m.ResourceReader != nil},
		{m.ResourceTemplateCatalog, "resourceTemplates", m.ResourceReader != nil},
	} {
		if catalog.method == nil {
			continue
		}
		if !catalog.available {
			if catalog.field == resourceCatalogCollection || catalog.field == "resourceTemplates" {
				verr.Add(m, "%s catalog requires ResourceReader", catalog.field)
			} else {
				verr.Add(m, "%s catalog requires declared %s", catalog.field, catalog.field)
			}
		}
		validateCatalogMethod(verr, catalog.method, catalog.field)
	}
}

// validateCatalogMethod keeps cursors as native typed strings and permits only
// the fields that become this list reply. Credentials and mapped URL values are
// already removed by the shared argument planner and retain their native owner.
func validateCatalogMethod(verr *eval.ValidationErrors, method *expr.MethodExpr, collection string) {
	if method.IsStreaming() {
		verr.Add(method, "MCP catalog method must be unary")
	}
	for _, key := range []string{mcpinput.ExchangeMetaKey, mcpinput.TaskExchangeMetaKey} {
		if _, declared := method.Meta[key]; declared {
			verr.Add(method, "MCP catalog methods must return completed pages")
		}
	}
	arguments, err := mcpinput.Arguments(method)
	if err != nil {
		verr.Add(method, "%s", err.Error())
		return
	}
	payload := expr.AsObject(arguments.Type)
	if payload == nil || len(*payload) != 1 || payload.Attribute("cursor") == nil || !isPrimitive(payload.Attribute("cursor").Type, expr.String) || arguments.IsRequired("cursor") {
		verr.Add(method, "MCP catalog payload must contain only an optional cursor string apart from native HTTP inputs")
	}
	validateCatalogResult(verr, method, collection)
}

// validateCatalogResult checks the shared page shape independently of its input.
// Catalogs and directory reads return the same descriptors and optional cursor.
func validateCatalogResult(verr *eval.ValidationErrors, method *expr.MethodExpr, collection string) {
	output := mcpinput.Resolved(method.Result)
	var result *expr.Object
	if hasValue(output) {
		result = expr.AsObject(output.Type)
	}
	if result == nil || result.Attribute(collection) == nil {
		verr.Add(method, "MCP catalog result must contain a %s array and optional nextCursor string", collection)
		return
	}
	for _, field := range *result {
		switch field.Name {
		case collection:
			array := expr.AsArray(field.Attribute.Type)
			if collection == "tools" || collection == "prompts" {
				if array == nil || !isPrimitive(array.ElemType.Type, expr.String) {
					verr.Add(method, "MCP catalog %s must be an array of declared names", collection)
				}
			} else {
				if array == nil || !array.NonNullableElems || expr.AsObject(array.ElemType.Type) == nil {
					verr.Add(method, "MCP catalog %s must use ArrayOfRequired of resource descriptors", collection)
				}
			}
		case "nextCursor":
			if !isPrimitive(field.Attribute.Type, expr.String) || output.IsRequired(field.Name) {
				verr.Add(method, "MCP catalog nextCursor must be an optional string")
			}
		default:
			verr.Add(method, "MCP catalog result has unsupported field %q", field.Name)
		}
	}
}
