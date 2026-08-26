// Package codegen prepares agent contract shapes and adds externally generated
// user types to the Goa design before Goa chooses packages and names.
package codegen

import (
	"fmt"

	goacodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"

	agentsExpr "goa.design/goa-ai/expr/agent"
)

type (
	// contractTypeCollector marks design types that Goa must generate before
	// agent packages can reference them. When locatedOnly is true, unlocated
	// types remain owned by the generated agent package.
	contractTypeCollector struct {
		root        *goaexpr.RootExpr
		designTypes map[goaexpr.UserType]struct{}
		rootTypes   map[goaexpr.UserType]struct{}
		visited     map[goaexpr.UserType]struct{}
		locatedOnly bool
	}
)

// Prepare ensures that external user types referenced only by agent contracts
// are present in the Goa root and marked for generation. This allows Goa to
// claim their requested packages before agent packages plan references to them.
//
// Tool inputs and results may be used outside Goa service methods, so every
// design type they reach is generated. Direct completions already generate
// unlocated types in their own package; preparation only claims types with an
// explicit struct:pkg:path and leaves all other completion types local.
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
	// This set never changes during collection. It distinguishes types already
	// present in the evaluated Goa design from wrappers Goa creates for primitive
	// OneOf branches.
	designTypes := make(map[goaexpr.UserType]struct{}, len(goaRoot.Types)+len(goaRoot.ResultTypes)+1)
	// The root-type set tracks the mutable slice that receives types forced solely by
	// agent contracts. Result types are appended there only when they are forced.
	rootTypes := make(map[goaexpr.UserType]struct{}, len(goaRoot.Types))
	for _, ut := range goaRoot.Types {
		designTypes[ut.Origin()] = struct{}{}
		rootTypes[ut.Origin()] = struct{}{}
	}
	for _, rt := range goaRoot.ResultTypes {
		designTypes[rt.Origin()] = struct{}{}
	}
	designTypes[goaexpr.ErrorResult.Origin()] = struct{}{}
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
			collectAndForceTypes(goaRoot, t.Args, designTypes, rootTypes, false)
			collectAndForceTypes(goaRoot, t.Return, designTypes, rootTypes, false)
		}
	}
	for _, completion := range agentRoot.Completions {
		collectAndForceTypes(goaRoot, completion.Return, designTypes, rootTypes, true)
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

// collectAndForceTypes marks design types reachable from one agent contract.
// locatedOnly keeps completion-owned types in the completion package while
// still finding explicitly located types nested inside them.
func collectAndForceTypes(
	root *goaexpr.RootExpr,
	att *goaexpr.AttributeExpr,
	designTypes, rootTypes map[goaexpr.UserType]struct{},
	locatedOnly bool,
) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return
	}
	collector := &contractTypeCollector{
		root:        root,
		designTypes: designTypes,
		rootTypes:   rootTypes,
		visited:     make(map[goaexpr.UserType]struct{}),
		locatedOnly: locatedOnly,
	}
	collector.walk(att, false)
}

// markTypeForGeneration asks Goa to emit a type without adding private agent
// schemas to service OpenAPI documents. HTTP endpoints still include the type.
func markTypeForGeneration(ut goaexpr.UserType) {
	attr := ut.Attribute()
	attr.AddMeta("type:generate:force")
	if _, ok := attr.Meta["openapi:generate"]; !ok {
		attr.AddMeta("openapi:generate", "false")
	}
}

// walk follows one agent contract value. unionBranch is true only for the
// immediate type Goa placed around a OneOf branch that was primitive in the
// design.
func (c *contractTypeCollector) walk(att *goaexpr.AttributeExpr, unionBranch bool) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return
	}
	for _, dataType := range append(append([]goaexpr.DataType{}, att.Bases...), att.References...) {
		if userType, ok := dataType.(goaexpr.UserType); ok {
			c.walkUserType(userType)
		}
	}

	switch actual := att.Type.(type) {
	case goaexpr.UserType:
		if unionBranch {
			if _, declared := c.designTypes[actual.Origin()]; !declared {
				c.walk(actual.Attribute(), false)
				return
			}
		}
		c.walkUserType(actual)
	case *goaexpr.Object:
		for _, field := range *actual {
			c.walk(field.Attribute, false)
		}
	case *goaexpr.Array:
		c.walk(actual.ElemType, false)
	case *goaexpr.Map:
		c.walk(actual.KeyType, false)
		c.walk(actual.ElemType, false)
	case *goaexpr.Union:
		for _, branch := range actual.Values {
			c.walk(branch.Attribute, true)
		}
	}
}

// walkUserType follows one design type and forces it when the active contract
// requires Goa to own its generated declaration. The original declaration
// identifies the same type across Goa copies.
func (c *contractTypeCollector) walkUserType(userType goaexpr.UserType) {
	origin := userType.Origin()
	if _, seen := c.visited[origin]; seen {
		return
	}
	c.visited[origin] = struct{}{}
	if !c.locatedOnly || goacodegen.UserTypeLocation(userType) != nil {
		markTypeForGeneration(userType)
		if _, exists := c.rootTypes[origin]; !exists {
			c.root.Types = append(c.root.Types, userType)
			c.rootTypes[origin] = struct{}{}
		}
	}
	c.walk(userType.Attribute(), false)
}
