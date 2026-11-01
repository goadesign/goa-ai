// Package prompt implements template parsing and static AST field validation for
// prompt templates.
//
// Static field validation inspects text/template/parse AST nodes before prompt
// registration or override persistence, ensuring templates only access declared
// fields when PromptSpec.Fields is specified.
package prompt

import (
	"fmt"
	"strings"
	"text/template"
	"text/template/parse"

	"goa.design/goa-ai/runtime/agent/tools"
)

// ValidatePromptTemplate parses templateSource and verifies field accesses
// against spec.Fields using the template AST.
func ValidatePromptTemplate(spec PromptSpec, templateSource string) error {
	tmpl := template.New(spec.ID.String()).Option("missingkey=error")
	funcs := template.FuncMap{
		"render": func(string, ...any) (string, error) {
			return "", nil
		},
	}
	for k, v := range spec.Funcs {
		funcs[k] = v
	}
	tmpl = tmpl.Funcs(funcs)

	parsed, err := tmpl.Parse(templateSource)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrTemplateParse, spec.ID, err)
	}

	// Untyped manual prompts (len(spec.Fields) == 0) skip static field checking.
	if len(spec.Fields) == 0 {
		return nil
	}

	activeTemplates := make(map[string]bool)
	visitedTemplates := make(map[string]bool)

	// Determine the main entry root.
	root := parsed.Root
	if !hasExecutableNodes(root) {
		if named := parsed.Lookup(spec.ID.String()); named != nil && named.Root != nil {
			root = named.Root
			visitedTemplates[spec.ID.String()] = true
		}
	}

	if root != nil {
		vars := map[string][]string{
			"$": nil,
		}
		if err := walkASTNode(root, nil, vars, spec.Fields, parsed, activeTemplates, visitedTemplates); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrTemplateFieldNotFound, spec.ID, err)
		}
	}

	// Walk any remaining unvisited defined templates in parsed.Templates().
	for _, t := range parsed.Templates() {
		if t.Root == nil || visitedTemplates[t.Name()] {
			continue
		}
		vars := map[string][]string{
			"$": nil,
		}
		if err := walkASTNode(t.Root, nil, vars, spec.Fields, parsed, activeTemplates, visitedTemplates); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrTemplateFieldNotFound, spec.ID, err)
		}
		visitedTemplates[t.Name()] = true
	}

	return nil
}

// walkASTNode traverses a template parse tree node and validates dot and variable field accesses.
func walkASTNode(
	node parse.Node,
	dotPath []string,
	vars map[string][]string,
	fields []tools.FieldMetadata,
	tmpl *template.Template,
	activeTemplates map[string]bool,
	visitedTemplates map[string]bool,
) error {
	switch n := node.(type) {
	case nil:
		return nil

	case *parse.ListNode:
		if n == nil {
			return nil
		}
		for _, child := range n.Nodes {
			if err := walkASTNode(child, dotPath, vars, fields, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		return nil

	case *parse.ActionNode:
		if n == nil || n.Pipe == nil {
			return nil
		}
		if len(n.Pipe.Decl) > 0 {
			if valPath, ok := resolvePipePath(n.Pipe, dotPath, vars); ok {
				for _, decl := range n.Pipe.Decl {
					if len(decl.Ident) > 0 {
						vars[decl.Ident[0]] = valPath
					}
				}
			}
		}
		return walkASTNode(n.Pipe, dotPath, vars, fields, tmpl, activeTemplates, visitedTemplates)

	case *parse.PipeNode:
		if n == nil {
			return nil
		}
		for _, cmd := range n.Cmds {
			if err := walkASTNode(cmd, dotPath, vars, fields, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		return nil

	case *parse.CommandNode:
		if n == nil {
			return nil
		}
		if len(n.Args) >= 2 {
			if ident, ok := n.Args[0].(*parse.IdentifierNode); ok && ident.Ident == "index" {
				if target, ok := resolveNodePath(n.Args[1], n.Args[2:], dotPath, vars); ok {
					if err := validateFieldPath(target, fields); err != nil {
						return err
					}
				}
			}
		}
		for _, arg := range n.Args {
			if err := walkASTNode(arg, dotPath, vars, fields, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		return nil

	case *parse.IfNode:
		if n == nil {
			return nil
		}
		if n.Pipe != nil {
			if err := walkASTNode(n.Pipe, dotPath, vars, fields, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		if n.List != nil {
			if err := walkASTNode(n.List, dotPath, copyVars(vars), fields, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		if n.ElseList != nil {
			if err := walkASTNode(n.ElseList, dotPath, copyVars(vars), fields, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		return nil

	case *parse.WithNode:
		if n == nil {
			return nil
		}
		if n.Pipe != nil {
			if err := walkASTNode(n.Pipe, dotPath, vars, fields, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		withPath, ok := resolvePipePath(n.Pipe, dotPath, vars)
		listDot := dotPath
		if ok {
			listDot = withPath
		}
		listVars := copyVars(vars)
		if ok && len(n.Pipe.Decl) > 0 {
			for _, decl := range n.Pipe.Decl {
				if len(decl.Ident) > 0 {
					listVars[decl.Ident[0]] = withPath
				}
			}
		}
		if n.List != nil {
			if err := walkASTNode(n.List, listDot, listVars, fields, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		if n.ElseList != nil {
			if err := walkASTNode(n.ElseList, dotPath, copyVars(vars), fields, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		return nil

	case *parse.RangeNode:
		if n == nil {
			return nil
		}
		if n.Pipe != nil {
			if err := walkASTNode(n.Pipe, dotPath, vars, fields, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		collPath, ok := resolvePipePath(n.Pipe, dotPath, vars)
		listDot := dotPath
		listVars := copyVars(vars)
		if ok {
			elemPath := append(append([]string(nil), collPath...), "*")
			listDot = elemPath
			if len(n.Pipe.Decl) == 1 {
				if len(n.Pipe.Decl[0].Ident) > 0 {
					listVars[n.Pipe.Decl[0].Ident[0]] = elemPath
				}
			} else if len(n.Pipe.Decl) == 2 {
				// Decl[0] is index/key, Decl[1] is element
				if len(n.Pipe.Decl[1].Ident) > 0 {
					listVars[n.Pipe.Decl[1].Ident[0]] = elemPath
				}
			}
		}
		if n.List != nil {
			if err := walkASTNode(n.List, listDot, listVars, fields, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		if n.ElseList != nil {
			if err := walkASTNode(n.ElseList, dotPath, copyVars(vars), fields, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		return nil

	case *parse.BranchNode:
		if n == nil {
			return nil
		}
		if n.Pipe != nil {
			if err := walkASTNode(n.Pipe, dotPath, vars, fields, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		if n.List != nil {
			if err := walkASTNode(n.List, dotPath, copyVars(vars), fields, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		if n.ElseList != nil {
			if err := walkASTNode(n.ElseList, dotPath, copyVars(vars), fields, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		return nil

	case *parse.TemplateNode:
		if n == nil {
			return nil
		}
		if n.Pipe != nil {
			if err := walkASTNode(n.Pipe, dotPath, vars, fields, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		pipeDot := dotPath
		if n.Pipe != nil {
			if p, ok := resolvePipePath(n.Pipe, dotPath, vars); ok {
				pipeDot = p
			}
		}
		if tmpl != nil && !activeTemplates[n.Name] {
			if sub := tmpl.Lookup(n.Name); sub != nil && sub.Root != nil {
				activeTemplates[n.Name] = true
				subVars := map[string][]string{
					"$": pipeDot,
				}
				err := walkASTNode(sub.Root, pipeDot, subVars, fields, tmpl, activeTemplates, visitedTemplates)
				delete(activeTemplates, n.Name)
				if err != nil {
					return err
				}
				visitedTemplates[n.Name] = true
			}
		}
		return nil

	case *parse.FieldNode:
		if n == nil || len(n.Ident) == 0 {
			return nil
		}
		fullPath := append(append([]string(nil), dotPath...), n.Ident...)
		return validateFieldPath(fullPath, fields)

	case *parse.VariableNode:
		if n == nil || len(n.Ident) == 0 {
			return nil
		}
		varName := n.Ident[0]
		base, ok := vars[varName]
		if !ok {
			return nil
		}
		fullPath := append(append([]string(nil), base...), n.Ident[1:]...)
		if len(fullPath) > 0 {
			return validateFieldPath(fullPath, fields)
		}
		return nil

	case *parse.ChainNode:
		if n == nil {
			return nil
		}
		if base, ok := resolveNodePath(n.Node, nil, dotPath, vars); ok {
			fullPath := append(append([]string(nil), base...), n.Field...)
			if err := validateFieldPath(fullPath, fields); err != nil {
				return err
			}
		}
		return walkASTNode(n.Node, dotPath, vars, fields, tmpl, activeTemplates, visitedTemplates)

	default:
		return nil
	}
}

// validateFieldPath verifies that target path exists in declared fields.
// Condition (a): Prefix match against a declared field with length >= len(target).
// Condition (b): Ancestor at target[:k] is an unstructured object (or Any) with zero declared descendants.
func validateFieldPath(target []string, fields []tools.FieldMetadata) error {
	if len(target) == 0 {
		return nil
	}
	if len(fields) == 0 {
		return nil
	}

	// Condition (a): Prefix match against a declared field with length >= len(target).
	for _, f := range fields {
		if len(f.Path) >= len(target) && segmentMatches(f.Path[:len(target)], target) {
			return nil
		}
	}

	// Condition (b): Unstructured object ancestor check.
	// An ancestor at target[:k] is an unstructured object (or Any) iff:
	// 1. It has JSONType == "object" (or "" or "any").
	// 2. There are ZERO longer descendant entries in spec.Fields under target[:k].
	for k := 0; k < len(target); k++ {
		ancestor := target[:k]
		for _, f := range fields {
			if len(f.Path) == k && segmentMatches(f.Path, ancestor) {
				if f.JSONType == "object" || f.JSONType == "" || f.JSONType == "any" {
					hasDescendants := false
					for _, other := range fields {
						if len(other.Path) > k && segmentMatches(other.Path[:k], ancestor) {
							hasDescendants = true
							break
						}
					}
					if !hasDescendants {
						return nil
					}
				}
			}
		}
	}

	return fmt.Errorf("field %q not declared in prompt spec", strings.Join(target, "."))
}

// segmentMatches compares pattern against target segments using exact string matching.
func segmentMatches(pattern []tools.FieldPathSegment, target []string) bool {
	if len(pattern) != len(target) {
		return false
	}
	for i, seg := range pattern {
		switch s := seg.(type) {
		case tools.FixedField:
			if target[i] != string(s) {
				return false
			}
		case tools.DynamicField:
			// DynamicField matches array index wildcard ("*") or any dynamic map key string.
		default:
			return false
		}
	}
	return true
}

func resolvePipePath(pipe *parse.PipeNode, dotPath []string, vars map[string][]string) ([]string, bool) {
	if pipe == nil || len(pipe.Cmds) == 0 {
		return nil, false
	}
	cmd := pipe.Cmds[0]
	if len(cmd.Args) == 0 {
		return nil, false
	}
	return resolveNodePath(cmd.Args[0], cmd.Args[1:], dotPath, vars)
}

func resolveNodePath(first parse.Node, rest []parse.Node, dotPath []string, vars map[string][]string) ([]string, bool) {
	var target []string
	switch n := first.(type) {
	case *parse.DotNode:
		target = append([]string(nil), dotPath...)

	case *parse.FieldNode:
		target = append(append([]string(nil), dotPath...), n.Ident...)

	case *parse.VariableNode:
		if len(n.Ident) == 0 {
			return nil, false
		}
		base, ok := vars[n.Ident[0]]
		if !ok {
			return nil, false
		}
		target = append(append([]string(nil), base...), n.Ident[1:]...)

	case *parse.ChainNode:
		base, ok := resolveNodePath(n.Node, nil, dotPath, vars)
		if !ok {
			return nil, false
		}
		target = append(append([]string(nil), base...), n.Field...)

	case *parse.IdentifierNode:
		if n.Ident == "index" && len(rest) >= 1 {
			return resolveNodePath(rest[0], rest[1:], dotPath, vars)
		}
		return nil, false

	case *parse.PipeNode:
		return resolvePipePath(n, dotPath, vars)

	default:
		return nil, false
	}

	for _, arg := range rest {
		switch a := arg.(type) {
		case *parse.StringNode:
			target = append(target, a.Text)
		case *parse.NumberNode:
			target = append(target, "*")
		default:
			return target, true
		}
	}
	return target, true
}

func copyVars(vars map[string][]string) map[string][]string {
	cp := make(map[string][]string, len(vars))
	for k, v := range vars {
		cp[k] = v
	}
	return cp
}

func hasExecutableNodes(node parse.Node) bool {
	switch n := node.(type) {
	case nil:
		return false
	case *parse.ListNode:
		if n == nil {
			return false
		}
		for _, child := range n.Nodes {
			if hasExecutableNodes(child) {
				return true
			}
		}
		return false
	case *parse.TextNode, *parse.CommentNode:
		return false
	default:
		return true
	}
}
