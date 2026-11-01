// Package agent defines expression types used to represent prompts alongside
// agents and toolsets during Goa design evaluation.
//
// PromptExpr captures prompt declarations configured via Goa DSL on agents and
// services. Compile-time validation enforces non-empty names, baseline templates,
// valid prompt roles, template syntax correctness, object Args shapes, and alignment
// between template dot-field references and declared exported Go struct fields.
package agent

import (
	"fmt"
	"reflect"
	"text/template"

	"goa.design/goa-ai/internal/tmplcheck"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// PromptExpr describes one prompt template declared on an agent or service.
	//
	// An agent-scoped prompt receives the canonical identifier `<service>.<agent>.<name>`,
	// while a service-scoped prompt receives `<service>.<name>`. Compile-time
	// validation ensures that template syntax compiles with missingkey=error, and
	// that any top-level dot fields referenced in the template exist on the declared
	// Args contract.
	PromptExpr struct {
		eval.DSLFunc

		// Name is the authored prompt identifier.
		Name string
		// Description provides a human-readable explanation of what the prompt does.
		Description string
		// Role classifies how the prompt is used (system, user, tool, or synthesis).
		Role prompt.PromptRole
		// Template contains the baseline Go text/template source.
		Template string
		// Args defines the typed input data schema passed to the template during rendering.
		Args *goaexpr.AttributeExpr
		// Service is the Goa service expression that owns or encloses this prompt.
		Service *goaexpr.ServiceExpr
		// Agent is the enclosing agent expression, or nil for service-level prompts.
		Agent *AgentExpr
	}

	// exprAttrType adapts a Goa AttributeExpr into tmplcheck.Type for static template checking.
	exprAttrType struct {
		att *goaexpr.AttributeExpr
	}
)

// EvalName returns the descriptive name of the prompt expression for DSL errors.
func (p *PromptExpr) EvalName() string {
	if p.Agent != nil && p.Service != nil {
		return fmt.Sprintf("prompt %q (agent %q, service %q)", p.Name, p.Agent.Name, p.Service.Name)
	}
	if p.Service != nil {
		return fmt.Sprintf("prompt %q (service %q)", p.Name, p.Service.Name)
	}
	return fmt.Sprintf("prompt %q", p.Name)
}

// ID returns the canonical globally unique prompt identifier.
func (p *PromptExpr) ID() prompt.Ident {
	svcName := ""
	if p.Service != nil {
		svcName = p.Service.Name
	}
	if p.Agent != nil {
		return prompt.Ident(svcName + "." + p.Agent.Name + "." + p.Name)
	}
	return prompt.Ident(svcName + "." + p.Name)
}

// Version returns the deterministic sha256 version derived from the prompt template.
func (p *PromptExpr) Version() string {
	return prompt.VersionFromTemplate(p.Template)
}

// SetDescription implements goa.design/goa/v3/expr.DescriptionHolder so the Goa
// Description DSL helper can be used inside Prompt configuration blocks.
func (p *PromptExpr) SetDescription(d string) {
	p.Description = d
}

// Prepare ensures prompt defaults and shapes are initialized before validation.
func (p *PromptExpr) Prepare() {
	if p.Role == "" {
		if p.Name == "system" {
			p.Role = prompt.PromptRoleSystem
		} else {
			p.Role = prompt.PromptRoleUser
		}
	}
	finalizeToolShape(p.Args)
}

// Finalize ensures prompt attribute shapes are finalized before code generation.
func (p *PromptExpr) Finalize() {
	finalizeToolShape(p.Args)
}

// Validate verifies prompt invariants: non-empty name and template, valid role,
// valid Go template syntax, valid object Args shape, and field access alignment
// using static template checking.
func (p *PromptExpr) Validate() error {
	verr := new(eval.ValidationErrors)
	if p.Name == "" {
		verr.Add(p, "prompt name cannot be empty")
	}
	if p.Template == "" {
		verr.Add(p, "prompt template cannot be empty")
	}

	switch p.Role {
	case prompt.PromptRoleSystem, prompt.PromptRoleUser, prompt.PromptRoleTool, prompt.PromptRoleSynthesis:
		// Valid role.
	default:
		verr.Add(p, "prompt role %q must be one of \"system\", \"user\", \"tool\", or \"synthesis\"", p.Role)
	}

	hasArgs := p.Args != nil && p.Args.Type != nil && p.Args.Type != goaexpr.Empty
	if hasArgs {
		validateContractShape(p, "Args", p.Args, verr)
		if goaexpr.AsObject(p.Args.Type) == nil {
			verr.Add(p, "Prompt Args must be an object or a user type backed by an object")
		}
	}

	funcs := template.FuncMap{
		"render": func(string, ...any) (string, error) { return "", nil },
	}
	parsed, err := template.New(string(p.ID())).Option("missingkey=error").Funcs(funcs).Parse(p.Template)
	if err != nil {
		verr.Add(p, "invalid prompt template syntax: %v", err)
		if len(verr.Errors) == 0 {
			return nil
		}
		return verr
	}

	var rootType tmplcheck.Type
	if hasArgs {
		rootType = newExprAttrType(p.Args)
	} else {
		rootType = tmplcheck.ReflectType(reflect.TypeFor[struct{}]())
	}

	if err := tmplcheck.Check(parsed, string(p.ID()), rootType, funcs); err != nil {
		verr.Add(p, "%s", err)
	}

	if len(verr.Errors) == 0 {
		return nil
	}
	return verr
}

// newExprAttrType creates a tmplcheck.Type wrapping an attribute expression, returning nil
// for unconstrained dynamic types (such as Any or struct:field:type overrides).
func newExprAttrType(att *goaexpr.AttributeExpr) tmplcheck.Type {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return tmplcheck.ReflectType(reflect.TypeFor[struct{}]())
	}
	if len(att.Meta["struct:field:type"]) > 0 {
		return nil
	}
	if att.Type == goaexpr.Any {
		return nil
	}
	if ut, ok := att.Type.(goaexpr.UserType); ok {
		if len(ut.Attribute().Meta["struct:field:type"]) > 0 {
			return nil
		}
	}
	return &exprAttrType{att: att}
}

// unwrapAttr strips enclosing user types, returning the underlying attribute expression
// and a boolean indicating whether any encountered user type had a dynamic override.
func unwrapAttr(att *goaexpr.AttributeExpr) (*goaexpr.AttributeExpr, bool) {
	curr := att
	for curr != nil && curr.Type != nil {
		if ut, ok := curr.Type.(goaexpr.UserType); ok && ut != goaexpr.Empty {
			if len(ut.Attribute().Meta["struct:field:type"]) > 0 {
				return nil, true
			}
			curr = ut.Attribute()
			continue
		}
		break
	}
	return curr, false
}

// ID returns the type identity for cycle detection in recursive template invocations.
func (e *exprAttrType) ID() string {
	if e == nil || e.att == nil || e.att.Type == nil {
		return ""
	}
	if ut, ok := e.att.Type.(goaexpr.UserType); ok {
		return "ut:" + ut.ID()
	}
	switch dt := e.att.Type.(type) {
	case *goaexpr.Object:
		return fmt.Sprintf("obj:%p", dt)
	case *goaexpr.Array:
		return fmt.Sprintf("arr:%p", dt)
	case *goaexpr.Map:
		return fmt.Sprintf("map:%p", dt)
	case goaexpr.Primitive:
		return "prim:" + dt.Name()
	default:
		return ""
	}
}

// Member resolves dot-field or map key access on this attribute type.
func (e *exprAttrType) Member(name string) (tmplcheck.Type, error) {
	if e == nil || e.att == nil || e.att.Type == nil {
		return nil, fmt.Errorf("%w: member %q on nil type", prompt.ErrTemplateFieldNotFound, name)
	}
	if ut, ok := e.att.Type.(goaexpr.UserType); ok {
		if len(ut.Attribute().Meta["struct:field:type"]) > 0 {
			return nil, nil
		}
		return (&exprAttrType{att: ut.Attribute()}).Member(name)
	}
	if obj := goaexpr.AsObject(e.att.Type); obj != nil {
		for _, nat := range *obj {
			if nat == nil || nat.Attribute == nil {
				continue
			}
			goName := codegen.GoifyAtt(nat.Attribute, nat.Name, true)
			if goName == name {
				return newExprAttrType(nat.Attribute), nil
			}
		}
		return nil, fmt.Errorf("%w: field %q not declared in Args", prompt.ErrTemplateFieldNotFound, name)
	}
	if mp := goaexpr.AsMap(e.att.Type); mp != nil {
		if mp.KeyType != nil && ((mp.KeyType.Type == goaexpr.String && len(mp.KeyType.Meta["struct:field:type"]) == 0) || mp.KeyType.Type == goaexpr.Any) {
			return newExprAttrType(mp.ElemType), nil
		}
		return nil, fmt.Errorf("%w: field %q not declared in type %s", prompt.ErrTemplateFieldNotFound, name, e.att.Type.Name())
	}
	return nil, fmt.Errorf("%w: field %q not declared in type %s", prompt.ErrTemplateFieldNotFound, name, e.att.Type.Name())
}

// Index resolves an index operation (index x k) on array, map, string, or bytes types.
func (e *exprAttrType) Index() (tmplcheck.Type, error) {
	curr, dynamic := unwrapAttr(e.att)
	if dynamic || curr == nil || curr.Type == nil {
		return nil, nil
	}
	if curr.Type == goaexpr.Any {
		return nil, nil
	}
	switch dt := curr.Type.(type) {
	case *goaexpr.Array:
		return newExprAttrType(dt.ElemType), nil
	case *goaexpr.Map:
		return newExprAttrType(dt.ElemType), nil
	case goaexpr.Primitive:
		if dt.Kind() == goaexpr.StringKind || dt.Kind() == goaexpr.BytesKind {
			return tmplcheck.ReflectType(reflect.TypeFor[byte]()), nil
		}
		return nil, fmt.Errorf("%w: cannot index type %s", prompt.ErrTemplateFieldNotFound, curr.Type.Name())
	default:
		return nil, fmt.Errorf("%w: cannot index type %s", prompt.ErrTemplateFieldNotFound, curr.Type.Name())
	}
}

// Range resolves a range iteration (range x) on array, map, or integer range types.
func (e *exprAttrType) Range() (tmplcheck.Type, tmplcheck.Type, error) {
	curr, dynamic := unwrapAttr(e.att)
	if dynamic || curr == nil || curr.Type == nil {
		return nil, nil, nil
	}
	if curr.Type == goaexpr.Any {
		return nil, nil, nil
	}
	switch dt := curr.Type.(type) {
	case *goaexpr.Array:
		return tmplcheck.ReflectType(reflect.TypeFor[int]()), newExprAttrType(dt.ElemType), nil
	case *goaexpr.Map:
		return newExprAttrType(dt.KeyType), newExprAttrType(dt.ElemType), nil
	case goaexpr.Primitive:
		switch dt.Kind() { //nolint:exhaustive // Only integer primitives support range.
		case goaexpr.IntKind, goaexpr.Int32Kind, goaexpr.Int64Kind, goaexpr.UIntKind, goaexpr.UInt32Kind, goaexpr.UInt64Kind:
			return tmplcheck.ReflectType(reflect.TypeFor[int]()), tmplcheck.ReflectType(reflect.TypeFor[int]()), nil
		default:
			return nil, nil, fmt.Errorf("%w: cannot range over type %s", prompt.ErrTemplateFieldNotFound, curr.Type.Name())
		}
	default:
		return nil, nil, fmt.Errorf("%w: cannot range over type %s", prompt.ErrTemplateFieldNotFound, curr.Type.Name())
	}
}
