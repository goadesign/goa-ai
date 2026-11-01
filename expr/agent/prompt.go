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
	"text/template"
	"text/template/parse"

	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/tools"
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

// FieldMetadata returns the static field specifications for this prompt.
//
// When p.Args is empty or nil, it returns a single entry with JSONType "null"
// (the zero-arg sentinel recognized by prompt.ValidatePromptTemplate).
// When p.Args is an object, it traverses fields recursively, emitting exported Go
// struct field names (using codegen.GoifyAtt, respecting struct:field:name metadata).
func (p *PromptExpr) FieldMetadata() []tools.FieldMetadata {
	if p.Args == nil || p.Args.Type == nil || p.Args.Type == goaexpr.Empty {
		return []tools.FieldMetadata{{JSONType: "null"}}
	}
	obj := goaexpr.AsObject(p.Args.Type)
	if obj == nil {
		return []tools.FieldMetadata{{JSONType: "null"}}
	}
	var (
		fields  []tools.FieldMetadata
		visited = make(map[string]bool)
	)
	var walk func(att *goaexpr.AttributeExpr, path []tools.FieldPathSegment)
	walk = func(att *goaexpr.AttributeExpr, path []tools.FieldPathSegment) {
		if att == nil || att.Type == nil {
			return
		}
		if ut, ok := att.Type.(goaexpr.UserType); ok {
			typeName := ut.Name()
			if typeName != "" {
				if visited[typeName] {
					return
				}
				visited[typeName] = true
				defer func() { delete(visited, typeName) }()
			}
			walk(ut.Attribute(), path)
			return
		}
		switch t := att.Type.(type) {
		case *goaexpr.Object:
			for _, nat := range *t {
				if nat == nil || nat.Attribute == nil {
					continue
				}
				goName := codegen.GoifyAtt(nat.Attribute, nat.Name, true)
				fieldPath := append(append([]tools.FieldPathSegment(nil), path...), tools.FixedField(goName))
				jsonType := jsonCategory(nat.Attribute.Type)
				fields = append(fields, tools.FieldMetadata{
					Path:        fieldPath,
					JSONType:    jsonType,
					Description: nat.Attribute.Description,
				})
				walk(nat.Attribute, fieldPath)
			}
		case *goaexpr.Array:
			elemPath := append(append([]tools.FieldPathSegment(nil), path...), tools.DynamicField{})
			walk(t.ElemType, elemPath)
		case *goaexpr.Map:
			keyPath := append(append([]tools.FieldPathSegment(nil), path...), tools.DynamicField{})
			walk(t.ElemType, keyPath)
		}
	}
	walk(p.Args, nil)
	return fields
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
// using prompt.ValidatePromptTemplate.
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

	spec := prompt.PromptSpec{
		ID:       p.ID(),
		Role:     p.Role,
		Template: p.Template,
		Fields:   p.FieldMetadata(),
	}
	if err := prompt.ValidatePromptTemplate(spec, p.Template); err != nil {
		verr.Add(p, "%s", err)
	}

	if len(verr.Errors) == 0 {
		return nil
	}
	return verr
}

// extractRenderedPromptIDs parses templateSource and extracts literal string prompt IDs
// passed as the first argument to the "render" template function (e.g. {{ render "id" ... }}).
func extractRenderedPromptIDs(templateSource string) []string {
	tmpl, err := template.New("").
		Funcs(template.FuncMap{
			"render": func(string, ...any) (string, error) { return "", nil },
		}).
		Parse(templateSource)
	if err != nil || tmpl.Tree == nil || tmpl.Root == nil {
		return nil
	}

	var (
		ids  []string
		seen = make(map[string]bool)
	)

	var walkNode func(n parse.Node)
	walkNode = func(n parse.Node) {
		if n == nil {
			return
		}
		switch node := n.(type) {
		case *parse.ListNode:
			if node != nil {
				for _, item := range node.Nodes {
					walkNode(item)
				}
			}
		case *parse.ActionNode:
			if node != nil {
				walkNode(node.Pipe)
			}
		case *parse.PipeNode:
			if node != nil {
				for _, cmd := range node.Cmds {
					walkNode(cmd)
				}
			}
		case *parse.CommandNode:
			if node != nil && len(node.Args) >= 2 {
				if idNode, ok := node.Args[0].(*parse.IdentifierNode); ok && idNode.Ident == "render" {
					if strNode, ok := node.Args[1].(*parse.StringNode); ok {
						if !seen[strNode.Text] {
							seen[strNode.Text] = true
							ids = append(ids, strNode.Text)
						}
					}
				}
			}
		case *parse.BranchNode:
			if node != nil {
				walkNode(node.Pipe)
				walkNode(node.List)
				walkNode(node.ElseList)
			}
		case *parse.IfNode:
			if node != nil {
				walkNode(&node.BranchNode)
			}
		case *parse.WithNode:
			if node != nil {
				walkNode(&node.BranchNode)
			}
		case *parse.RangeNode:
			if node != nil {
				walkNode(&node.BranchNode)
			}
		}
	}

	walkNode(tmpl.Root)
	return ids
}

// jsonCategory maps Goa data types to JSON Schema type strings.
func jsonCategory(dt goaexpr.DataType) string {
	switch actual := dt.(type) {
	case goaexpr.UserType:
		return jsonCategory(actual.Attribute().Type)
	case *goaexpr.Object, *goaexpr.Map:
		return "object"
	case *goaexpr.Array:
		return "array"
	case goaexpr.Primitive:
		//nolint:exhaustive // Only primitive kinds map to JSON scalar types.
		switch actual.Kind() {
		case goaexpr.BooleanKind:
			return "boolean"
		case goaexpr.StringKind, goaexpr.BytesKind:
			return "string"
		case goaexpr.IntKind, goaexpr.Int32Kind, goaexpr.Int64Kind, goaexpr.UIntKind, goaexpr.UInt32Kind, goaexpr.UInt64Kind:
			return "integer"
		case goaexpr.Float32Kind, goaexpr.Float64Kind:
			return "number"
		default:
			return ""
		}
	}
	return ""
}
