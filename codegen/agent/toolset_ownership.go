package codegen

import (
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"goa.design/goa-ai/codegen/ir"
	"goa.design/goa-ai/codegen/naming"
	goacodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
)

// toolsetOwner describes the computed, toolset-owner-scoped output locations
// used by generators to keep tool specs/codecs emitted once per defining toolset.
type toolsetOwner struct {
	specsDir        string
	specsImportPath string

	agentToolsImportPath string
	agentToolsPackage    string
}

// assignToolsetOwnership computes the canonical ownership anchor for each
// defining toolset in the design and wires the resulting output locations into
// all generated agent toolsets.
//
// Ownership is derived from the deterministic IR built from the evaluated Goa
// roots. The function then mutates every referenced `*ToolsetData` in `services`
// to set:
//   - SpecsDir/SpecsImportPath: where the toolset-owned specs/codecs live, and
//   - AgentToolsImportPath/AgentToolsPackage: provider-side agenttools helpers
//     used by consumers of agent-exported toolsets.
//
// For referenced (non-defining) toolsets, ownership is resolved via the defining
// toolset name (Origin) so that all aliases/uses converge on the same owner.
//
// This function is deliberately fail-fast: missing ownership assignments or
// unknown owner kinds are generator bugs (or invalid designs) and must surface
// as errors rather than silently producing partial output.
func assignToolsetOwnership(genpkg string, roots []eval.Root, services []*ServiceAgentsData) error {
	design, err := ir.Build(genpkg, roots)
	if err != nil {
		return err
	}
	owners := make(map[string]ir.Owner, len(design.Toolsets))
	for _, ts := range design.Toolsets {
		owners[ts.Name] = ts.Owner
	}

	// Precompute owners to avoid per-toolset recomputation and to keep the assignment
	// logic symmetric across Used/Exported toolsets.
	ownerInfo := make(map[string]toolsetOwner, len(owners))
	for name, owner := range owners {
		switch owner.Kind {
		case ir.OwnerKindService:
			ownerInfo[name] = toolsetOwner{
				specsDir:        filepath.Join(goacodegen.Gendir, owner.ServicePathName, "toolsets", naming.SanitizeToken(name, "toolset")),
				specsImportPath: path.Join(genpkg, owner.ServicePathName, "toolsets", naming.SanitizeToken(name, "toolset")),
			}
		case ir.OwnerKindAgentExport:
			tsSlug := naming.SanitizeToken(name, "toolset")
			ownerInfo[name] = toolsetOwner{
				specsDir:        filepath.Join(goacodegen.Gendir, owner.ServicePathName, "agents", owner.AgentSlug, "exports", tsSlug),
				specsImportPath: path.Join(genpkg, owner.ServicePathName, "agents", owner.AgentSlug, "exports", tsSlug),
				// Provider-side agenttools helpers for the agent-as-tool pattern.
				agentToolsImportPath: path.Join(genpkg, owner.ServicePathName, "agents", owner.AgentSlug, "agenttools", tsSlug),
				agentToolsPackage:    tsSlug,
			}
		default:
			return fmt.Errorf("unknown toolset owner kind %q for toolset %q", owner.Kind, name)
		}
	}

	for _, svc := range services {
		for _, ag := range svc.Agents {
			all := make([]*ToolsetData, 0, len(ag.UsedToolsets)+len(ag.ExportedToolsets))
			all = append(all, ag.UsedToolsets...)
			all = append(all, ag.ExportedToolsets...)
			slices.SortFunc(all, func(a, b *ToolsetData) int {
				if d := strings.Compare(string(a.Kind), string(b.Kind)); d != 0 {
					return d
				}
				return strings.Compare(a.Name, b.Name)
			})
			for _, ts := range all {
				defName := ts.Name
				if ts.Expr != nil && ts.Expr.Origin != nil && ts.Expr.Origin.Name != "" {
					defName = ts.Expr.Origin.Name
				}
				own, ok := ownerInfo[defName]
				if !ok {
					return fmt.Errorf("toolset %q has no ownership assignment", defName)
				}
				ts.SpecsDir = own.specsDir
				ts.SpecsImportPath = own.specsImportPath
				// For consumers of agent-exported toolsets, wire provider-side agenttools helpers.
				if ts.Kind == ToolsetKindUsed && own.agentToolsImportPath != "" {
					ts.AgentToolsImportPath = own.agentToolsImportPath
					ts.AgentToolsPackage = own.agentToolsPackage
				}
			}
		}
	}
	return nil
}


