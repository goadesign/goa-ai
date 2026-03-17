// Package shared provides common infrastructure for protocol code generation
// shared between MCP implementations.
package shared

import (
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

type (
	// ProtocolExprBuilderBase provides common expression building functionality
	// shared between MCP protocol implementations.
	ProtocolExprBuilderBase struct {
		types                   map[string]*expr.UserTypeExpr
		insertKeys              []string // tracks insertion order for deterministic iteration
		pendingValidationErrors []error
	}
)

// NewProtocolExprBuilderBase creates a new base expression builder.
func NewProtocolExprBuilderBase() *ProtocolExprBuilderBase {
	return &ProtocolExprBuilderBase{
		types: make(map[string]*expr.UserTypeExpr),
	}
}

// PrepareAndValidate runs Prepare, Validate, and Finalize on the provided root
// without mutating the global Goa expr.Root to keep generation reentrant.
func (b *ProtocolExprBuilderBase) PrepareAndValidate(root *expr.RootExpr) error {
	// Temporarily set global expr.Root so Goa validations that reference it
	// resolve services and servers correctly against this temporary root.
	originalRoot := expr.Root
	originalErrors := cloneEvalErrors(eval.Context.Errors)
	pendingErrors := b.consumePendingValidationErrors()
	expr.Root = root
	defer func() {
		expr.Root = originalRoot
		eval.Context.Errors = originalErrors
	}()

	// Step 1: Prepare
	prepareSet := func(set eval.ExpressionSet) {
		for _, def := range set {
			if p, ok := def.(eval.Preparer); ok {
				p.Prepare()
			}
		}
	}
	prepareSet(eval.ExpressionSet{root})
	root.WalkSets(prepareSet)

	// Step 2: Validate
	validateSet := func(set eval.ExpressionSet) {
		errors := &eval.ValidationErrors{}
		for _, def := range set {
			if validate, ok := def.(eval.Validator); ok {
				if err := validate.Validate(); err != nil {
					errors.AddError(def, err)
				}
			}
		}
		if len(errors.Errors) > 0 {
			eval.Context.Record(&eval.Error{GoError: errors})
		}
	}
	validateSet(eval.ExpressionSet{root})
	root.WalkSets(validateSet)

	var validationErrors eval.MultiError
	if len(eval.Context.Errors) > len(originalErrors) {
		validationErrors = cloneEvalErrors(eval.Context.Errors[len(originalErrors):])
	}
	if len(pendingErrors) > 0 {
		return appendPendingValidationErrors(validationErrors, pendingErrors)
	}
	if len(validationErrors) > 0 {
		return validationErrors
	}

	// Step 3: Finalize
	finalizeSet := func(set eval.ExpressionSet) {
		for _, def := range set {
			if f, ok := def.(eval.Finalizer); ok {
				f.Finalize()
			}
		}
	}
	finalizeSet(eval.ExpressionSet{root})
	root.WalkSets(finalizeSet)

	return nil
}

// RecordValidationError registers a generation-time validation failure that must
// be returned by PrepareAndValidate even when it is discovered before Goa's
// eval.Context snapshot begins.
func (b *ProtocolExprBuilderBase) RecordValidationError(err error) {
	if err == nil {
		return
	}
	b.pendingValidationErrors = append(b.pendingValidationErrors, err)
}

func cloneEvalErrors(errors eval.MultiError) eval.MultiError {
	if errors == nil {
		return nil
	}
	cloned := make(eval.MultiError, len(errors))
	copy(cloned, errors)
	return cloned
}

func appendPendingValidationErrors(existing eval.MultiError, pending []error) eval.MultiError {
	merged := cloneEvalErrors(existing)
	for _, err := range pending {
		merged = append(merged, &eval.Error{GoError: err})
	}
	return merged
}

func (b *ProtocolExprBuilderBase) consumePendingValidationErrors() []error {
	if len(b.pendingValidationErrors) == 0 {
		return nil
	}
	pending := append([]error(nil), b.pendingValidationErrors...)
	b.pendingValidationErrors = nil
	return pending
}

// CollectUserTypes returns all user types referenced by the protocol service
// in a deterministic order for stable code generation. The order is based on
// insertion order, then sorted alphabetically using insertion sort.
func (b *ProtocolExprBuilderBase) CollectUserTypes() []expr.UserType {
	keys := make([]string, 0, len(b.types))
	for k := range b.types {
		keys = append(keys, k)
	}
	// Simple insertion sort for deterministic ordering
	for i := 1; i < len(keys); i++ {
		j := i
		for j > 0 && keys[j-1] > keys[j] {
			keys[j-1], keys[j] = keys[j], keys[j-1]
			j--
		}
	}
	out := make([]expr.UserType, 0, len(keys))
	for _, k := range keys {
		out = append(out, b.types[k])
	}
	return out
}

// GetOrCreateType retrieves or creates a named user type used by the protocol model.
func (b *ProtocolExprBuilderBase) GetOrCreateType(name string, builder func() *expr.AttributeExpr) *expr.UserTypeExpr {
	if t, ok := b.types[name]; ok {
		return t
	}

	t := &expr.UserTypeExpr{
		TypeName:      name,
		AttributeExpr: builder(),
	}
	b.types[name] = t
	b.insertKeys = append(b.insertKeys, name)
	return t
}

// UserTypeAttr returns an attribute that references the user type with the
// given name. This ensures downstream codegen treats the payload/result as a
// user type instead of inlining the underlying object.
func (b *ProtocolExprBuilderBase) UserTypeAttr(name string, builder func() *expr.AttributeExpr) *expr.AttributeExpr {
	return &expr.AttributeExpr{Type: b.GetOrCreateType(name, builder)}
}

// Types returns the internal types map for direct access when needed.
func (b *ProtocolExprBuilderBase) Types() map[string]*expr.UserTypeExpr {
	return b.types
}
