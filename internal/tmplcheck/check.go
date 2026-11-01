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
		nextVarID    int
	}

	// templateCallKey tracks named template invocations by template name and input type identity.
	templateCallKey struct {
		name   string
		typeID string
	}

	// varEnv tracks lexical scope and mutable variable dataflow bindings.
	varEnv struct {
		names map[string]int // lexical scope: variable name -> binding ID
		types map[int]Type   // dataflow state: binding ID -> current static Type
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
		env := c.newVarEnv(rootType)
		if err := c.walk(root, rootType, env); err != nil {
			return err
		}
	}

	// Walk any remaining defined templates in parsed.Templates() not invoked via {{ template "..." }} with rootType.
	for _, t := range parsed.Templates() {
		if t.Root == nil || c.invokedNames[t.Name()] {
			continue
		}
		env := c.newVarEnv(rootType)
		if err := c.walk(t.Root, rootType, env); err != nil {
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

// isConstBool checks whether a pipeline consists of a single constant boolean literal.
func isConstBool(pipe *parse.PipeNode) (bool, bool) {
	if pipe == nil || len(pipe.Cmds) != 1 {
		return false, false
	}
	cmd := pipe.Cmds[0]
	if len(cmd.Args) != 1 {
		return false, false
	}
	if b, ok := cmd.Args[0].(*parse.BoolNode); ok && b != nil {
		return true, b.True
	}
	return false, false
}

// newVarEnv creates an environment initialized with root data $.
func (c *checker) newVarEnv(rootType Type) *varEnv {
	id := c.nextVarID
	c.nextVarID++
	return &varEnv{
		names: map[string]int{"$": id},
		types: map[int]Type{id: rootType},
	}
}

// declare registers a new variable binding in lexical scope, shadowing any existing binding with the same name.
func (c *checker) declare(env *varEnv, name string, t Type) {
	id := c.nextVarID
	c.nextVarID++
	env.names[name] = id
	env.types[id] = t
}

// assign mutates an existing variable binding in env, or declares it if not present.
func (c *checker) assign(env *varEnv, name string, t Type) {
	if id, ok := env.names[name]; ok {
		env.types[id] = t
		return
	}
	c.declare(env, name, t)
}

// walk recursively traverses a template AST node and validates member and index accesses against static types.
func (c *checker) walk(node parse.Node, dot Type, env *varEnv) error {
	switch n := node.(type) {
	case nil:
		return nil

	case *parse.ListNode:
		if n == nil {
			return nil
		}
		for _, child := range n.Nodes {
			if err := c.walk(child, dot, env); err != nil {
				return err
			}
		}
		return nil

	case *parse.ActionNode:
		if n == nil || n.Pipe == nil {
			return nil
		}
		_, err := c.evalPipe(n.Pipe, dot, env)
		return err

	case *parse.PipeNode:
		_, err := c.evalPipe(n, dot, env)
		return err

	case *parse.CommandNode:
		_, err := c.evalCommand(n, dot, env, nil)
		return err

	case *parse.IfNode:
		if n == nil {
			return nil
		}
		return c.walkBranch(n.Pipe, n.List, n.ElseList, dot, false, env)

	case *parse.WithNode:
		if n == nil {
			return nil
		}
		return c.walkBranch(n.Pipe, n.List, n.ElseList, dot, true, env)

	case *parse.RangeNode:
		if n == nil {
			return nil
		}
		rangeEnv := env.clone()
		collType, err := c.evalPipe(n.Pipe, dot, rangeEnv)
		if err != nil {
			return err
		}
		for id := range env.types {
			env.types[id] = rangeEnv.types[id]
		}
		var keyType, elemType Type
		if collType != nil {
			var rangeErr error
			keyType, elemType, rangeErr = collType.Range()
			if rangeErr != nil {
				return rangeErr
			}
		}
		elseEnv := rangeEnv.clone()
		listEnv := rangeEnv.clone()
		bindRangeDecls := func(targetEnv *varEnv) {
			if n.Pipe != nil && len(n.Pipe.Decl) > 0 {
				if len(n.Pipe.Decl) == 1 {
					if len(n.Pipe.Decl[0].Ident) > 0 {
						c.assign(targetEnv, n.Pipe.Decl[0].Ident[0], elemType)
					}
				} else if len(n.Pipe.Decl) >= 2 {
					if len(n.Pipe.Decl[0].Ident) > 0 {
						c.assign(targetEnv, n.Pipe.Decl[0].Ident[0], keyType)
					}
					if len(n.Pipe.Decl[1].Ident) > 0 {
						c.assign(targetEnv, n.Pipe.Decl[1].Ident[0], elemType)
					}
				}
			}
		}
		bindRangeDecls(listEnv)
		if n.List != nil {
			iter1Env := listEnv.clone()
			if err := c.walk(n.List, elemType, iter1Env); err != nil {
				return err
			}
			iter2Env := listEnv.clone()
			for id := range rangeEnv.types {
				iter2Env.types[id] = mergeTypes(listEnv.types[id], iter1Env.types[id])
			}
			bindRangeDecls(iter2Env)
			if err := c.walk(n.List, elemType, iter2Env); err != nil {
				return err
			}
			listEnv = iter2Env
		}
		if n.ElseList != nil {
			if err := c.walk(n.ElseList, dot, elseEnv); err != nil {
				return err
			}
		}
		for id := range env.types {
			if n.ElseList != nil {
				env.types[id] = mergeTypes(listEnv.types[id], elseEnv.types[id])
			} else {
				env.types[id] = mergeTypes(listEnv.types[id], env.types[id])
			}
		}
		return nil

	case *parse.BranchNode:
		return nil

	case *parse.TemplateNode:
		if n == nil {
			return nil
		}
		var pipeDot Type = nilDataType{}
		if n.Pipe != nil {
			var err error
			pipeDot, err = c.evalPipe(n.Pipe, dot, env)
			if err != nil {
				return err
			}
		}
		return c.walkNamedTemplate(n.Name, pipeDot)

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
		base := env.lookup(n.Ident[0])
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
		if err := c.walk(n.Node, dot, env); err != nil {
			return err
		}
		cur := c.resolveNode(n.Node, dot, env)
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
func (c *checker) walkBranch(pipe *parse.PipeNode, list, elseList *parse.ListNode, outerDot Type, withDot bool, env *varEnv) error {
	pipeEnv := env.clone()
	pipeVal, err := c.evalPipe(pipe, outerDot, pipeEnv)
	if err != nil {
		return err
	}
	for id := range env.types {
		env.types[id] = pipeEnv.types[id]
	}
	thenEnv := pipeEnv.clone()
	listDot := outerDot
	if withDot {
		listDot = pipeVal
	}
	if list != nil {
		if err := c.walk(list, listDot, thenEnv); err != nil {
			return err
		}
	}
	elseEnv := pipeEnv.clone()
	if elseList != nil {
		if err := c.walk(elseList, outerDot, elseEnv); err != nil {
			return err
		}
	}
	isConst, val := isConstBool(pipe)
	for id := range env.types {
		switch {
		case isConst && val:
			env.types[id] = thenEnv.types[id]
		case isConst && !val:
			if elseList != nil {
				env.types[id] = elseEnv.types[id]
			}
		case elseList != nil:
			env.types[id] = mergeTypes(thenEnv.types[id], elseEnv.types[id])
		default:
			env.types[id] = mergeTypes(thenEnv.types[id], env.types[id])
		}
	}
	return nil
}

// walkNamedTemplate validates an invoked named template, tracking (name, typeID) pairs for cycle detection.
func (c *checker) walkNamedTemplate(name string, pipeDot Type) error {
	c.invokedNames[name] = true

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
		subEnv := c.newVarEnv(pipeDot)
		err := c.walk(sub.Root, pipeDot, subEnv)
		delete(c.activeCalls, key)
		if err != nil {
			return err
		}
		c.visitedCalls[key] = true
	}
	return nil
}

// evalPipe evaluates a pipeline, updates declared/assigned variables in env, and returns the resulting static Type.
func (c *checker) evalPipe(pipe *parse.PipeNode, dot Type, env *varEnv) (Type, error) {
	if pipe == nil {
		return nil, nil
	}
	var pipedVal Type
	for _, cmd := range pipe.Cmds {
		var err error
		pipedVal, err = c.evalCommand(cmd, dot, env, pipedVal)
		if err != nil {
			return nil, err
		}
	}
	if len(pipe.Decl) > 0 {
		for _, decl := range pipe.Decl {
			if len(decl.Ident) == 0 {
				continue
			}
			if pipe.IsAssign {
				c.assign(env, decl.Ident[0], pipedVal)
			} else {
				c.declare(env, decl.Ident[0], pipedVal)
			}
		}
	}
	return pipedVal, nil
}

// evalCommand validates arguments (evaluating sub-pipelines) and returns the static Type of the command.
func (c *checker) evalCommand(cmd *parse.CommandNode, dot Type, env *varEnv, pipedVal Type) (Type, error) {
	if cmd == nil || len(cmd.Args) == 0 {
		return nil, nil
	}
	if ident, ok := cmd.Args[0].(*parse.IdentifierNode); ok && ident.Ident == "index" {
		if _, overridden := c.funcs["index"]; !overridden {
			if len(cmd.Args) >= 2 {
				coll := c.resolveNode(cmd.Args[1], dot, env)
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
						return nil, err
					}
					coll = elem
				}
			}
		}
	}
	for _, arg := range cmd.Args {
		if err := c.walk(arg, dot, env); err != nil {
			return nil, err
		}
	}
	return c.resolveCommand(cmd, dot, env, pipedVal), nil
}

// resolveCommand evaluates the static type result of one command in a pipeline.
func (c *checker) resolveCommand(cmd *parse.CommandNode, dot Type, env *varEnv, pipedVal Type) Type {
	if cmd == nil || len(cmd.Args) == 0 {
		return nil
	}

	switch first := cmd.Args[0].(type) {
	case *parse.IdentifierNode:
		if _, ok := c.funcs[first.Ident]; ok && first.Ident != "render" {
			return c.resolveFuncIdent(first.Ident)
		}

		switch first.Ident {
		case "index":
			if len(cmd.Args) < 2 {
				return nil
			}
			coll := c.resolveNode(cmd.Args[1], dot, env)
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
			var coll Type
			if len(cmd.Args) >= 2 {
				coll = c.resolveNode(cmd.Args[1], dot, env)
			} else if pipedVal != nil {
				coll = pipedVal
			}
			if rt, ok := coll.(*reflectType); ok {
				cur := deref(rt.t)
				if cur != nil {
					if cur.Kind() == reflect.Array {
						return ReflectType(reflect.SliceOf(cur.Elem()))
					}
					return ReflectType(cur)
				}
			}
			return coll

		case "call":
			var fnType Type
			if len(cmd.Args) >= 2 {
				fnType = c.resolveNode(cmd.Args[1], dot, env)
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
			var types []Type
			for _, arg := range cmd.Args[1:] {
				types = append(types, c.resolveNode(arg, dot, env))
			}
			if pipedVal != nil {
				types = append(types, pipedVal)
			}
			return mergeTypeList(types)

		default:
			return c.resolveFuncIdent(first.Ident)
		}

	default:
		return c.resolveNode(first, dot, env)
	}
}

// resolveNode evaluates the static type result of an individual expression node.
func (c *checker) resolveNode(node parse.Node, dot Type, env *varEnv) Type {
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
		base := env.lookup(n.Ident[0])
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
		cur := c.resolveNode(n.Node, dot, env)
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
		val, _ := c.evalPipe(n, dot, env)
		return val

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

// clone creates a deep copy of lexical variable bindings and types.
func (env *varEnv) clone() *varEnv {
	if env == nil {
		return nil
	}
	names := make(map[string]int, len(env.names))
	for k, v := range env.names {
		names[k] = v
	}
	types := make(map[int]Type, len(env.types))
	for k, v := range env.types {
		types[k] = v
	}
	return &varEnv{
		names: names,
		types: types,
	}
}

// lookup retrieves the Type currently bound to the named variable in env.
func (env *varEnv) lookup(name string) Type {
	if env == nil {
		return nil
	}
	id, ok := env.names[name]
	if !ok {
		return nil
	}
	return env.types[id]
}
