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

// validatePromptTemplate parses templateSource and verifies field accesses
// against spec.Fields using the template AST.
func validatePromptTemplate(spec PromptSpec, templateSource string) error {
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

	if len(spec.Fields) > 0 && parsed.Tree != nil && parsed.Root != nil {
		if err := walkASTNode(parsed.Root, true, spec.Fields); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrTemplateFieldNotFound, spec.ID, err)
		}
	}
	return nil
}

// walkASTNode traverses a template parse tree node and validates root dot field accesses.
func walkASTNode(node parse.Node, isRootDot bool, fields []tools.FieldSpec) error {
	switch n := node.(type) {
	case nil:
		return nil

	case *parse.ListNode:
		if n == nil {
			return nil
		}
		for _, child := range n.Nodes {
			if err := walkASTNode(child, isRootDot, fields); err != nil {
				return err
			}
		}
		return nil

	case *parse.ActionNode:
		if n == nil || n.Pipe == nil {
			return nil
		}
		return walkASTNode(n.Pipe, isRootDot, fields)

	case *parse.PipeNode:
		if n == nil {
			return nil
		}
		for _, cmd := range n.Cmds {
			if err := walkASTNode(cmd, isRootDot, fields); err != nil {
				return err
			}
		}
		return nil

	case *parse.CommandNode:
		if n == nil {
			return nil
		}
		for _, arg := range n.Args {
			if err := walkASTNode(arg, isRootDot, fields); err != nil {
				return err
			}
		}
		return nil

	case *parse.IfNode:
		if n == nil {
			return nil
		}
		if n.Pipe != nil {
			if err := walkASTNode(n.Pipe, isRootDot, fields); err != nil {
				return err
			}
		}
		if n.List != nil {
			if err := walkASTNode(n.List, isRootDot, fields); err != nil {
				return err
			}
		}
		if n.ElseList != nil {
			if err := walkASTNode(n.ElseList, isRootDot, fields); err != nil {
				return err
			}
		}
		return nil

	case *parse.WithNode:
		if n == nil {
			return nil
		}
		if n.Pipe != nil {
			if err := walkASTNode(n.Pipe, isRootDot, fields); err != nil {
				return err
			}
		}
		if n.List != nil {
			if err := walkASTNode(n.List, false, fields); err != nil {
				return err
			}
		}
		if n.ElseList != nil {
			if err := walkASTNode(n.ElseList, isRootDot, fields); err != nil {
				return err
			}
		}
		return nil

	case *parse.RangeNode:
		if n == nil {
			return nil
		}
		if n.Pipe != nil {
			if err := walkASTNode(n.Pipe, isRootDot, fields); err != nil {
				return err
			}
		}
		if n.List != nil {
			if err := walkASTNode(n.List, false, fields); err != nil {
				return err
			}
		}
		if n.ElseList != nil {
			if err := walkASTNode(n.ElseList, isRootDot, fields); err != nil {
				return err
			}
		}
		return nil

	case *parse.BranchNode:
		if n == nil {
			return nil
		}
		if n.Pipe != nil {
			if err := walkASTNode(n.Pipe, isRootDot, fields); err != nil {
				return err
			}
		}
		if n.List != nil {
			if err := walkASTNode(n.List, false, fields); err != nil {
				return err
			}
		}
		if n.ElseList != nil {
			if err := walkASTNode(n.ElseList, isRootDot, fields); err != nil {
				return err
			}
		}
		return nil

	case *parse.TemplateNode:
		if n == nil || n.Pipe == nil {
			return nil
		}
		return walkASTNode(n.Pipe, isRootDot, fields)

	case *parse.FieldNode:
		if !isRootDot {
			return nil
		}
		return validateFieldAccess(n.Ident, fields)

	case *parse.ChainNode:
		if !isRootDot {
			return nil
		}
		if n.Node != nil {
			if err := walkASTNode(n.Node, isRootDot, fields); err != nil {
				return err
			}
		}
		return nil

	default:
		return nil
	}
}

// validateFieldAccess verifies that ident path exists in declared fields.
func validateFieldAccess(ident []string, fields []tools.FieldSpec) error {
	if len(ident) == 0 || len(fields) == 0 {
		return nil
	}
	rootName := ident[0]
	var matchingCandidates []tools.FieldSpec
	for _, f := range fields {
		if len(f.Path) == 0 {
			continue
		}
		if seg, ok := f.Path[0].(tools.FixedField); ok {
			if matchFieldName(string(seg), rootName) {
				matchingCandidates = append(matchingCandidates, f)
			}
		}
	}

	if len(matchingCandidates) == 0 {
		return fmt.Errorf("root field %q not declared", rootName)
	}

	if len(ident) == 1 {
		return nil
	}

	for _, f := range matchingCandidates {
		if len(f.Path) == 1 {
			return nil
		}
	}

	for _, f := range matchingCandidates {
		if pathMatchesIdent(f.Path, ident) {
			return nil
		}
	}

	return fmt.Errorf("nested path %q not declared", strings.Join(ident, "."))
}

// matchFieldName compares a schema field segment name with a template identifier name,
// supporting exact, case-insensitive, and snake_case/camelCase equivalence.
func matchFieldName(segName, ident string) bool {
	if segName == ident {
		return true
	}
	if strings.EqualFold(segName, ident) {
		return true
	}
	normSeg := strings.ToLower(strings.ReplaceAll(segName, "_", ""))
	normIdent := strings.ToLower(strings.ReplaceAll(ident, "_", ""))
	return normSeg == normIdent
}

// pathMatchesIdent checks whether path matches ident along their common prefix.
func pathMatchesIdent(path []tools.FieldPathSegment, ident []string) bool {
	limit := min(len(path), len(ident))
	for i := 0; i < limit; i++ {
		seg, ok := path[i].(tools.FixedField)
		if !ok {
			continue
		}
		if !matchFieldName(string(seg), ident[i]) {
			return false
		}
	}
	return true
}
