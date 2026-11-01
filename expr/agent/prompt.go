// Package agent defines expression types used to represent prompts alongside
// agents and toolsets during Goa design evaluation.
//
// PromptExpr captures prompt declarations configured via Goa DSL on agents and
// services. Compile-time validation enforces non-empty names, baseline templates,
// valid prompt roles, template syntax correctness, and alignment between template
// dot-field references and declared Args schemas.
package agent

import (
	"fmt"
	"strings"
	"text/template"
	"text/template/parse"

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

// Prepare ensures prompt defaults are initialized before validation.
func (p *PromptExpr) Prepare() {
	if p.Role == "" {
		if p.Name == "system" {
			p.Role = prompt.PromptRoleSystem
		} else {
			p.Role = prompt.PromptRoleUser
		}
	}
}

// Validate verifies prompt invariants: non-empty name and template, valid role,
// syntactically valid Go template, and typed Args shape and field alignment.
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

	tmpl, err := template.New(string(p.ID())).
		Option("missingkey=error").
		Funcs(template.FuncMap{
			"render": func(string, ...any) string { return "" },
		}).
		Parse(p.Template)
	if err != nil {
		verr.Add(p, "failed to parse template: %s", err)
		return verr
	}

	fieldRefs := collectTemplateFieldRefs(tmpl.Tree)

	hasArgs := p.Args != nil && p.Args.Type != nil && p.Args.Type != goaexpr.Empty
	if hasArgs {
		validateContractShape(p, "Args", p.Args, verr)
	}

	for _, ref := range fieldRefs {
		if !hasArgs {
			verr.Add(p, "template references undeclared argument %q but prompt declares no Args", "."+ref)
			continue
		}
		if !attributeHasField(p.Args, ref) {
			verr.Add(p, "template references undeclared argument %q on Args contract", "."+ref)
		}
	}

	if len(verr.Errors) == 0 {
		return nil
	}
	return verr
}

// collectTemplateFieldRefs walks the parsed template AST tree and collects all
// top-level dot field references (.FieldName).
func collectTemplateFieldRefs(tree *parse.Tree) []string {
	if tree == nil || tree.Root == nil {
		return nil
	}
	var refs []string
	seen := make(map[string]struct{})
	var walk func(node parse.Node)
	walk = func(node parse.Node) {
		if node == nil {
			return
		}
		switch n := node.(type) {
		case *parse.ListNode:
			if n != nil {
				for _, item := range n.Nodes {
					walk(item)
				}
			}
		case *parse.ActionNode:
			if n != nil {
				walk(n.Pipe)
			}
		case *parse.PipeNode:
			if n != nil {
				for _, cmd := range n.Cmds {
					walk(cmd)
				}
				for _, decl := range n.Decl {
					walk(decl)
				}
			}
		case *parse.CommandNode:
			if n != nil {
				for _, arg := range n.Args {
					walk(arg)
				}
			}
		case *parse.BranchNode:
			if n != nil {
				walk(n.Pipe)
				walk(n.List)
				walk(n.ElseList)
			}
		case *parse.IfNode:
			if n != nil {
				walk(&n.BranchNode)
			}
		case *parse.WithNode:
			if n != nil {
				walk(&n.BranchNode)
			}
		case *parse.RangeNode:
			if n != nil {
				walk(&n.BranchNode)
			}
		case *parse.TemplateNode:
			if n != nil {
				walk(n.Pipe)
			}
		case *parse.ChainNode:
			if n != nil {
				walk(n.Node)
			}
		case *parse.FieldNode:
			if n != nil && len(n.Ident) > 0 {
				ref := n.Ident[0]
				if _, dup := seen[ref]; !dup {
					seen[ref] = struct{}{}
					refs = append(refs, ref)
				}
			}
		}
	}
	walk(tree.Root)
	return refs
}

// attributeHasField returns true if the given attribute represents an object
// containing a field matching ref by attribute name, Go struct name, or field tag.
func attributeHasField(att *goaexpr.AttributeExpr, ref string) bool {
	if att == nil || att.Type == nil {
		return false
	}
	obj := goaexpr.AsObject(att.Type)
	if obj == nil {
		return false
	}
	for _, named := range *obj {
		if named == nil {
			continue
		}
		if named.Name == ref {
			return true
		}
		if codegen.Goify(named.Name, true) == ref {
			return true
		}
		if codegen.Goify(named.Name, false) == ref {
			return true
		}
		if strings.EqualFold(named.Name, ref) {
			return true
		}
		if named.Attribute != nil && len(named.Attribute.Meta["struct:field:name"]) > 0 {
			if named.Attribute.Meta["struct:field:name"][0] == ref {
				return true
			}
		}
	}
	return false
}
