// Package codegen prepares tool shapes and adds tool-only user types to the Goa
// design before Goa chooses generated names.
package codegen

import (
	"fmt"

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
//
// This function must not synthesize additional user types (for example, union
// branch aliases). Goa's generators already emit all required union helpers when
// the containing user types are forced for generation; injecting synthetic alias
// user types can create duplicate names and broken references across packages.
func Prepare(_ string, roots []eval.Root) error {
	goaRoot, agentRoot := agentDesignRoots(roots)
	if agentRoot == nil {
		return nil
	}
	if goaRoot == nil {
		return fmt.Errorf("agent design requires a Goa service design")
	}
	// Build quick lookups of existing user type IDs/names to avoid duplicates.
	existingByID := make(map[string]struct{})
	existingByName := make(map[string]struct{})
	for _, ut := range goaRoot.Types {
		existingByID[ut.ID()] = struct{}{}
		existingByName[ut.Name()] = struct{}{}
	}
	for _, ts := range agentRoot.DefiningToolsets() {
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

			// Walk Args and Return shapes only. Goa will generate method
			// payloads and results as part of service generation.
			if err := collectAndForceTypes(goaRoot, t.Args, existingByID, existingByName); err != nil {
				return err
			}
			if err := collectAndForceTypes(goaRoot, t.Return, existingByID, existingByName); err != nil {
				return err
			}
		}
	}
	return nil
}

// agentDesignRoots returns the Goa and agent designs supplied to one plugin run.
func agentDesignRoots(roots []eval.Root) (*goaexpr.RootExpr, *agentsExpr.RootExpr) {
	var goaRoot *goaexpr.RootExpr
	var agentRoot *agentsExpr.RootExpr
	for _, root := range roots {
		switch actual := root.(type) {
		case *goaexpr.RootExpr:
			goaRoot = actual
		case *agentsExpr.RootExpr:
			agentRoot = actual
		}
	}
	return goaRoot, agentRoot
}

// modelTransportShape copies a tool input and marks fields supplied by the
// runtime so model JSON cannot contain them.
func modelTransportShape(att *goaexpr.AttributeExpr, hidden []string) *goaexpr.AttributeExpr {
	if att == nil {
		return nil
	}
	newAtt := goaexpr.DupAtt(att)

	if ut, ok := newAtt.Type.(goaexpr.UserType); ok {
		inner := goaexpr.DupAtt(ut.Attribute())
		if len(newAtt.UserExamples) == 0 {
			newAtt.UserExamples = inner.ExtractUserExamples()
		}
		newAtt.Type = inner.Type
		if newAtt.Validation == nil {
			newAtt.Validation = inner.Validation
		}
	}

	obj := goaexpr.AsObject(newAtt.Type)
	for _, fieldName := range hidden {
		field := obj.Attribute(fieldName)
		if field == nil {
			panic(fmt.Sprintf("model-hidden field %q is missing from the tool input", fieldName))
		}
		if field.Meta == nil {
			field.Meta = make(goaexpr.MetaExpr)
		}
		field.Meta["struct:tag:json"] = []string{"-"}
		if newAtt.Validation != nil {
			newAtt.Validation.RemoveRequired(fieldName)
		}
	}

	return newAtt
}

// collectAndForceTypes walks the attribute recursively and ensures any
// encountered user types are marked for Go generation and present in
// goaexpr.Root.Types. The walk recurses into user type attributes as well
// (including alias bases and extended bases) using a visited set.
func collectAndForceTypes(root *goaexpr.RootExpr, att *goaexpr.AttributeExpr, existingByID, existingByName map[string]struct{}) error {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return nil
	}
	visited := make(map[string]struct{})
	var walkUT func(ut goaexpr.UserType) error
	walkUT = func(ut goaexpr.UserType) error {
		if ut == nil {
			return nil
		}
		if _, seen := visited[ut.ID()]; seen {
			return nil
		}
		visited[ut.ID()] = struct{}{}

		markToolTypeForGeneration(ut)
		if _, ok := existingByID[ut.ID()]; !ok {
			root.Types = append(root.Types, ut)
			existingByID[ut.ID()] = struct{}{}
			existingByName[ut.Name()] = struct{}{}
		}

		// Recurse into the user type attribute to catch nested user types as well as
		// dependencies captured via attribute bases/references and union branches.
		if err := gcodegen.Walk(ut.Attribute(), func(a *goaexpr.AttributeExpr) error {
			if a == nil || a.Type == nil || a.Type == goaexpr.Empty {
				return nil
			}
			return walkAttributeDependencyTypes(a, walkUT)
		}); err != nil {
			return err
		}

		return nil
	}

	if err := gcodegen.Walk(att, func(a *goaexpr.AttributeExpr) error {
		if a == nil || a.Type == nil || a.Type == goaexpr.Empty {
			return nil
		}
		return walkAttributeDependencyTypes(a, walkUT)
	}); err != nil {
		return err
	}

	return nil
}

// markToolTypeForGeneration preserves Goa type generation for tool-only shapes
// without forcing private agent schemas into service OpenAPI documents. Goa
// still includes the type in OpenAPI when it is reached from an HTTP endpoint.
func markToolTypeForGeneration(ut goaexpr.UserType) {
	attr := ut.Attribute()
	attr.AddMeta("type:generate:force")
	if _, ok := attr.Meta["openapi:generate"]; !ok {
		attr.AddMeta("openapi:generate", "false")
	}
}

func walkAttributeDependencyTypes(att *goaexpr.AttributeExpr, walkUT func(goaexpr.UserType) error) error {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return nil
	}

	// Primary type reference.
	if ut, ok := att.Type.(goaexpr.UserType); ok && ut != nil {
		if err := walkUT(ut); err != nil {
			return err
		}
	}

	// Bases and references may carry user types even when att.Type is a primitive.
	for _, dt := range att.Bases {
		ut, ok := dt.(goaexpr.UserType)
		if !ok || ut == nil {
			continue
		}
		if err := walkUT(ut); err != nil {
			return err
		}
	}
	for _, dt := range att.References {
		ut, ok := dt.(goaexpr.UserType)
		if !ok || ut == nil {
			continue
		}
		if err := walkUT(ut); err != nil {
			return err
		}
	}

	return nil
}
