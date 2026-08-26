// Package codegen pairs each tool's DSL meaning with the final Go names selected for
// its generated package. Templates read behavior from Tool and names from Spec.
package codegen

import "fmt"

type (
	// toolRenderData gives a template the semantic tool and its generated names
	// without copying names between the two records.
	toolRenderData struct {
		Tool       *ToolData
		Spec       *toolEntry
		ServerData []*serverDataRenderData
	}

	// serverDataRenderData pairs one declared server result with the generated
	// type, codec, and transform written for that result.
	serverDataRenderData struct {
		Data *ServerDataData
		Spec *serverDataEntry
	}
)

// buildToolRenderData pairs tools by their complete runtime name and returns
// an error before rendering when a generated entry is missing.
func buildToolRenderData(toolset *ToolsetData) ([]*toolRenderData, error) {
	entries := make(map[string]*toolEntry, len(toolset.specs.tools))
	for _, entry := range toolset.specs.tools {
		entries[entry.Name] = entry
	}

	tools := make([]*toolRenderData, 0, len(toolset.Tools))
	for _, tool := range toolset.Tools {
		entry := entries[tool.QualifiedName]
		if entry == nil {
			return nil, fmt.Errorf(
				"agent codegen: generated names for tool %q are missing from toolset %q",
				tool.QualifiedName,
				toolset.QualifiedName,
			)
		}
		serverData, err := buildServerDataRenderData(toolset, tool, entry)
		if err != nil {
			return nil, err
		}
		tools = append(tools, &toolRenderData{
			Tool:       tool,
			Spec:       entry,
			ServerData: serverData,
		})
	}
	return tools, nil
}

// buildServerDataRenderData pairs each declared server result with the type
// and functions selected for it in the generated tool package.
func buildServerDataRenderData(toolset *ToolsetData, tool *ToolData, entry *toolEntry) ([]*serverDataRenderData, error) {
	entries := make(map[string]*serverDataEntry, len(entry.ServerData))
	for _, data := range entry.ServerData {
		entries[data.Kind] = data
	}

	serverData := make([]*serverDataRenderData, 0, len(tool.ServerData))
	for _, data := range tool.ServerData {
		generated := entries[data.Kind]
		if generated == nil {
			return nil, fmt.Errorf(
				"agent codegen: generated names for server data %q of tool %q are missing from toolset %q",
				data.Kind,
				tool.QualifiedName,
				toolset.QualifiedName,
			)
		}
		serverData = append(serverData, &serverDataRenderData{Data: data, Spec: generated})
	}
	return serverData, nil
}
