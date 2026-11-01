// Package tmplcheck provides static type checking for Go text/template ASTs.
package tmplcheck

import (
	"math"
	"reflect"
	"text/template"
	"text/template/parse"
)

type (
	// checker coordinates static type checking across a parsed template AST.
	checker struct {
		tmpl         *template.Template
		funcs        template.FuncMap
		activeCalls  map[templateCallKey]bool
		visitedCalls map[templateCallKey]bool
		nextVarID    int
	}

	// templateCallKey uniquely identifies a template call by target name and dot type.
	templateCallKey struct {
		name   string
		typeID string
	}

	// varEnv tracks lexical variable bindings and flow-sensitive static types.
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
	if rt, ok := rootType.(*reflectType); ok && rt.t == reflect.TypeFor[reflect.Value]() {
		rootType = nil
	}

	c := &checker{
		tmpl:         parsed,
		funcs:        funcs,
		activeCalls:  make(map[templateCallKey]bool),
		visitedCalls: make(map[templateCallKey]bool),
	}

	env := c.newVarEnv(rootType)
	_, _, _, err := c.walkFlow(parsed.Root, rootType, env)
	return err
}

// staticTruth evaluates whether a node and its static type have statically known truthiness.
func staticTruth(node parse.Node, typ Type) (known bool, truth bool) {
	if typ != nil {
		switch v := typ.(type) {
		case *reflectType:
			if v.knownTruth != nil {
				return true, *v.knownTruth
			}
		case *multiType:
			if v.knownTruth != nil {
				return true, *v.knownTruth
			}
		}
	}
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

// rangeIterBound determines if a collection has a statically known exact iteration count.
func rangeIterBound(pipe *parse.PipeNode, collType Type) (exact int, known bool) {
	if isNilDataType(collType) {
		return 0, true
	}
	if rt, ok := collType.(*reflectType); ok && rt.t != nil {
		cur := deref(rt.t)
		if cur != nil && cur.Kind() == reflect.Array {
			return cur.Len(), true
		}
	}
	if pipe != nil && len(pipe.Cmds) == 1 && len(pipe.Cmds[0].Args) == 1 {
		if num, ok := pipe.Cmds[0].Args[0].(*parse.NumberNode); ok {
			if num.IsInt {
				if num.Int64 <= 0 {
					return 0, true
				}
				if num.Int64 > math.MaxInt {
					return 0, false
				}
				return int(num.Int64), true
			}
			if num.IsUint {
				if num.Uint64 > math.MaxInt {
					return 0, false
				}
				return int(num.Uint64), true
			}
		}
	}
	return 0, false
}

// cleanScope projects resultEnv onto baseEnv's lexical scope, preserving mutations
// to outer variables while discarding locally declared variables that shadowed outer names.
func cleanScope(baseEnv, resultEnv *varEnv) *varEnv {
	if resultEnv == nil || baseEnv == nil {
		return resultEnv
	}
	res := baseEnv.clone()
	for id := range baseEnv.types {
		if t, ok := resultEnv.types[id]; ok {
			res.types[id] = t
		}
	}
	return res
}

// mergeTwoEnvs merges two optional fall environments using baseEnv as the domain.
func mergeTwoEnvs(baseEnv, a, b *varEnv) *varEnv {
	if a == nil && b == nil {
		return nil
	}
	if a == nil {
		return cleanScope(baseEnv, b)
	}
	if b == nil {
		return cleanScope(baseEnv, a)
	}
	res := baseEnv.clone()
	for id := range baseEnv.types {
		res.types[id] = mergeTypes(a.types[id], b.types[id])
	}
	return res
}

// mergeEnvSlice merges a slice of optional environments using baseEnv as the domain.
func mergeEnvSlice(baseEnv *varEnv, envs []*varEnv) *varEnv {
	var nonNil []*varEnv
	for _, e := range envs {
		if e != nil {
			nonNil = append(nonNil, e)
		}
	}
	if len(nonNil) == 0 {
		return nil
	}
	res := baseEnv.clone()
	for id := range baseEnv.types {
		merged := nonNil[0].types[id]
		for _, e := range nonNil[1:] {
			merged = mergeTypes(merged, e.types[id])
		}
		res.types[id] = merged
	}
	return res
}

// envsEqual reports whether two variable environments bind all variables to identical static type IDs.
func envsEqual(a, b *varEnv) bool {
	if a == nil || b == nil {
		return a == b
	}
	if len(a.names) != len(b.names) {
		return false
	}
	for name, idA := range a.names {
		idB, ok := b.names[name]
		if !ok {
			return false
		}
		if typeID(a.types[idA]) != typeID(b.types[idB]) {
			return false
		}
	}
	return true
}

// isNilDataType reports whether t represents untyped nil data.
func isNilDataType(t Type) bool {
	if t == nil {
		return false
	}
	_, ok := t.(nilDataType)
	return ok
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

// walkFlow traverses a template AST node with control-flow propagation for break/continue.
func (c *checker) walkFlow(node parse.Node, dot Type, env *varEnv) (fallEnv *varEnv, breakEnvs []*varEnv, continueEnvs []*varEnv, err error) {
	switch n := node.(type) {
	case nil:
		return env, nil, nil, nil

	case *parse.BreakNode:
		return nil, []*varEnv{env.clone()}, nil, nil

	case *parse.ContinueNode:
		return nil, nil, []*varEnv{env.clone()}, nil

	case *parse.ListNode:
		if n == nil {
			return env, nil, nil, nil
		}
		curEnv := env
		var deadEnv *varEnv
		for _, child := range n.Nodes {
			if curEnv != nil {
				nextFall, bEnvs, cEnvs, err := c.walkFlow(child, dot, curEnv)
				if err != nil {
					return nil, nil, nil, err
				}
				breakEnvs = append(breakEnvs, bEnvs...)
				continueEnvs = append(continueEnvs, cEnvs...)
				if nextFall == nil {
					deadEnv = curEnv.clone()
					curEnv = nil
				} else {
					curEnv = nextFall
				}
			} else {
				// Dead tail after unconditional break/continue.
				if _, _, _, err := c.walkFlow(child, dot, deadEnv.clone()); err != nil {
					return nil, nil, nil, err
				}
			}
		}
		return curEnv, breakEnvs, continueEnvs, nil

	case *parse.ActionNode:
		if n == nil || n.Pipe == nil {
			return env, nil, nil, nil
		}
		_, err := c.evalPipe(n.Pipe, dot, env)
		if err != nil {
			return nil, nil, nil, err
		}
		return env, nil, nil, nil

	case *parse.PipeNode:
		_, err := c.evalPipe(n, dot, env)
		if err != nil {
			return nil, nil, nil, err
		}
		return env, nil, nil, nil

	case *parse.CommandNode:
		_, err := c.evalCommand(n, dot, env, nil)
		if err != nil {
			return nil, nil, nil, err
		}
		return env, nil, nil, nil

	case *parse.IfNode:
		if n == nil {
			return env, nil, nil, nil
		}
		return c.walkBranch(false, n.Pipe, n.List, n.ElseList, dot, env)

	case *parse.WithNode:
		if n == nil {
			return env, nil, nil, nil
		}
		return c.walkBranch(true, n.Pipe, n.List, n.ElseList, dot, env)

	case *parse.RangeNode:
		if n == nil {
			return env, nil, nil, nil
		}
		rangeEnv := env.clone()
		collType, err := c.evalPipe(n.Pipe, dot, rangeEnv)
		if err != nil {
			return nil, nil, nil, err
		}
		twoVars := n.Pipe != nil && len(n.Pipe.Decl) > 1
		var keyType, elemType Type
		if collType != nil {
			keyType, elemType, err = collType.Range(twoVars)
			if err != nil {
				return nil, nil, nil, err
			}
		}

		bodyDot := dot
		if n.Pipe == nil || len(n.Pipe.Decl) == 0 {
			bodyDot = elemType
		}

		rangeVarIDs := make(map[string]int)
		bindRangeDecls := func(targetEnv *varEnv) {
			if n.Pipe != nil && len(n.Pipe.Decl) > 0 {
				bindVar := func(name string, t Type) {
					if len(name) == 0 {
						return
					}
					id, ok := rangeVarIDs[name]
					if !ok {
						if n.Pipe.IsAssign {
							if existingID, exists := targetEnv.names[name]; exists {
								id = existingID
							} else {
								id = c.nextVarID
								c.nextVarID++
							}
						} else {
							id = c.nextVarID
							c.nextVarID++
						}
						rangeVarIDs[name] = id
					}
					targetEnv.names[name] = id
					targetEnv.types[id] = t
				}
				if len(n.Pipe.Decl) == 1 {
					if len(n.Pipe.Decl[0].Ident) > 0 {
						bindVar(n.Pipe.Decl[0].Ident[0], elemType)
					}
				} else if len(n.Pipe.Decl) >= 2 {
					if len(n.Pipe.Decl[0].Ident) > 0 {
						bindVar(n.Pipe.Decl[0].Ident[0], keyType)
					}
					if len(n.Pipe.Decl[1].Ident) > 0 {
						bindVar(n.Pipe.Decl[1].Ident[0], elemType)
					}
				}
			}
		}

		listEnv := rangeEnv.clone()
		bindRangeDecls(listEnv)
		elseEnv := rangeEnv.clone()

		exact, known := rangeIterBound(n.Pipe, collType)
		switch {
		case known && exact == 0:
			if n.List != nil {
				if _, _, _, err := c.walkFlow(n.List, bodyDot, listEnv.clone()); err != nil {
					return nil, nil, nil, err
				}
			}
			if n.ElseList != nil {
				elseFall, elseBreaks, elseContinues, err := c.walkFlow(n.ElseList, dot, elseEnv)
				return cleanScope(env, elseFall), elseBreaks, elseContinues, err
			}
			return env, nil, nil, nil

		case known && exact == 1:
			var fall1 *varEnv
			var breaks1, continues1 []*varEnv
			if n.List != nil {
				fall1, breaks1, continues1, err = c.walkFlow(n.List, bodyDot, listEnv.clone())
				if err != nil {
					return nil, nil, nil, err
				}
			} else {
				fall1 = listEnv.clone()
			}
			if n.ElseList != nil {
				if _, _, _, err := c.walkFlow(n.ElseList, dot, elseEnv.clone()); err != nil {
					return nil, nil, nil, err
				}
			}
			postEnv := mergeEnvSlice(rangeEnv, append(append([]*varEnv{fall1}, continues1...), breaks1...))
			return cleanScope(env, postEnv), nil, nil, nil

		default:
			var fall1 *varEnv
			var breaks1, continues1 []*varEnv
			if n.List != nil {
				fall1, breaks1, continues1, err = c.walkFlow(n.List, bodyDot, listEnv.clone())
				if err != nil {
					return nil, nil, nil, err
				}
			} else {
				fall1 = listEnv.clone()
			}
			allBreaks := append([]*varEnv(nil), breaks1...)
			nextIterEnv := mergeEnvSlice(rangeEnv, append([]*varEnv{fall1}, continues1...))
			if nextIterEnv != nil {
				for {
					candidate := rangeEnv.clone()
					for id := range rangeEnv.types {
						candidate.types[id] = mergeTypes(listEnv.types[id], nextIterEnv.types[id])
					}
					bindRangeDecls(candidate)
					if envsEqual(listEnv, candidate) {
						break
					}
					listEnv = candidate
					stepFall, stepBreaks, stepContinues, err := c.walkFlow(n.List, bodyDot, listEnv.clone())
					if err != nil {
						return nil, nil, nil, err
					}
					allBreaks = append(allBreaks, stepBreaks...)
					nextIterEnv = mergeEnvSlice(rangeEnv, append([]*varEnv{stepFall}, stepContinues...))
					if nextIterEnv == nil {
						break
					}
				}
			}
			bodyExitEnv := mergeEnvSlice(rangeEnv, append([]*varEnv{nextIterEnv}, allBreaks...))
			if n.ElseList != nil {
				elseFall, elseBreaks, elseContinues, err := c.walkFlow(n.ElseList, dot, elseEnv)
				if err != nil {
					return nil, nil, nil, err
				}
				if known && exact > 1 {
					return cleanScope(env, bodyExitEnv), nil, nil, nil
				}
				return mergeTwoEnvs(env, bodyExitEnv, elseFall), elseBreaks, elseContinues, nil
			}
			if known && exact > 1 {
				return cleanScope(env, bodyExitEnv), nil, nil, nil
			}
			return mergeTwoEnvs(env, bodyExitEnv, rangeEnv), nil, nil, nil
		}

	case *parse.BranchNode:
		return env, nil, nil, nil

	case *parse.TemplateNode:
		if n == nil {
			return env, nil, nil, nil
		}
		var pipeDot Type = nilDataType{}
		if n.Pipe != nil {
			var err error
			pipeDot, err = c.evalPipe(n.Pipe, dot, env)
			if err != nil {
				return nil, nil, nil, err
			}
		}
		if err := c.walkNamedTemplate(n.Name, pipeDot); err != nil {
			return nil, nil, nil, err
		}
		return env, nil, nil, nil

	case *parse.FieldNode, *parse.VariableNode, *parse.ChainNode:
		_, err := c.evalNode(n, dot, env)
		if err != nil {
			return nil, nil, nil, err
		}
		return env, nil, nil, nil

	default:
		return env, nil, nil, nil
	}
}

// walkBranch manages variable scoping and branch merging for IfNode and WithNode.
func (c *checker) walkBranch(isWith bool, pipe *parse.PipeNode, list, elseList *parse.ListNode, dot Type, env *varEnv) (*varEnv, []*varEnv, []*varEnv, error) {
	pipeEnv := env.clone()
	pipeVal, err := c.evalPipe(pipe, dot, pipeEnv)
	if err != nil {
		return nil, nil, nil, err
	}

	branchDot := dot
	if isWith {
		branchDot = pipeVal
	}

	var thenFall *varEnv
	var thenBreaks, thenContinues []*varEnv
	if list != nil {
		thenFall, thenBreaks, thenContinues, err = c.walkFlow(list, branchDot, pipeEnv.clone())
		if err != nil {
			return nil, nil, nil, err
		}
	} else {
		thenFall = pipeEnv.clone()
	}

	var elseFall *varEnv
	var elseBreaks, elseContinues []*varEnv
	if elseList != nil {
		elseFall, elseBreaks, elseContinues, err = c.walkFlow(elseList, dot, pipeEnv.clone())
		if err != nil {
			return nil, nil, nil, err
		}
	} else {
		elseFall = pipeEnv.clone()
	}

	known, truth := staticTruth(pipe, pipeVal)
	switch {
	case known && truth:
		return cleanScope(env, thenFall), thenBreaks, thenContinues, nil
	case known && !truth:
		return cleanScope(env, elseFall), elseBreaks, elseContinues, nil
	default:
		mergedFall := mergeTwoEnvs(env, thenFall, elseFall)
		allBreaks := append([]*varEnv(nil), thenBreaks...)
		allBreaks = append(allBreaks, elseBreaks...)
		allContinues := append([]*varEnv(nil), thenContinues...)
		allContinues = append(allContinues, elseContinues...)
		return mergedFall, allBreaks, allContinues, nil
	}
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
		_, _, _, err := c.walkFlow(sub.Root, pipeDot, subEnv)
		delete(c.activeCalls, key)
		if err != nil {
			return err
		}
		c.visitedCalls[key] = true
	}
	return nil
}

// evalPipe evaluates pipeline declarations and commands, updating the variable environment.
func (c *checker) evalPipe(pipe *parse.PipeNode, dot Type, env *varEnv) (Type, error) {
	if pipe == nil {
		return nil, nil
	}

	var pipedVal Type
	for _, cmd := range pipe.Cmds {
		res, err := c.evalCommand(cmd, dot, env, pipedVal)
		if err != nil {
			return nil, err
		}
		pipedVal = res
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
	if isIdent && (firstIdent.Ident == "and" || firstIdent.Ident == "or") {
		if _, ok := c.funcs[firstIdent.Ident]; !ok {
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
		if _, ok := c.funcs[firstIdent.Ident]; ok {
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
						return unwrapFuncReturn(out), nil
					}
				}
			}
			return nil, nil

		case "print", "printf", "println", "html", "js", "urlquery", "render":
			return ReflectType(reflect.TypeFor[string]()), nil

		case "len":
			return ReflectType(reflect.TypeFor[int]()), nil

		case "not":
			if len(argTypes) >= 2 {
				argNode := cmd.Args[1]
				argType := argTypes[1]
				if known, truth := staticTruth(argNode, argType); known {
					return withKnownTruth(ReflectType(reflect.TypeFor[bool]()), !truth), nil
				}
			}
			return ReflectType(reflect.TypeFor[bool]()), nil

		case "eq", "ne", "lt", "le", "gt", "ge":
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
	if pipedVal != nil {
		ops = append(ops, operand{piped: true})
	}
	for _, a := range args {
		ops = append(ops, operand{node: a})
	}

	if len(ops) == 0 {
		return nil, nil
	}

	curEnv := env
	var resultTypes []Type
	var forkEnvs []*varEnv
	shortCircuited := false
	totalOperands := len(ops)

	for i, op := range ops {
		if shortCircuited {
			// Validate dead operand in a discarded clone.
			if op.node != nil {
				discardEnv := curEnv.clone()
				if _, err := c.evalNode(op.node, dot, discardEnv); err != nil {
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

	result := mergeTypeList(resultTypes)
	if shortCircuited {
		return withKnownTruth(result, isOr), nil
	}
	if len(forkEnvs) == 0 && len(ops) > 0 {
		return withKnownTruth(result, !isOr), nil
	}
	return result, nil
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
		return withKnownTruth(ReflectType(reflect.TypeFor[string]()), len(n.Text) > 0), nil

	case *parse.BoolNode:
		return withKnownTruth(ReflectType(reflect.TypeFor[bool]()), n.True), nil

	case *parse.NumberNode:
		truth := (n.IsInt && n.Int64 != 0) || (n.IsUint && n.Uint64 != 0) || (n.IsFloat && n.Float64 != 0) || (n.IsComplex && n.Complex128 != 0)
		if n.IsInt {
			return withKnownTruth(ReflectType(reflect.TypeFor[int]()), truth), nil
		}
		if n.IsUint {
			return withKnownTruth(ReflectType(reflect.TypeFor[uint]()), truth), nil
		}
		if n.IsFloat {
			return withKnownTruth(ReflectType(reflect.TypeFor[float64]()), truth), nil
		}
		return withKnownTruth(ReflectType(reflect.TypeFor[int]()), truth), nil

	default:
		return nil, nil
	}
}

// resolveFuncIdent checks registered template functions and returns the primary return type.
func (c *checker) resolveFuncIdent(ident string) Type {
	if fn, ok := c.funcs[ident]; ok && fn != nil {
		ft := reflect.TypeOf(fn)
		if ft.Kind() == reflect.Func {
			if out, valid := validMethodReturn(ft); valid {
				return unwrapFuncReturn(out)
			}
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
