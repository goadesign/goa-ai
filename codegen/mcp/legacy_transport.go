// Package codegen derives the older HTTP reply shapes from Goa's planned response
// types. Generated servers reuse those types and configured endpoints instead
// of exposing another application service or decoding already-encoded replies.
package codegen

import (
	"fmt"
	"path/filepath"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
	httpcodegen "goa.design/goa/v3/http/codegen"
)

type (
	// legacyTransportData contains only older-wire choices that are already known
	// from the authored methods and Goa's finalized HTTP body declarations.
	legacyTransportData struct {
		Bodies        []legacyBody
		ContentType   *codegen.NameDeclaration
		OutputSchemas map[string]string
	}
	// legacyBody retains the existing response name and, for an operation union,
	// the existing completed branch name. No application value type is copied.
	legacyBody struct {
		Method       string
		Type         *codegen.NameDeclaration
		Ref          string
		CompleteType *codegen.NameDeclaration
	}
)

// buildLegacyTransportData links basic methods to the native HTTP declarations.
// Missing declarations mean the generator cannot safely write a typed encoder.
func buildLegacyTransportData(files []*codegen.File, planned *plannedMCPService, transport httpcodegen.JSONRPCServiceSnapshot) (*legacyTransportData, error) {
	result := &legacyTransportData{OutputSchemas: make(map[string]string)}
	for _, tool := range planned.adapterData.Tools {
		if tool.legacyOutputSchema != "" {
			result.OutputSchemas[tool.Name] = tool.legacyOutputSchema
		}
	}
	types := make(map[string]*codegen.NameDeclaration)
	target := filepath.Join(codegen.Gendir, "jsonrpc", planned.adapterData.mcpPathName, "server", "types.go")
	for _, file := range files {
		if filepath.ToSlash(file.Path) != filepath.ToSlash(target) {
			continue
		}
		for _, section := range file.SectionTemplates {
			if body, ok := section.Data.(*httpcodegen.TypeData); ok {
				types[body.Name] = body.Declaration
			}
		}
	}
	result.ContentType = types["ContentItem"]
	for _, endpoint := range transport.Endpoints {
		switch endpoint.Method.Name {
		case "server/discover", "tools/list", methodToolsCall, "resources/list", methodResourcesRead, "resources/templates/list", "prompts/list", methodPromptsGet, "completion/complete":
		default:
			continue
		}
		if endpoint.Result == nil || len(endpoint.Result.Responses) != 1 || len(endpoint.Result.Responses[0].ServerBody) != 1 {
			return nil, fmt.Errorf("basic MCP method %q must have one planned response body", endpoint.Method.Name)
		}
		body := endpoint.Result.Responses[0].ServerBody[0]
		entry := legacyBody{Method: endpoint.Method.Name, Type: body.Declaration, Ref: body.Ref}
		switch endpoint.Method.Name {
		case methodToolsCall:
			entry.CompleteType = types["ToolsCallCompleteResult"]
			if result.ContentType == nil {
				return nil, fmt.Errorf("basic MCP tools/call has no planned content type")
			}
		case methodResourcesRead:
			entry.CompleteType = types["ResourcesReadCompleteResult"]
		case methodPromptsGet:
			entry.CompleteType = types["PromptsGetCompleteResult"]
		}
		if endpoint.Method.Name == methodToolsCall || endpoint.Method.Name == methodResourcesRead || endpoint.Method.Name == methodPromptsGet {
			if entry.CompleteType == nil {
				return nil, fmt.Errorf("basic MCP method %q has no planned completed body", endpoint.Method.Name)
			}
		}
		result.Bodies = append(result.Bodies, entry)
	}
	return result, nil
}

// mcpHTTPService selects the exact attached JSON-RPC expression for Goa's plan.
func mcpHTTPService(prepared *preparedMCPService) (*expr.HTTPServiceExpr, error) {
	for _, service := range prepared.root.API.JSONRPC.Services {
		if service.Name() == prepared.mcpService.Name {
			return service, nil
		}
	}
	return nil, fmt.Errorf("MCP service %q has no attached JSON-RPC expression", prepared.mcpService.Name)
}
