// Package shared builds named Goa types used by protocol generators.
package shared

import (
	"maps"
	"slices"

	"goa.design/goa/v3/expr"
)

type (
	// ProtocolExprBuilderBase creates and reuses named Goa types while a
	// protocol service is being built.
	ProtocolExprBuilderBase struct {
		types map[string]*expr.UserTypeExpr
	}
)

// NewProtocolExprBuilderBase creates a new base expression builder.
func NewProtocolExprBuilderBase() *ProtocolExprBuilderBase {
	return &ProtocolExprBuilderBase{
		types: make(map[string]*expr.UserTypeExpr),
	}
}

// CollectUserTypes returns all user types referenced by the protocol service
// sorted by name so generated files remain stable.
func (b *ProtocolExprBuilderBase) CollectUserTypes() []expr.UserType {
	keys := slices.Sorted(maps.Keys(b.types))
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
