// Package jsonshape reads raw JSON metadata to identify values whose number text
// and open members survive generated validation and conversion without base64 encoding.
package jsonshape

import (
	"go/ast"
	"go/parser"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

// IsRawJSON recognizes only an explicit encoding/json.RawMessage representation.
// Named types inherit that representation unless their occurrence overrides it.
func IsRawJSON(attribute *expr.AttributeExpr) bool {
	if name, imported := codegen.GetMetaType(attribute); name != "" {
		primitive, ok := PrimitiveType(attribute)
		if !ok || (primitive != expr.Any && primitive != expr.Bytes) || imported == nil || imported.Path != "encoding/json" {
			return false
		}
		declared, err := parser.ParseExpr(name)
		if err != nil {
			return false
		}
		selector, ok := declared.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "RawMessage" {
			return false
		}
		qualifier, ok := selector.X.(*ast.Ident)
		alias := imported.Name
		if alias == "" {
			alias = "json"
		}
		return ok && qualifier.Name == alias
	}
	if named, ok := attribute.Type.(expr.UserType); ok {
		return IsRawJSON(named.Attribute())
	}
	return false
}
