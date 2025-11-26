package codegen

import (
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"

	agentsExpr "goa.design/goa-ai/expr/agent"
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
				// Prepare the tool expression (inheritance from method)
				if t.Method != nil {
					if t.Args.Type == goaexpr.Empty {
						t.Args = goaexpr.DupAtt(t.Method.Payload)
					}
					if t.Return.Type == goaexpr.Empty {
						t.Return = goaexpr.DupAtt(t.Method.Result)
					}
				}

				// Handle injected fields by hiding them from JSON (LLM view)
				if len(t.InjectedFields) > 0 {
					t.Args = flattenAndHide(t.Args, t.InjectedFields)
				}

				// Walk Args and Return shapes only. Goa will generate method
				// payloads and results as part of service generation.
				collectAndForceTypes(t.Args, existingByID, existingByName)
				collectAndForceTypes(t.Return, existingByID, existingByName)
			}
		}
	}
	return nil
}

// flattenAndHide creates a deep copy of the attribute, converting UserTypes to inline Objects
// where necessary to modify field metadata (hiding injected fields).
func flattenAndHide(att *goaexpr.AttributeExpr, injected []string) *goaexpr.AttributeExpr {
	if att == nil {
		return nil
	}
	newAtt := goaexpr.DupAtt(att)

	// If it's a UserType, we must unwrap it to modify fields without side effects on the original type.
	if ut, ok := newAtt.Type.(goaexpr.UserType); ok {
		// Extract the underlying attribute from the UserType and dup it.
		// We lose the named type wrapping, effectively flattening it to an inline definition.
		inner := goaexpr.DupAtt(ut.Attribute())
		newAtt.Type = inner.Type
		// Merge validation if needed (inner validation is the effective one for the fields)
		if newAtt.Validation == nil {
			newAtt.Validation = inner.Validation
		}
		// Note: we discard the UserType wrapper, so the resulting attribute is anonymous (inline).
	}

	// If it's an Object, iterate fields and apply hiding
	if obj, ok := newAtt.Type.(*goaexpr.Object); ok {
		for _, fieldName := range injected {
			for _, namedAtt := range *obj {
				if namedAtt.Name == fieldName {
					fieldAtt := namedAtt.Attribute
					// Hide from JSON (LLM)
					if fieldAtt.Meta == nil {
						fieldAtt.Meta = make(goaexpr.MetaExpr)
					}
					fieldAtt.Meta["struct:tag:json"] = []string{"-"}

					// Remove from Required list if present on the parent object.
					if newAtt.Validation != nil {
						required := newAtt.Validation.Required
						for i, r := range required {
							if r == fieldName {
								required = append(required[:i], required[i+1:]...)
								newAtt.Validation.Required = required
								break
							}
						}
					}
					break
				}
			}
		}
	}

	return newAtt
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
