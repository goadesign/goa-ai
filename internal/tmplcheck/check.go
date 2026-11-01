// Package tmplcheck provides static type checking for Go text/template ASTs.
package tmplcheck

import (
	"reflect"
	"text/template"
	"text/template/parse"
)

type (
	// checker manages static template type checking state.
	checker struct {
		tmpl         *template.Template
		funcs        template.FuncMap
		activeCalls  map[templateCallKey]bool
		visitedCalls map[templateCallKey]bool
		invokedNames map[string]bool
	}

	// templateCallKey tracks named template invocations by template name and input type identity.
	templateCallKey struct {
		name   string
		typeID string
	}
)

// Check validates parsed template ASTs starting from rootType.
// When rootType is nil, no static type constraints are checked.
func Check(parsed *template.Template, rootName string, rootType Type, funcs template.FuncMap) error {
	if parsed == nil || rootType == nil {
		return nil
	}

	c := &checker{
		tmpl:         parsed,
		funcs:        funcs,
		activeCalls:  make(map[templateCallKey]bool),
		visitedCalls: make(map[templateCallKey]bool),
		invokedNames: make(map[string]bool),
	}

	c.invokedNames[rootName] = true
	if parsed.Name() != "" {
		c.invokedNames[parsed.Name()] = true
	}

	root := parsed.Root
	if !hasExecutableNodes(root) {
		if named := parsed.Lookup(rootName); named != nil && named.Root != nil {
			root = named.Root
		}
	}

	if root != nil {
		vars := map[string]Type{
			"$": rootType,
		}
		if err := c.walk(root, rootType, vars); err != nil {
			return err
		}
	}

	// Walk any remaining defined templates in parsed.Templates() not invoked via {{ template "..." }} with rootType.
	for _, t := range parsed.Templates() {
		if t.Root == nil || c.invokedNames[t.Name()] {
			continue
		}
		vars := map[string]Type{
			"$": rootType,
		}
		if err := c.walk(t.Root, rootType, vars); err != nil {
			return err
		}
	}

	return nil
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

// copyVars returns a shallow copy of the variables map.
func copyVars(vars map[string]Type) map[string]Type {
	cp := make(map[string]Type, len(vars))
	for k, v := range vars {
		cp[k] = v
	}
	return cp
}

// walk recursively traverses a template AST node and validates member and index accesses against static types.
func (c *checker) walk(node parse.Node, dot Type, vars map[string]Type) error {
	switch n := node.(type) {
	case nil:
		return nil

	case *parse.ListNode:
		if n == nil {
			return nil
		}
		for _, child := range n.Nodes {
			if err := c.walk(child, dot, vars); err != nil {
				return err
			}
		}
		return nil

	case *parse.ActionNode:
		if n == nil || n.Pipe == nil {
			return nil
		}
		if err := c.walk(n.Pipe, dot, vars); err != nil {
			return err
		}
		if len(n.Pipe.Decl) > 0 {
			pipeVal := c.resolvePipe(n.Pipe, dot, vars)
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
		var pipedVal Type
		for _, cmd := range n.Cmds {
			if err := c.walkCommand(cmd, dot, vars, pipedVal); err != nil {
				return err
			}
			pipedVal = c.resolveCommand(cmd, dot, vars, pipedVal)
		}
		return nil

	case *parse.CommandNode:
		return c.walkCommand(n, dot, vars, nil)

	case *parse.IfNode:
		if n == nil {
			return nil
		}
		return c.walkBranch(n.Pipe, n.List, n.ElseList, dot, dot, vars)

	case *parse.WithNode:
		if n == nil {
			return nil
		}
		var listDot Type
		if n.Pipe != nil {
			listDot = c.resolvePipe(n.Pipe, dot, vars)
		}
		return c.walkBranch(n.Pipe, n.List, n.ElseList, dot, listDot, vars)

	case *parse.RangeNode:
		if n == nil {
			return nil
		}
		if n.Pipe != nil {
			if err := c.walk(n.Pipe, dot, vars); err != nil {
				return err
			}
		}
		var collType Type
		if n.Pipe != nil {
			collType = c.resolvePipe(n.Pipe, dot, vars)
		}
		var keyType, elemType Type
		if collType != nil {
			var err error
			keyType, elemType, err = collType.Range()
			if err != nil {
				return err
			}
		}
		if n.Pipe != nil && n.Pipe.IsAssign {
			for _, decl := range n.Pipe.Decl {
				if len(decl.Ident) > 0 {
					vars[decl.Ident[0]] = collType
				}
			}
		}
		listVars := copyVars(vars)
		if n.Pipe != nil && len(n.Pipe.Decl) > 0 {
			if len(n.Pipe.Decl) == 1 {
				if len(n.Pipe.Decl[0].Ident) > 0 {
					listVars[n.Pipe.Decl[0].Ident[0]] = elemType
				}
			} else if len(n.Pipe.Decl) >= 2 {
				if len(n.Pipe.Decl[0].Ident) > 0 {
					listVars[n.Pipe.Decl[0].Ident[0]] = keyType
				}
				if len(n.Pipe.Decl[1].Ident) > 0 {
					listVars[n.Pipe.Decl[1].Ident[0]] = elemType
				}
			}
		}
		if n.List != nil {
			if err := c.walk(n.List, elemType, listVars); err != nil {
				return err
			}
		}
		elseVars := copyVars(vars)
		if n.Pipe != nil && len(n.Pipe.Decl) > 0 {
			for _, decl := range n.Pipe.Decl {
				if len(decl.Ident) > 0 {
					elseVars[decl.Ident[0]] = collType
				}
			}
		}
		if n.ElseList != nil {
			if err := c.walk(n.ElseList, dot, elseVars); err != nil {
				return err
			}
		}
		c.mergeBranchVars(vars, listVars, elseVars, n.ElseList != nil)
		return nil

	case *parse.BranchNode:
		if n == nil {
			return nil
		}
		if n.Pipe != nil {
			if err := c.walk(n.Pipe, dot, vars); err != nil {
				return err
			}
		}
		if n.List != nil {
			if err := c.walk(n.List, dot, copyVars(vars)); err != nil {
				return err
			}
		}
		if n.ElseList != nil {
			if err := c.walk(n.ElseList, dot, copyVars(vars)); err != nil {
				return err
			}
		}
		return nil

	case *parse.TemplateNode:
		if n == nil {
			return nil
		}
		if n.Pipe != nil {
			if err := c.walk(n.Pipe, dot, vars); err != nil {
				return err
			}
		}
		return c.walkNamedTemplate(n.Name, dot, vars, n.Pipe)

	case *parse.FieldNode:
		if n == nil || len(n.Ident) == 0 {
			return nil
		}
		cur := dot
		for _, ident := range n.Ident {
			if cur == nil {
				return nil
			}
			next, err := cur.Member(ident)
			if err != nil {
				return err
			}
			cur = next
		}
		return nil

	case *parse.VariableNode:
		if n == nil || len(n.Ident) == 0 {
			return nil
		}
		base := vars[n.Ident[0]]
		cur := base
		for _, ident := range n.Ident[1:] {
			if cur == nil {
				return nil
			}
			next, err := cur.Member(ident)
			if err != nil {
				return err
			}
			cur = next
		}
		return nil

	case *parse.ChainNode:
		if n == nil {
			return nil
		}
		if err := c.walk(n.Node, dot, vars); err != nil {
			return err
		}
		cur := c.resolveNode(n.Node, dot, vars)
		for _, field := range n.Field {
			if cur == nil {
				return nil
			}
			next, err := cur.Member(field)
			if err != nil {
				return err
			}
			cur = next
		}
		return nil

	default:
		return nil
	}
}

// walkBranch manages variable scoping and branch merging for IfNode and WithNode.
func (c *checker) walkBranch(pipe *parse.PipeNode, list, elseList *parse.ListNode, outerDot, listDot Type, vars map[string]Type) error {
	if pipe != nil {
		if err := c.walk(pipe, outerDot, vars); err != nil {
			return err
		}
	}
	var pipeVal Type
	if pipe != nil {
		pipeVal = c.resolvePipe(pipe, outerDot, vars)
	}
	if pipe != nil && pipe.IsAssign {
		for _, decl := range pipe.Decl {
			if len(decl.Ident) > 0 {
				vars[decl.Ident[0]] = pipeVal
			}
		}
	}
	branchVars := copyVars(vars)
	if pipe != nil && len(pipe.Decl) > 0 {
		for _, decl := range pipe.Decl {
			if len(decl.Ident) > 0 {
				branchVars[decl.Ident[0]] = pipeVal
			}
		}
	}
	thenVars := copyVars(branchVars)
	if list != nil {
		if err := c.walk(list, listDot, thenVars); err != nil {
			return err
		}
	}
	elseVars := copyVars(branchVars)
	if elseList != nil {
		if err := c.walk(elseList, outerDot, elseVars); err != nil {
			return err
		}
	}
	c.mergeBranchVars(vars, thenVars, elseVars, elseList != nil)
	return nil
}

// walkCommand validates field accesses and index operations inside one pipeline command.
func (c *checker) walkCommand(cmd *parse.CommandNode, dot Type, vars map[string]Type, pipedVal Type) error {
	if cmd == nil || len(cmd.Args) == 0 {
		return nil
	}
	if ident, ok := cmd.Args[0].(*parse.IdentifierNode); ok && ident.Ident == "index" {
		if len(cmd.Args) >= 2 {
			coll := c.resolveNode(cmd.Args[1], dot, vars)
			keyOps := len(cmd.Args) - 2
			if pipedVal != nil {
				keyOps++
			}
			for i := 0; i < keyOps; i++ {
				if coll == nil {
					break
				}
				elem, err := coll.Index()
				if err != nil {
					return err
				}
				coll = elem
			}
		}
	}
	for _, arg := range cmd.Args {
		if err := c.walk(arg, dot, vars); err != nil {
			return err
		}
	}
	return nil
}

// walkNamedTemplate validates an invoked named template, tracking (name, typeID) pairs for cycle detection.
func (c *checker) walkNamedTemplate(name string, dot Type, vars map[string]Type, pipe *parse.PipeNode) error {
	c.invokedNames[name] = true
	var pipeDot Type = nilDataType{}
	if pipe != nil {
		pipeDot = c.resolvePipe(pipe, dot, vars)
	}

	key := templateCallKey{
		name:   name,
		typeID: typeID(pipeDot),
	}
	if c.activeCalls[key] {
		// Cycle detected with the same input type; stop recursion.
		return nil
	}
	if c.visitedCalls[key] {
		return nil
	}

	if sub := c.tmpl.Lookup(name); sub != nil && sub.Root != nil {
		c.activeCalls[key] = true
		subVars := map[string]Type{
			"$": pipeDot,
		}
		err := c.walk(sub.Root, pipeDot, subVars)
		delete(c.activeCalls, key)
		if err != nil {
			return err
		}
		c.visitedCalls[key] = true
	}
	return nil
}

// resolvePipe evaluates the static type result of a pipeline.
func (c *checker) resolvePipe(pipe *parse.PipeNode, dot Type, vars map[string]Type) Type {
	if pipe == nil || len(pipe.Cmds) == 0 {
		return nil
	}
	var pipedVal Type
	for _, cmd := range pipe.Cmds {
		pipedVal = c.resolveCommand(cmd, dot, vars, pipedVal)
	}
	return pipedVal
}

// resolveCommand evaluates the static type result of one command in a pipeline.
func (c *checker) resolveCommand(cmd *parse.CommandNode, dot Type, vars map[string]Type, pipedVal Type) Type {
	if cmd == nil || len(cmd.Args) == 0 {
		return nil
	}

	switch first := cmd.Args[0].(type) {
	case *parse.IdentifierNode:
		switch first.Ident {
		case "index":
			if len(cmd.Args) < 2 {
				return nil
			}
			coll := c.resolveNode(cmd.Args[1], dot, vars)
			keyOps := len(cmd.Args) - 2
			if pipedVal != nil {
				keyOps++
			}
			for i := 0; i < keyOps; i++ {
				if coll == nil {
					return nil
				}
				elem, err := coll.Index()
				if err != nil {
					return nil
				}
				coll = elem
			}
			return coll

		case "slice":
			if len(cmd.Args) >= 2 {
				return c.resolveNode(cmd.Args[1], dot, vars)
			}
			if pipedVal != nil {
				return pipedVal
			}
			return nil

		case "call":
			var fnType Type
			if len(cmd.Args) >= 2 {
				fnType = c.resolveNode(cmd.Args[1], dot, vars)
			} else if pipedVal != nil {
				fnType = pipedVal
			}
			if rt, ok := fnType.(*reflectType); ok {
				cur := deref(rt.t)
				if cur != nil && cur.Kind() == reflect.Func {
					if out, valid := validMethodReturn(cur); valid {
						return ReflectType(out)
					}
				}
			}
			return nil

		case "print", "printf", "println", "html", "js", "urlquery", "render":
			return ReflectType(reflect.TypeFor[string]())

		case "len":
			return ReflectType(reflect.TypeFor[int]())

		case "not", "eq", "ne", "lt", "le", "gt", "ge":
			return ReflectType(reflect.TypeFor[bool]())

		case "and", "or":
			var commonID string
			var resultType Type
			firstArg := true
			argsToInspect := cmd.Args[1:]
			for _, arg := range argsToInspect {
				argType := c.resolveNode(arg, dot, vars)
				id := typeID(argType)
				if firstArg {
					commonID = id
					resultType = argType
					firstArg = false
				} else if id != commonID {
					return nil
				}
			}
			if pipedVal != nil {
				if firstArg {
					return pipedVal
				}
				if typeID(pipedVal) != commonID {
					return nil
				}
			}
			if commonID == dynamicTypeID {
				return nil
			}
			return resultType

		default:
			return c.resolveFuncIdent(first.Ident)
		}

	default:
		return c.resolveNode(first, dot, vars)
	}
}

// resolveNode evaluates the static type result of an individual expression node.
func (c *checker) resolveNode(node parse.Node, dot Type, vars map[string]Type) Type {
	switch n := node.(type) {
	case nil:
		return nil

	case *parse.DotNode:
		return dot

	case *parse.FieldNode:
		if n == nil || len(n.Ident) == 0 {
			return dot
		}
		cur := dot
		for _, ident := range n.Ident {
			if cur == nil {
				return nil
			}
			next, err := cur.Member(ident)
			if err != nil {
				return nil
			}
			cur = next
		}
		return cur

	case *parse.VariableNode:
		if n == nil || len(n.Ident) == 0 {
			return nil
		}
		base := vars[n.Ident[0]]
		cur := base
		for _, ident := range n.Ident[1:] {
			if cur == nil {
				return nil
			}
			next, err := cur.Member(ident)
			if err != nil {
				return nil
			}
			cur = next
		}
		return cur

	case *parse.ChainNode:
		if n == nil {
			return nil
		}
		cur := c.resolveNode(n.Node, dot, vars)
		for _, field := range n.Field {
			if cur == nil {
				return nil
			}
			next, err := cur.Member(field)
			if err != nil {
				return nil
			}
			cur = next
		}
		return cur

	case *parse.IdentifierNode:
		if n == nil {
			return nil
		}
		return c.resolveFuncIdent(n.Ident)

	case *parse.PipeNode:
		return c.resolvePipe(n, dot, vars)

	case *parse.StringNode:
		return ReflectType(reflect.TypeFor[string]())

	case *parse.BoolNode:
		return ReflectType(reflect.TypeFor[bool]())

	case *parse.NumberNode:
		if n.IsInt {
			return ReflectType(reflect.TypeFor[int]())
		}
		if n.IsFloat {
			return ReflectType(reflect.TypeFor[float64]())
		}
		return ReflectType(reflect.TypeFor[int]())

	default:
		return nil
	}
}

// resolveFuncIdent checks registered template functions and returns the primary return type.
func (c *checker) resolveFuncIdent(ident string) Type {
	if fn, ok := c.funcs[ident]; ok && fn != nil {
		ft := reflect.TypeOf(fn)
		if ft.Kind() == reflect.Func && ft.NumOut() >= 1 {
			return ReflectType(ft.Out(0))
		}
	}
	return nil
}

// mergeBranchVars merges variable state after divergent execution branches.
func (c *checker) mergeBranchVars(outerVars, thenVars, elseVars map[string]Type, hasElse bool) {
	for k := range outerVars {
		if hasElse {
			outerVars[k] = mergeTypes(thenVars[k], elseVars[k])
		} else {
			outerVars[k] = mergeTypes(thenVars[k], outerVars[k])
		}
	}
}
