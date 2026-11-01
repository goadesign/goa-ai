// Package prompt implements template parsing and static AST field validation for
// prompt templates.
//
// Static field validation inspects text/template/parse AST nodes before prompt
// registration or override persistence, ensuring templates only access declared
// fields when PromptSpec.Fields is specified.
package prompt

import (
	"fmt"
	"reflect"
	"strings"
	"text/template"
	"text/template/parse"

	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// templateValueKind categorizes how a template expression's type is known.
	templateValueKind int

	// templateValue tracks static typing for template evaluation contexts.
	templateValue struct {
		kind  templateValueKind
		path  []string
		rtype reflect.Type
	}
)

const (
	// valueFieldsPath indicates the value is tracked by a field path within PromptSpec.Fields.
	valueFieldsPath templateValueKind = iota
	// valueReflectType indicates the value has a known Go reflect.Type.
	valueReflectType
	// valueDynamic indicates an unconstrained runtime value.
	valueDynamic
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

	rootDot := templateValue{
		kind: valueFieldsPath,
		path: nil,
	}

	// Determine the main entry root.
	root := parsed.Root
	if !hasExecutableNodes(root) {
		if named := parsed.Lookup(spec.ID.String()); named != nil && named.Root != nil {
			root = named.Root
			visitedTemplates[spec.ID.String()] = true
		}
	}

	if root != nil {
		vars := map[string]templateValue{
			"$": rootDot,
		}
		if err := walkASTNode(root, rootDot, vars, spec.Fields, funcs, parsed, activeTemplates, visitedTemplates); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrTemplateFieldNotFound, spec.ID, err)
		}
	}

	// Walk any remaining unvisited defined templates in parsed.Templates().
	for _, t := range parsed.Templates() {
		if t.Root == nil || visitedTemplates[t.Name()] {
			continue
		}
		vars := map[string]templateValue{
			"$": rootDot,
		}
		if err := walkASTNode(t.Root, rootDot, vars, spec.Fields, funcs, parsed, activeTemplates, visitedTemplates); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrTemplateFieldNotFound, spec.ID, err)
		}
		visitedTemplates[t.Name()] = true
	}

	return nil
}

// walkASTNode traverses a template parse tree node and validates dot and variable field accesses.
func walkASTNode(
	node parse.Node,
	dot templateValue,
	vars map[string]templateValue,
	fields []tools.FieldMetadata,
	funcs template.FuncMap,
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
			if err := walkASTNode(child, dot, vars, fields, funcs, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		return nil

	case *parse.ActionNode:
		if n == nil || n.Pipe == nil {
			return nil
		}
		// First validate the RHS pipeline using the current vars
		if err := walkASTNode(n.Pipe, dot, vars, fields, funcs, tmpl, activeTemplates, visitedTemplates); err != nil {
			return err
		}
		// Then resolve pipeline value and update vars for declared identifiers
		if len(n.Pipe.Decl) > 0 {
			pipeVal := resolvePipeValue(n.Pipe, dot, vars, fields, funcs)
			for _, decl := range n.Pipe.Decl {
				if len(decl.Ident) > 0 {
					vars[decl.Ident[0]] = pipeVal
				}
			}
		}
		return nil

	case *parse.PipeNode:
		if n == nil {
			return nil
		}
		for _, cmd := range n.Cmds {
			if err := walkASTNode(cmd, dot, vars, fields, funcs, tmpl, activeTemplates, visitedTemplates); err != nil {
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
				idxVal := resolveIndexCommand(n, nil, dot, vars, fields, funcs)
				if idxVal.kind == valueFieldsPath && len(idxVal.path) > 0 {
					if err := validateFieldPath(idxVal.path, fields); err != nil {
						return err
					}
				}
			}
		}
		for _, arg := range n.Args {
			if err := walkASTNode(arg, dot, vars, fields, funcs, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		return nil

	case *parse.IfNode:
		if n == nil {
			return nil
		}
		if n.Pipe != nil {
			if err := walkASTNode(n.Pipe, dot, vars, fields, funcs, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		branchVars := copyVars(vars)
		if n.Pipe != nil && len(n.Pipe.Decl) > 0 {
			pipeVal := resolvePipeValue(n.Pipe, dot, vars, fields, funcs)
			for _, decl := range n.Pipe.Decl {
				if len(decl.Ident) > 0 {
					branchVars[decl.Ident[0]] = pipeVal
				}
			}
		}
		if n.List != nil {
			if err := walkASTNode(n.List, dot, branchVars, fields, funcs, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		if n.ElseList != nil {
			if err := walkASTNode(n.ElseList, dot, branchVars, fields, funcs, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		return nil

	case *parse.WithNode:
		if n == nil {
			return nil
		}
		if n.Pipe != nil {
			if err := walkASTNode(n.Pipe, dot, vars, fields, funcs, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		listDot := templateValue{kind: valueDynamic}
		if n.Pipe != nil {
			listDot = resolvePipeValue(n.Pipe, dot, vars, fields, funcs)
		}
		listVars := copyVars(vars)
		if n.Pipe != nil && len(n.Pipe.Decl) > 0 {
			for _, decl := range n.Pipe.Decl {
				if len(decl.Ident) > 0 {
					listVars[decl.Ident[0]] = listDot
				}
			}
		}
		if n.List != nil {
			if err := walkASTNode(n.List, listDot, listVars, fields, funcs, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		if n.ElseList != nil {
			if err := walkASTNode(n.ElseList, dot, copyVars(vars), fields, funcs, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		return nil

	case *parse.RangeNode:
		if n == nil {
			return nil
		}
		if n.Pipe != nil {
			if err := walkASTNode(n.Pipe, dot, vars, fields, funcs, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		collVal := templateValue{kind: valueDynamic}
		if n.Pipe != nil {
			collVal = resolvePipeValue(n.Pipe, dot, vars, fields, funcs)
		}
		var keyVal, elemVal templateValue
		switch collVal.kind {
		case valueReflectType:
			rt := collVal.rtype
			for rt != nil && rt.Kind() == reflect.Ptr {
				rt = rt.Elem()
			}
			if rt == nil {
				keyVal = templateValue{kind: valueDynamic}
				elemVal = templateValue{kind: valueDynamic}
			} else {
				switch rt.Kind() { //nolint:exhaustive // Only collection kinds have element types.
				case reflect.Slice, reflect.Array:
					keyVal = templateValueFromReflectType(reflect.TypeOf(0))
					elemVal = templateValueFromReflectType(rt.Elem())
				case reflect.Chan:
					keyVal = templateValue{kind: valueDynamic}
					elemVal = templateValueFromReflectType(rt.Elem())
				case reflect.Map:
					keyVal = templateValueFromReflectType(rt.Key())
					elemVal = templateValueFromReflectType(rt.Elem())
				default:
					keyVal = templateValue{kind: valueDynamic}
					elemVal = templateValue{kind: valueDynamic}
				}
			}
		case valueFieldsPath:
			keyVal = templateValue{kind: valueDynamic}
			elemPath := append(append([]string(nil), collVal.path...), "*")
			elemVal = templateValue{kind: valueFieldsPath, path: elemPath}
		case valueDynamic:
			keyVal = templateValue{kind: valueDynamic}
			elemVal = templateValue{kind: valueDynamic}
		}

		listVars := copyVars(vars)
		if n.Pipe != nil && len(n.Pipe.Decl) > 0 {
			if len(n.Pipe.Decl) == 1 {
				if len(n.Pipe.Decl[0].Ident) > 0 {
					listVars[n.Pipe.Decl[0].Ident[0]] = elemVal
				}
			} else if len(n.Pipe.Decl) >= 2 {
				if len(n.Pipe.Decl[0].Ident) > 0 {
					listVars[n.Pipe.Decl[0].Ident[0]] = keyVal
				}
				if len(n.Pipe.Decl[1].Ident) > 0 {
					listVars[n.Pipe.Decl[1].Ident[0]] = elemVal
				}
			}
		}
		listDot := elemVal
		if n.List != nil {
			if err := walkASTNode(n.List, listDot, listVars, fields, funcs, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		if n.ElseList != nil {
			if err := walkASTNode(n.ElseList, dot, copyVars(vars), fields, funcs, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		return nil

	case *parse.BranchNode:
		if n == nil {
			return nil
		}
		if n.Pipe != nil {
			if err := walkASTNode(n.Pipe, dot, vars, fields, funcs, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		if n.List != nil {
			if err := walkASTNode(n.List, dot, copyVars(vars), fields, funcs, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		if n.ElseList != nil {
			if err := walkASTNode(n.ElseList, dot, copyVars(vars), fields, funcs, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		return nil

	case *parse.TemplateNode:
		if n == nil {
			return nil
		}
		if n.Pipe != nil {
			if err := walkASTNode(n.Pipe, dot, vars, fields, funcs, tmpl, activeTemplates, visitedTemplates); err != nil {
				return err
			}
		}
		pipeDot := dot
		if n.Pipe != nil {
			pipeDot = resolvePipeValue(n.Pipe, dot, vars, fields, funcs)
		}
		if tmpl != nil && !activeTemplates[n.Name] {
			if sub := tmpl.Lookup(n.Name); sub != nil && sub.Root != nil {
				activeTemplates[n.Name] = true
				subVars := map[string]templateValue{
					"$": pipeDot,
				}
				err := walkASTNode(sub.Root, pipeDot, subVars, fields, funcs, tmpl, activeTemplates, visitedTemplates)
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
		_, err := resolveFieldAccess(dot, n.Ident, fields)
		return err

	case *parse.VariableNode:
		if n == nil || len(n.Ident) == 0 {
			return nil
		}
		varName := n.Ident[0]
		base, ok := vars[varName]
		if !ok {
			return nil
		}
		if len(n.Ident) > 1 {
			_, err := resolveFieldAccess(base, n.Ident[1:], fields)
			return err
		}
		return nil

	case *parse.ChainNode:
		if n == nil {
			return nil
		}
		if err := walkASTNode(n.Node, dot, vars, fields, funcs, tmpl, activeTemplates, visitedTemplates); err != nil {
			return err
		}
		base := resolveNodeValue(n.Node, dot, vars, fields, funcs)
		_, err := resolveFieldAccess(base, n.Field, fields)
		return err

	default:
		return nil
	}
}

// resolvePipeValue evaluates the result value of a pipeline.
func resolvePipeValue(
	pipe *parse.PipeNode,
	dot templateValue,
	vars map[string]templateValue,
	fields []tools.FieldMetadata,
	funcs template.FuncMap,
) templateValue {
	if pipe == nil || len(pipe.Cmds) == 0 {
		return templateValue{kind: valueDynamic}
	}
	lastCmd := pipe.Cmds[len(pipe.Cmds)-1]
	if len(lastCmd.Args) == 0 {
		return templateValue{kind: valueDynamic}
	}
	hasPipedInput := len(pipe.Cmds) > 1

	switch first := lastCmd.Args[0].(type) {
	case *parse.IdentifierNode:
		if first.Ident == "index" {
			return resolveIndexCommand(lastCmd, pipe, dot, vars, fields, funcs)
		}
		if fn, ok := funcs[first.Ident]; ok && fn != nil {
			ft := reflect.TypeOf(fn)
			if ft.Kind() == reflect.Func && ft.NumOut() >= 1 {
				return templateValueFromReflectType(ft.Out(0))
			}
		}
		switch first.Ident {
		case "print", "printf", "println", "html", "js", "urlquery", "render":
			return templateValueFromReflectType(reflect.TypeOf(""))
		case "len":
			return templateValueFromReflectType(reflect.TypeOf(0))
		case "not", "eq", "ne", "lt", "le", "gt", "ge":
			return templateValueFromReflectType(reflect.TypeOf(false))
		default:
			return templateValue{kind: valueDynamic}
		}

	default:
		if !hasPipedInput && len(lastCmd.Args) == 1 {
			return resolveNodeValue(first, dot, vars, fields, funcs)
		}
		receiverVal := resolveNodeValue(first, dot, vars, fields, funcs)
		if receiverVal.kind == valueReflectType && receiverVal.rtype.Kind() == reflect.Func {
			ft := receiverVal.rtype
			if ft.NumOut() >= 1 {
				return templateValueFromReflectType(ft.Out(0))
			}
		}
		return receiverVal
	}
}

// resolveNodeValue evaluates an individual node into its corresponding templateValue.
func resolveNodeValue(
	node parse.Node,
	dot templateValue,
	vars map[string]templateValue,
	fields []tools.FieldMetadata,
	funcs template.FuncMap,
) templateValue {
	switch n := node.(type) {
	case nil:
		return templateValue{kind: valueDynamic}

	case *parse.DotNode:
		return dot

	case *parse.FieldNode:
		if n == nil || len(n.Ident) == 0 {
			return dot
		}
		val, err := resolveFieldAccess(dot, n.Ident, fields)
		if err != nil {
			return templateValue{kind: valueDynamic}
		}
		return val

	case *parse.VariableNode:
		if n == nil || len(n.Ident) == 0 {
			return templateValue{kind: valueDynamic}
		}
		base, ok := vars[n.Ident[0]]
		if !ok {
			return templateValue{kind: valueDynamic}
		}
		if len(n.Ident) > 1 {
			val, err := resolveFieldAccess(base, n.Ident[1:], fields)
			if err != nil {
				return templateValue{kind: valueDynamic}
			}
			return val
		}
		return base

	case *parse.ChainNode:
		if n == nil {
			return templateValue{kind: valueDynamic}
		}
		base := resolveNodeValue(n.Node, dot, vars, fields, funcs)
		val, err := resolveFieldAccess(base, n.Field, fields)
		if err != nil {
			return templateValue{kind: valueDynamic}
		}
		return val

	case *parse.PipeNode:
		return resolvePipeValue(n, dot, vars, fields, funcs)

	case *parse.StringNode:
		return templateValueFromReflectType(reflect.TypeOf(""))

	case *parse.BoolNode:
		return templateValueFromReflectType(reflect.TypeOf(false))

	case *parse.NumberNode:
		if n.IsInt {
			return templateValueFromReflectType(reflect.TypeOf(int(0)))
		}
		if n.IsFloat {
			return templateValueFromReflectType(reflect.TypeOf(float64(0)))
		}
		return templateValueFromReflectType(reflect.TypeOf(0))

	default:
		return templateValue{kind: valueDynamic}
	}
}

// resolveIndexCommand resolves the result of an index invocation.
func resolveIndexCommand(
	lastCmd *parse.CommandNode,
	pipe *parse.PipeNode,
	dot templateValue,
	vars map[string]templateValue,
	fields []tools.FieldMetadata,
	funcs template.FuncMap,
) templateValue {
	if lastCmd == nil || len(lastCmd.Args) < 2 {
		return templateValue{kind: valueDynamic}
	}
	collNode := lastCmd.Args[1]
	keyNodes := lastCmd.Args[2:]

	if pipe != nil && len(pipe.Cmds) > 1 {
		prevCmd := pipe.Cmds[len(pipe.Cmds)-2]
		if len(prevCmd.Args) > 0 {
			keyNodes = append([]parse.Node{prevCmd.Args[0]}, keyNodes...)
		}
	}

	collVal := resolveNodeValue(collNode, dot, vars, fields, funcs)
	switch collVal.kind {
	case valueFieldsPath:
		targetPath := append([]string(nil), collVal.path...)
		for _, arg := range keyNodes {
			switch a := arg.(type) {
			case *parse.StringNode:
				targetPath = append(targetPath, a.Text)
			default:
				targetPath = append(targetPath, "*")
			}
		}
		return templateValue{kind: valueFieldsPath, path: targetPath}

	case valueReflectType:
		cur := collVal.rtype
		for cur != nil && cur.Kind() == reflect.Ptr {
			cur = cur.Elem()
		}
		for range keyNodes {
			if cur == nil {
				return templateValue{kind: valueDynamic}
			}
			switch cur.Kind() { //nolint:exhaustive // Only slice, array, and map support indexing.
			case reflect.Slice, reflect.Array:
				cur = cur.Elem()
			case reflect.Map:
				cur = cur.Elem()
			default:
				return templateValue{kind: valueDynamic}
			}
		}
		return templateValueFromReflectType(cur)

	case valueDynamic:
		return templateValue{kind: valueDynamic}
	}
	return templateValue{kind: valueDynamic}
}

// resolveFieldAccess validates that accessing idents on base is valid and returns the resulting templateValue.
func resolveFieldAccess(base templateValue, idents []string, fields []tools.FieldMetadata) (templateValue, error) {
	if len(idents) == 0 {
		return base, nil
	}
	switch base.kind {
	case valueDynamic:
		return templateValue{kind: valueDynamic}, nil

	case valueFieldsPath:
		fullPath := append(append([]string(nil), base.path...), idents...)
		if err := validateFieldPath(fullPath, fields); err != nil {
			return templateValue{}, err
		}
		return templateValue{kind: valueFieldsPath, path: fullPath}, nil

	case valueReflectType:
		cur := base.rtype
		for _, seg := range idents {
			// Check methods on cur first (including pointer receiver if cur is a pointer),
			// then dereference pointers.
			if m, ok := cur.MethodByName(seg); ok && m.Type.NumOut() >= 1 {
				cur = m.Type.Out(0)
				continue
			}
			for cur != nil && cur.Kind() == reflect.Ptr {
				cur = cur.Elem()
			}
			if cur == nil || cur.Kind() == reflect.Interface {
				return templateValue{kind: valueDynamic}, nil
			}
			switch cur.Kind() { //nolint:exhaustive // Structs and maps have named members; other types checked for methods.
			case reflect.Struct:
				if sf, ok := cur.FieldByName(seg); ok && sf.IsExported() {
					cur = sf.Type
					continue
				}
				if m, ok := cur.MethodByName(seg); ok && m.Type.NumOut() >= 1 {
					cur = m.Type.Out(0)
					continue
				}
				if m, ok := reflect.PointerTo(cur).MethodByName(seg); ok && m.Type.NumOut() >= 1 {
					cur = m.Type.Out(0)
					continue
				}
				return templateValue{}, fmt.Errorf("field %q not declared in type %s", seg, cur)

			case reflect.Map:
				if cur.Key().Kind() == reflect.String || cur.Key().Kind() == reflect.Interface {
					cur = cur.Elem()
					continue
				}
				return templateValue{}, fmt.Errorf("field %q not declared in type %s", seg, cur)

			default:
				if m, ok := cur.MethodByName(seg); ok && m.Type.NumOut() >= 1 {
					cur = m.Type.Out(0)
					continue
				}
				return templateValue{}, fmt.Errorf("field %q not declared in type %s", seg, cur)
			}
		}
		return templateValueFromReflectType(cur), nil

	default:
		return templateValue{kind: valueDynamic}, nil
	}
}

// templateValueFromReflectType wraps a Go reflect.Type into a templateValue.
func templateValueFromReflectType(t reflect.Type) templateValue {
	if t == nil || t.Kind() == reflect.Interface {
		return templateValue{kind: valueDynamic}
	}
	return templateValue{
		kind:  valueReflectType,
		rtype: t,
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

// copyVars shallow-copies the template variable map.
func copyVars(vars map[string]templateValue) map[string]templateValue {
	cp := make(map[string]templateValue, len(vars))
	for k, v := range vars {
		cp[k] = v
	}
	return cp
}

// hasExecutableNodes reports whether the node contains any executable AST node.
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
