package codegen

import (
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"

	agentsExpr "goa.design/goa-ai/expr/agents"
	gcodegen "goa.design/goa/v3/codegen"
)

// Prepare ensures that any external user types referenced by agent tool shapes
// (including method-backed tools) are present in the Goa root and marked for
// generation. This allows core Goa codegen to emit the corresponding Go types
// in their intended packages when only referenced indirectly by agent specs.
//
// The function is intentionally conservative: it walks tool Args/Return and, if
// available, bound method payload/result attributes to collect all referenced
// user types. For each user type, if it is not already part of goaexpr.Root.Types,
// it is appended and marked with the "type:generate:force" meta so core codegen
// generates it even when not directly used by a service method payload/result.
//
//nolint:unparam // result is always nil by design; Prepare should not fail
func Prepare(_ string, _ []eval.Root) error {
	if agentsExpr.Root == nil {
		return nil
	}
	// Build quick lookups of existing user type IDs/names to avoid duplicates.
	existingByID := make(map[string]struct{})
	existingByName := make(map[string]struct{})
	for _, ut := range goaexpr.Root.Types {
		existingByID[ut.ID()] = struct{}{}
		existingByName[ut.Name()] = struct{}{}
	}
	for _, a := range agentsExpr.Root.Agents {
		// Collect toolsets from both Used and Exported groups.
		var toolsets []*agentsExpr.ToolsetExpr
		if a.Used != nil {
			toolsets = append(toolsets, a.Used.Toolsets...)
		}
		if a.Exported != nil {
			toolsets = append(toolsets, a.Exported.Toolsets...)
		}
		for _, ts := range toolsets {
			for _, t := range ts.Tools {
				// Walk Args and Return shapes only. Goa will generate method
				// payloads and results as part of service generation.
				collectAndForceTypes(t.Args, existingByID, existingByName)
				collectAndForceTypes(t.Return, existingByID, existingByName)
			}
		}
	}
	return nil
}

// collectAndForceTypes walks the attribute recursively and ensures any
// encountered user types are marked with the "type:generate:force" meta and
// present in goaexpr.Root.Types. The walk recurses into user type attributes
// as well (including alias bases and extended bases) using a visited set.
func collectAndForceTypes(att *goaexpr.AttributeExpr, existingByID, existingByName map[string]struct{}) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return
	}
	visited := make(map[string]struct{})
	var walkUT func(ut goaexpr.UserType)
	walkUT = func(ut goaexpr.UserType) {
		if ut == nil {
			return
		}
		if _, seen := visited[ut.ID()]; seen {
			return
		}
		visited[ut.ID()] = struct{}{}
		// Mark for generation across services. Preserve any existing meta.
		ut.Attribute().AddMeta("type:generate:force")
		if _, ok := existingByID[ut.ID()]; !ok {
			goaexpr.Root.Types = append(goaexpr.Root.Types, ut)
			existingByID[ut.ID()] = struct{}{}
			existingByName[ut.Name()] = struct{}{}
		}
		// Recurse into the user type attribute to catch nested user types,
		// alias bases, and extended bases.
		_ = gcodegen.Walk(ut.Attribute(), func(a *goaexpr.AttributeExpr) error {
			if a == nil || a.Type == nil || a.Type == goaexpr.Empty {
				return nil
			}
			if nut, ok := a.Type.(goaexpr.UserType); ok && nut != nil {
				walkUT(nut)
			}
			return nil
		})
	}

	_ = gcodegen.Walk(att, func(a *goaexpr.AttributeExpr) error {
		if a == nil || a.Type == nil || a.Type == goaexpr.Empty {
			return nil
		}
		if ut, ok := a.Type.(goaexpr.UserType); ok && ut != nil {
			walkUT(ut)
		}
		return nil
	})
}
