package tmplcheck

import (
	"fmt"
	"reflect"
	"text/template/parse"
)

// evalCall validates and resolves the return type of built-in call.
func evalCall(fnType Type) (Type, error) {
	fnType = withoutKnownTruth(fnType)
	if fnType == nil {
		return nil, nil
	}
	if isNilDataType(fnType) {
		return nil, fmt.Errorf("call of nil")
	}
	if rt, ok := fnType.(*reflectType); ok {
		if rt.t == nil || rt.t == reflect.TypeFor[reflect.Value]() || rt.t.Kind() == reflect.Interface {
			return nil, nil
		}
		if rt.t.Kind() != reflect.Func {
			return nil, fmt.Errorf("non-function of type %s", rt.t)
		}
		if out, valid := validMethodReturn(rt.t); valid {
			return ReflectType(out), nil
		}
		return nil, fmt.Errorf("function of type %s has invalid template return signature", rt.t)
	}
	if mt, ok := fnType.(*multiType); ok {
		results := make([]Type, len(mt.types))
		for i, alt := range mt.types {
			res, err := evalCall(alt)
			if err != nil {
				return nil, err
			}
			results[i] = res
		}
		return mergeTypeList(results), nil
	}
	return nil, fmt.Errorf("non-function of type %s", typeID(fnType))
}

// evalPipe evaluates pipeline declarations and commands, updating the variable environment.
func (c *checker) evalPipe(pipe *parse.PipeNode, dot Type, env *varEnv) (Type, error) {
	if pipe == nil {
		return nil, nil
	}

	var pipedVal Type
	for i, cmd := range pipe.Cmds {
		res, err := c.evalCommand(cmd, dot, env, i > 0, pipedVal)
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
func (c *checker) evalCommand(cmd *parse.CommandNode, dot Type, env *varEnv, hasPiped bool, pipedVal Type) (Type, error) {
	if cmd == nil || len(cmd.Args) == 0 {
		return nil, nil
	}

	firstIdent, isIdent := cmd.Args[0].(*parse.IdentifierNode)
	if isIdent && (firstIdent.Ident == "and" || firstIdent.Ident == "or") {
		if _, ok := c.funcs[firstIdent.Ident]; !ok {
			return c.evalAndOr(firstIdent.Ident == "or", cmd.Args[1:], dot, env, hasPiped, pipedVal)
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
			args := append([]Type(nil), argTypes[1:]...)
			if hasPiped {
				args = append(args, pipedVal)
			}
			if len(args) == 0 {
				return nil, nil
			}
			coll := args[0]
			for i := 1; i < len(args); i++ {
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
			} else if hasPiped {
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
			hasFn := false
			if len(argTypes) >= 2 {
				fnType = argTypes[1]
				hasFn = true
			} else if hasPiped {
				fnType = pipedVal
				hasFn = true
			}
			if !hasFn {
				return nil, fmt.Errorf("wrong number of args for call: want at least 1 got 0")
			}
			return evalCall(fnType)

		case "print", "printf", "println", "html", "js", "urlquery", "render":
			return ReflectType(reflect.TypeFor[string]()), nil

		case "len":
			return ReflectType(reflect.TypeFor[int]()), nil

		case "not":
			if len(argTypes) >= 2 {
				if known, truth := staticTruth(cmd.Args[1], argTypes[1]); known {
					return withKnownTruth(ReflectType(reflect.TypeFor[bool]()), !truth), nil
				}
			} else if len(argTypes) == 1 && hasPiped {
				if known, truth := staticTruth(nil, pipedVal); known {
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
func (c *checker) evalAndOr(isOr bool, args []parse.Node, dot Type, env *varEnv, hasPiped bool, pipedVal Type) (Type, error) {
	type operand struct {
		node  parse.Node
		piped bool
	}
	var ops []operand
	for _, a := range args {
		ops = append(ops, operand{node: a})
	}
	if hasPiped {
		ops = append(ops, operand{piped: true})
	}

	if len(ops) == 0 {
		return nil, nil
	}

	curEnv := env
	var resultTypes []Type
	var forkEnvs []*varEnv
	shortCircuited := false
	allKnownContinue := true
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
		opType = unwrapAndOrOperand(opType)

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
			allKnownContinue = false
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
	if allKnownContinue && len(ops) > 0 {
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

	case *parse.NilNode:
		return nilDataType{}, nil

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

	case *parse.PipeNode:
		return c.evalPipe(n, dot, env)

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

// resolveFuncIdent checks for registered functions and resolves their return Type.
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
