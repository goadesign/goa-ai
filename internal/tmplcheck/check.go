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
func Check(parsed *template.Template, _ string, rootType Type, funcs template.FuncMap) error {
	if parsed == nil || rootType == nil || parsed.Root == nil {
		return nil
	}

	c := &checker{
		tmpl:         parsed,
		funcs:        funcs,
		activeCalls:  make(map[templateCallKey]bool),
		visitedCalls: make(map[templateCallKey]bool),
	}

	env := c.newVarEnv(rootType)
	return c.walk(parsed.Root, rootType, env)
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

	case *parse.FieldNode, *parse.VariableNode, *parse.ChainNode:
		_, err := c.evalNode(n, dot, env)
		return err

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

// evalCommand validates arguments left-to-right (evaluating sub-pipelines) and returns the static Type of the command.
func (c *checker) evalCommand(cmd *parse.CommandNode, dot Type, env *varEnv, pipedVal Type) (Type, error) {
	if cmd == nil || len(cmd.Args) == 0 {
		return nil, nil
	}

	argTypes := make([]Type, len(cmd.Args))
	for i, arg := range cmd.Args {
		var err error
		argTypes[i], err = c.evalNode(arg, dot, env)
		if err != nil {
			return nil, err
		}
	}

	firstIdent, isIdent := cmd.Args[0].(*parse.IdentifierNode)
	if isIdent {
		if _, ok := c.funcs[firstIdent.Ident]; ok && firstIdent.Ident != "render" {
			return c.resolveFuncIdent(firstIdent.Ident), nil
		}

		switch firstIdent.Ident {
		case "index":
			if len(argTypes) < 2 {
				return nil, nil
			}
			coll := argTypes[1]
			keyOps := len(argTypes) - 2
			if pipedVal != nil {
				keyOps++
			}
			for i := 0; i < keyOps; i++ {
				if coll == nil {
					return nil, nil
				}
				elem, err := coll.Index()
				if err != nil {
					return nil, err
				}
				coll = elem
			}
			return coll, nil

		case "slice":
			var coll Type
			if len(argTypes) >= 2 {
				coll = argTypes[1]
			} else if pipedVal != nil {
				coll = pipedVal
			}
			if rt, ok := coll.(*reflectType); ok {
				cur := deref(rt.t)
				if cur != nil {
					if cur.Kind() == reflect.Array {
						return ReflectType(reflect.SliceOf(cur.Elem())), nil
					}
					return ReflectType(cur), nil
				}
			}
			return coll, nil

		case "call":
			var fnType Type
			if len(argTypes) >= 2 {
				fnType = argTypes[1]
			} else if pipedVal != nil {
				fnType = pipedVal
			}
			if rt, ok := fnType.(*reflectType); ok {
				cur := deref(rt.t)
				if cur != nil && cur.Kind() == reflect.Func {
					if out, valid := validMethodReturn(cur); valid {
						return ReflectType(out), nil
					}
				}
			}
			return nil, nil

		case "print", "printf", "println", "html", "js", "urlquery", "render":
			return ReflectType(reflect.TypeFor[string]()), nil

		case "len":
			return ReflectType(reflect.TypeFor[int]()), nil

		case "not", "eq", "ne", "lt", "le", "gt", "ge":
			return ReflectType(reflect.TypeFor[bool]()), nil

		case "and", "or":
			types := make([]Type, 0, len(argTypes)-1+1)
			types = append(types, argTypes[1:]...)
			if pipedVal != nil {
				types = append(types, pipedVal)
			}
			return mergeTypeList(types), nil

		default:
			return c.resolveFuncIdent(firstIdent.Ident), nil
		}
	}

	return argTypes[0], nil
}

// evalNode evaluates an individual expression node in a single left-to-right pass.
func (c *checker) evalNode(node parse.Node, dot Type, env *varEnv) (Type, error) {
	switch n := node.(type) {
	case nil:
		return nil, nil

	case *parse.DotNode:
		return dot, nil

	case *parse.FieldNode:
		if n == nil || len(n.Ident) == 0 {
			return dot, nil
		}
		cur := dot
		for _, ident := range n.Ident {
			if cur == nil {
				return nil, nil
			}
			next, err := cur.Member(ident)
			if err != nil {
				return nil, err
			}
			cur = next
		}
		return cur, nil

	case *parse.VariableNode:
		if n == nil || len(n.Ident) == 0 {
			return nil, nil
		}
		base := env.lookup(n.Ident[0])
		cur := base
		for _, ident := range n.Ident[1:] {
			if cur == nil {
				return nil, nil
			}
			next, err := cur.Member(ident)
			if err != nil {
				return nil, err
			}
			cur = next
		}
		return cur, nil

	case *parse.ChainNode:
		if n == nil {
			return nil, nil
		}
		cur, err := c.evalNode(n.Node, dot, env)
		if err != nil {
			return nil, err
		}
		for _, field := range n.Field {
			if cur == nil {
				return nil, nil
			}
			next, err := cur.Member(field)
			if err != nil {
				return nil, err
			}
			cur = next
		}
		return cur, nil

	case *parse.PipeNode:
		return c.evalPipe(n, dot, env)

	case *parse.IdentifierNode:
		if n == nil {
			return nil, nil
		}
		return c.resolveFuncIdent(n.Ident), nil

	case *parse.StringNode:
		return ReflectType(reflect.TypeFor[string]()), nil

	case *parse.BoolNode:
		return ReflectType(reflect.TypeFor[bool]()), nil

	case *parse.NumberNode:
		if n.IsInt {
			return ReflectType(reflect.TypeFor[int]()), nil
		}
		if n.IsFloat {
			return ReflectType(reflect.TypeFor[float64]()), nil
		}
		return ReflectType(reflect.TypeFor[int]()), nil

	default:
		return nil, nil
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
