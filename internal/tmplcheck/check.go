// Package tmplcheck provides static type checking for Go text/template ASTs.
package tmplcheck

import (
	"fmt"
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
func Check(parsed *template.Template, rootType Type, funcs template.FuncMap) error {
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

// staticTruth evaluates whether a node and its static type have statically known truthiness.
func staticTruth(node parse.Node, typ Type) (known bool, truth bool) {
	if isNilDataType(typ) {
		return true, false
	}
	if rt, ok := typ.(*reflectType); ok && rt.t != nil {
		if rt.t.Kind() == reflect.Struct && rt.t != reflect.TypeFor[reflect.Value]() {
			return true, true
		}
		if rt.t.Kind() == reflect.Array {
			return true, rt.t.Len() > 0
		}
	}
	switch n := node.(type) {
	case *parse.NilNode:
		return true, false
	case *parse.BoolNode:
		return true, n.True
	case *parse.StringNode:
		return true, len(n.Text) > 0
	case *parse.NumberNode:
		return true, (n.IsInt && n.Int64 != 0) || (n.IsUint && n.Uint64 != 0) || (n.IsFloat && n.Float64 != 0) || (n.IsComplex && n.Complex128 != 0)
	case *parse.PipeNode:
		if len(n.Cmds) == 1 && len(n.Cmds[0].Args) == 1 {
			return staticTruth(n.Cmds[0].Args[0], typ)
		}
	}
	return false, false
}

// isBreakOrContinue reports whether node represents a break or continue action.
func isBreakOrContinue(node parse.Node) bool {
	switch node.(type) {
	case *parse.BreakNode, *parse.ContinueNode:
		return true
	default:
		return false
	}
}

// hasTopLevelBreak reports whether the node list directly contains a break action.
func hasTopLevelBreak(list *parse.ListNode) bool {
	if list == nil {
		return false
	}
	for _, child := range list.Nodes {
		if _, ok := child.(*parse.BreakNode); ok {
			return true
		}
	}
	return false
}

// isZeroIterationRange reports whether ranging over collType is statically known to execute 0 iterations.
func isZeroIterationRange(collType Type) bool {
	if collType == nil {
		return false
	}
	if isNilDataType(collType) {
		return true
	}
	if rt, ok := collType.(*reflectType); ok && rt.t != nil {
		cur := deref(rt.t)
		if cur != nil && cur.Kind() == reflect.Array && cur.Len() == 0 {
			return true
		}
	}
	return false
}

// isNilDataType reports whether t represents untyped nil data.
func isNilDataType(t Type) bool {
	if t == nil {
		return false
	}
	_, ok := t.(nilDataType)
	return ok
}

// isSeq2 reports whether collType is an iter.Seq2 function yielding two values.
func isSeq2(collType Type) bool {
	if collType == nil {
		return false
	}
	if rt, ok := collType.(*reflectType); ok && rt.t != nil {
		cur := deref(rt.t)
		if cur != nil && cur.Kind() == reflect.Func && cur.NumIn() == 1 && cur.NumOut() == 0 {
			yield := cur.In(0)
			return yield.Kind() == reflect.Func && yield.NumIn() == 2 && yield.NumOut() == 1 && yield.Out(0).Kind() == reflect.Bool
		}
	}
	if mt, ok := collType.(*multiType); ok {
		for _, alt := range mt.types {
			if isSeq2(alt) {
				return true
			}
		}
	}
	return false
}

// envsEqual reports whether two variable environments bind all variables to identical static type IDs.
func envsEqual(a, b *varEnv) bool {
	if a == nil || b == nil {
		return a == b
	}
	if len(a.types) != len(b.types) {
		return false
	}
	for id, ta := range a.types {
		tb, ok := b.types[id]
		if !ok || typeID(ta) != typeID(tb) {
			return false
		}
	}
	return true
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
		curEnv := env
		for _, child := range n.Nodes {
			if err := c.walk(child, dot, curEnv); err != nil {
				return err
			}
			if isBreakOrContinue(child) {
				curEnv = curEnv.clone()
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
		if n.Pipe != nil && len(n.Pipe.Decl) > 1 && collType != nil && !isNilDataType(collType) && keyType == nil && elemType != nil {
			return fmt.Errorf("cannot range over %s with two variables", collType.ID())
		}
		if (n.Pipe == nil || len(n.Pipe.Decl) <= 1) && isSeq2(collType) {
			elemType = keyType
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
			iterEnv := listEnv.clone()
			if err := c.walk(n.List, elemType, iterEnv); err != nil {
				return err
			}
			if !hasTopLevelBreak(n.List) {
				for {
					nextEnv := listEnv.clone()
					for id := range rangeEnv.types {
						nextEnv.types[id] = mergeTypes(listEnv.types[id], iterEnv.types[id])
					}
					bindRangeDecls(nextEnv)
					if envsEqual(listEnv, nextEnv) {
						break
					}
					listEnv = nextEnv
					iterEnv = listEnv.clone()
					if err := c.walk(n.List, elemType, iterEnv); err != nil {
						return err
					}
				}
			}
			listEnv = iterEnv
		}
		if n.ElseList != nil {
			if err := c.walk(n.ElseList, dot, elseEnv); err != nil {
				return err
			}
		}
		if isZeroIterationRange(collType) {
			if n.ElseList != nil {
				for id := range env.types {
					env.types[id] = elseEnv.types[id]
				}
			}
		} else {
			for id := range env.types {
				if n.ElseList != nil {
					env.types[id] = mergeTypes(listEnv.types[id], elseEnv.types[id])
				} else {
					env.types[id] = mergeTypes(listEnv.types[id], env.types[id])
				}
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
	known, truth := staticTruth(pipe, pipeVal)
	for id := range env.types {
		switch {
		case known && truth:
			env.types[id] = thenEnv.types[id]
		case known && !truth:
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

	firstIdent, isIdent := cmd.Args[0].(*parse.IdentifierNode)
	if isIdent {
		if _, ok := c.funcs[firstIdent.Ident]; ok {
			return c.resolveFuncIdent(firstIdent.Ident), nil
		}

		if firstIdent.Ident == "and" || firstIdent.Ident == "or" {
			return c.evalAndOr(firstIdent.Ident == "or", cmd.Args[1:], dot, env, pipedVal)
		}
	}

	argTypes := make([]Type, len(cmd.Args))
	for i, arg := range cmd.Args {
		var err error
		argTypes[i], err = c.evalNode(arg, dot, env)
		if err != nil {
			return nil, err
		}
	}

	if isIdent {
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

		default:
			return c.resolveFuncIdent(firstIdent.Ident), nil
		}
	}

	return argTypes[0], nil
}

// evalAndOr evaluates short-circuiting and/or expressions left-to-right.
func (c *checker) evalAndOr(isOr bool, args []parse.Node, dot Type, env *varEnv, pipedVal Type) (Type, error) {
	type operand struct {
		node  parse.Node
		piped bool
	}
	var ops []operand
	for _, arg := range args {
		ops = append(ops, operand{node: arg, piped: false})
	}
	if pipedVal != nil {
		ops = append(ops, operand{node: nil, piped: true})
	}

	curEnv := env.clone()
	var forkEnvs []*varEnv
	shortCircuited := false
	var resultTypes []Type

	totalOperands := len(ops)
	for i, op := range ops {
		if shortCircuited {
			if op.node != nil {
				deadEnv := curEnv.clone()
				if _, err := c.evalNode(op.node, dot, deadEnv); err != nil {
					return nil, err
				}
			}
			continue
		}

		var opType Type
		if op.piped {
			opType = pipedVal
		} else {
			var err error
			opType, err = c.evalNode(op.node, dot, curEnv)
			if err != nil {
				return nil, err
			}
		}

		known, truth := staticTruth(op.node, opType)
		stops := (isOr && truth) || (!isOr && !truth)
		continues := (isOr && !truth) || (!isOr && truth)
		isLast := i == totalOperands-1

		switch {
		case known && stops:
			resultTypes = append(resultTypes, opType)
			shortCircuited = true
		case known && continues:
			if isLast {
				resultTypes = append(resultTypes, opType)
			}
		default:
			resultTypes = append(resultTypes, opType)
			if !isLast {
				forkEnvs = append(forkEnvs, curEnv.clone())
			}
		}
	}

	for id := range env.types {
		merged := curEnv.types[id]
		for _, fe := range forkEnvs {
			merged = mergeTypes(merged, fe.types[id])
		}
		env.types[id] = merged
	}

	return mergeTypeList(resultTypes), nil
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
