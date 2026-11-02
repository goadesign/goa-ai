// Package tmplcheck provides static type checking for Go text/template ASTs.
package tmplcheck

import (
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
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
// When rootType is nil, dynamic root values are allowed while structural
// AST invariants (such as defined named template targets) are still checked.
func Check(parsed *template.Template, rootType Type, funcs template.FuncMap) error {
	if parsed == nil || parsed.Root == nil {
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
		case *knownTruthType:
			return true, v.truth
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
	collType = withoutKnownTruth(collType)
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

// envStateKey constructs a canonical string key representing the variable bindings and their static type IDs.
func envStateKey(env *varEnv) string {
	if env == nil {
		return ""
	}
	names := make([]string, 0, len(env.names))
	for name := range env.names {
		names = append(names, name)
	}
	slices.Sort(names)
	var b strings.Builder
	for _, name := range names {
		id := typeID(env.types[env.names[name]])
		b.WriteString(strconv.Itoa(len(name)))
		b.WriteByte(':')
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(strconv.Itoa(len(id)))
		b.WriteByte(':')
		b.WriteString(id)
		b.WriteByte(';')
	}
	return b.String()
}

// isNilDataType reports whether t represents untyped nil data.
func isNilDataType(t Type) bool {
	if t == nil {
		return false
	}
	_, ok := withoutKnownTruth(t).(nilDataType)
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
				nextDead, _, _, err := c.walkFlow(child, dot, deadEnv.clone())
				if err != nil {
					return nil, nil, nil, err
				}
				if nextDead != nil {
					deadEnv = nextDead
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
		_, err := c.evalCommand(n, dot, env, false, nil)
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

		bodyDot := elemType

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
			return cleanScope(env, rangeEnv), nil, nil, nil

		case known && exact > 0:
			if n.ElseList != nil {
				if _, _, _, err := c.walkFlow(n.ElseList, dot, elseEnv.clone()); err != nil {
					return nil, nil, nil, err
				}
			}
			var allBreaks []*varEnv
			nextIterEnv := rangeEnv.clone()
			seenPreIdx := make(map[string]int)
			var seenPostEnvs []*varEnv
			for iter := 0; iter < exact && nextIterEnv != nil; iter++ {
				curIterEnv := nextIterEnv.clone()
				bindRangeDecls(curIterEnv)
				key := envStateKey(curIterEnv)
				if prevIdx, found := seenPreIdx[key]; found {
					cycleLen := iter - prevIdx
					targetIdx := prevIdx + (exact-1-prevIdx)%cycleLen
					nextIterEnv = seenPostEnvs[targetIdx].clone()
					break
				}
				seenPreIdx[key] = iter
				var stepFall *varEnv
				var stepBreaks, stepContinues []*varEnv
				if n.List != nil {
					stepFall, stepBreaks, stepContinues, err = c.walkFlow(n.List, bodyDot, curIterEnv.clone())
					if err != nil {
						return nil, nil, nil, err
					}
				} else {
					stepFall = curIterEnv.clone()
				}
				allBreaks = append(allBreaks, stepBreaks...)
				nextIterEnv = mergeEnvSlice(rangeEnv, append([]*varEnv{stepFall}, stepContinues...))
				seenPostEnvs = append(seenPostEnvs, nextIterEnv.clone())
			}
			postEnv := mergeEnvSlice(rangeEnv, append([]*varEnv{nextIterEnv}, allBreaks...))
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
				return mergeTwoEnvs(env, bodyExitEnv, elseFall), elseBreaks, elseContinues, nil
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

	sub := c.tmpl.Lookup(name)
	if sub == nil || sub.Root == nil {
		return fmt.Errorf("template %q is not defined", name)
	}
	c.activeCalls[key] = true
	subEnv := c.newVarEnv(pipeDot)
	_, _, _, err := c.walkFlow(sub.Root, pipeDot, subEnv)
	delete(c.activeCalls, key)
	if err != nil {
		return err
	}
	c.visitedCalls[key] = true
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
