// Package codegen resolves consumer tool selections for generated agent contracts.
// Catalogs, executable names, loading choices, and embedded child definitions use
// the same selected data. Provider registration and executors keep complete data.
package codegen

import (
	"fmt"
	"slices"
	"strings"
)

// selectedAgentData returns an agent copy whose consumed tools match its Use
// declarations. Exported specifications remain complete, including when the same
// agent consumes a subset of an external MCP toolset it also exports.
// The original provider data is unchanged.
func selectedAgentData(complete *AgentData) (*AgentData, error) {
	selected := *complete
	selected.UsedToolsets = nil
	selected.AllToolsets = nil
	selected.Tools = nil
	exports := make(map[string]bool, len(complete.ExportedToolsets))
	for _, toolset := range complete.ExportedToolsets {
		exports[toolset.SpecsImportPath] = true
	}
	for _, toolset := range complete.UsedToolsets {
		consumed, err := selectedAgentToolset(toolset)
		if err != nil {
			return nil, err
		}
		selected.UsedToolsets = append(selected.UsedToolsets, consumed)
		catalog := consumed
		if exports[consumed.SpecsImportPath] {
			// This reference also supplies exported specifications. Keep its
			// complete catalog, route and metadata; only execution is narrowed.
			catalog = toolset
		}
		selected.AllToolsets = append(selected.AllToolsets, catalog)
	}
	selected.AllToolsets = append(selected.AllToolsets, complete.ExportedToolsets...)
	for _, toolset := range selected.AllToolsets {
		selected.Tools = append(selected.Tools, toolset.Tools...)
	}
	return &selected, nil
}

// selectedAgentToolset resolves the consumer's tool names against the completed
// provider catalog and returns the data advertised by this agent. The original
// data remains complete for shared schemas, registration, and executors.
func selectedAgentToolset(complete *ToolsetData) (*ToolsetData, error) {
	if complete.Kind != ToolsetKindUsed || complete.Expr.Origin == nil || len(complete.Expr.Tools) == 0 {
		return complete, nil
	}
	available := make(map[string]*ToolData, len(complete.Tools))
	for _, tool := range complete.Tools {
		available[tool.Name] = tool
	}
	selected := *complete
	selected.Tools = nil
	seen := make(map[string]bool, len(complete.Expr.Tools))
	for _, name := range complete.Expr.Tools {
		tool, ok := available[name.Name]
		if !ok {
			return nil, fmt.Errorf("toolset %q: Use selects unknown tool %q", complete.Name, name.Name)
		}
		if seen[name.Name] {
			return nil, fmt.Errorf("toolset %q: Use selects duplicate tool %q", complete.Name, name.Name)
		}
		seen[name.Name] = true
		selected.Tools = append(selected.Tools, tool)
	}
	slices.SortFunc(selected.Tools, func(a, b *ToolData) int {
		return strings.Compare(a.Name, b.Name)
	})
	selected.RequiredLabels = requiredLabels(selected.Tools)
	return &selected, nil
}
