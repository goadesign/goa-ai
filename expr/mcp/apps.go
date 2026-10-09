// Package mcp records which callers may use a tool and which HTML resource
// presents its result. Existing resource methods still own contents and access;
// these declarations become tool catalog metadata and generated model choices.
package mcp

import (
	"net/url"
	"strings"

	"goa.design/goa/v3/eval"
)

type (
	// ToolVisibility selects the model, an embedded app, or both as tool callers.
	ToolVisibility uint8
)

const (
	// ModelAndAppVisibility permits both callers, as required by MCP's default.
	ModelAndAppVisibility ToolVisibility = iota
	// ModelVisibility excludes calls initiated by an embedded app.
	ModelVisibility
	// AppVisibility excludes the tool from model catalogs and model execution.
	AppVisibility
)

// validateToolUI rejects undeclared resources and conflicting fixed MIME types.
// A typed URI reader may serve a runtime resource; the host checks the returned
// HTML MIME type and browser policy before displaying its contents.
func (m *MCPExpr) validateToolUI(tool *ToolExpr, errors *eval.ValidationErrors) {
	if tool.UIResourceURI == "" {
		return
	}
	for _, resource := range m.Resources {
		if resource.URI != tool.UIResourceURI {
			continue
		}
		if resource.MimeType != "text/html;profile=mcp-app" {
			errors.Add(tool, "ToolUI(%q) must reference a resource with MIME type text/html;profile=mcp-app", tool.UIResourceURI)
		}
		return
	}
	if m.ResourceReader == nil {
		errors.Add(tool, "ToolUI(%q) requires a declared resource or ResourceReader in the same MCP service", tool.UIResourceURI)
	}
}

// validateAppDeclarations checks authored UI addresses and caller choices before
// generation. Unknown visibility values or non-UI addresses are design errors.
func (t *ToolExpr) validateAppDeclarations(errors *eval.ValidationErrors) {
	switch t.Visibility {
	case ModelAndAppVisibility, ModelVisibility, AppVisibility:
	default:
		errors.Add(t, "tool visibility must permit the model, app, or both")
	}
	if t.UIResourceURI == "" {
		return
	}
	address, err := url.Parse(t.UIResourceURI)
	if err != nil || !strings.HasPrefix(t.UIResourceURI, "ui://") || address.Scheme != "ui" || address.Host == "" && address.Path == "" && address.Opaque == "" {
		errors.Add(t, "ToolUI URI must be an absolute ui:// address")
	}
}
