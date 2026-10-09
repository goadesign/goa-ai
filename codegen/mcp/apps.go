// Package codegen writes Apps metadata from evaluated tool declarations.
// Static UI addresses and caller permissions are encoded once during generation;
// catalog pages and model-facing callers consume the same authored facts.
package codegen

import (
	"encoding/json"

	mcpexpr "goa.design/goa-ai/expr/mcp"
)

// toolUIMetadata encodes the current nested UI metadata namespace. Ordinary
// tools omit metadata, and no deprecated flat UI resource key is emitted.
func toolUIMetadata(tool *mcpexpr.ToolExpr) (string, error) {
	if tool.UIResourceURI == "" && tool.Visibility == mcpexpr.ModelAndAppVisibility {
		return "", nil
	}
	metadata := struct {
		UI struct {
			ResourceURI string   `json:"resourceUri,omitempty"` //nolint:tagliatelle // MCP Apps defines this field.
			Visibility  []string `json:"visibility,omitempty"`
		} `json:"ui"`
	}{}
	metadata.UI.ResourceURI = tool.UIResourceURI
	switch tool.Visibility {
	case mcpexpr.ModelAndAppVisibility:
		// Both callers are permitted when visibility is omitted from the wire.
		break
	case mcpexpr.ModelVisibility:
		metadata.UI.Visibility = []string{"model"}
	case mcpexpr.AppVisibility:
		metadata.UI.Visibility = []string{"app"}
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// serverExtensionMetadata encodes only extensions supported by the authored
// operations. Discovery returns these declarations without a client handshake.
func serverExtensionMetadata(data *AdapterData) (string, error) {
	extensions := make(map[string]any)
	if len(data.Tasks) > 0 {
		extensions["io.modelcontextprotocol/tasks"] = struct{}{}
	}
	if data.SkillCatalog != nil && data.SkillLookup != nil {
		extensions["io.modelcontextprotocol/skills"] = struct {
			DirectoryRead bool `json:"directoryRead,omitempty"` //nolint:tagliatelle // The Skills extension defines this field.
		}{DirectoryRead: data.ResourceDirectory != nil}
	}
	for _, tool := range data.Tools {
		if tool.UIMetadata != "" {
			extensions["io.modelcontextprotocol/ui"] = struct {
				MIMETypes []string `json:"mimeTypes"` //nolint:tagliatelle // MCP Apps defines this field.
			}{MIMETypes: []string{"text/html;profile=mcp-app"}}
			break
		}
	}
	if len(extensions) == 0 {
		return "", nil
	}
	encoded, err := json.Marshal(extensions)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
