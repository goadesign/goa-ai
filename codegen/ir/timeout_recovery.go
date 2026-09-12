package ir

// Timeout recovery belongs to the provider's tool definition. Validate every
// resolved route before generating code: only agent children carry this policy,
// and consumers must not replace the provider's promise with their own choice.

import "fmt"

func validateTimeoutRecoveryRoutes(agents []*Agent, serviceExports []*ToolsetRef) error {
	refs := append([]*ToolsetRef(nil), serviceExports...)
	for _, agent := range agents {
		refs = append(refs, agent.ExportedToolsets...)
		refs = append(refs, agent.UsedToolsets...)
	}
	for _, ref := range refs {
		declared := make(map[string]bool, len(ref.Definition.Expr.Tools))
		for _, tool := range ref.Definition.Expr.Tools {
			declared[tool.Name] = tool.ReplanOnTimeout
			if !tool.ReplanOnTimeout {
				continue
			}
			if (ref.Kind != ToolsetRefKindExported && ref.SourceExport == nil) ||
				ref.Provider != nil || len(ref.Expr.PublishTo) > 0 {
				return fmt.Errorf("tool %q ReplanOnTimeout requires a generated agent export and agent-as-tool consumers; direct, MCP, registry, and PublishTo routes are unsupported", ref.Name+"."+tool.Name)
			}
		}
		for _, tool := range ref.Expr.Tools {
			if tool.ReplanOnTimeout != declared[tool.Name] {
				return fmt.Errorf("tool %q ReplanOnTimeout must be declared on the provider's tool definition, not a consumer or export overlay", ref.Name+"."+tool.Name)
			}
		}
	}
	return nil
}
