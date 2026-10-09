// Package shared contains type queries used during protocol generation.
package shared

import "goa.design/goa/v3/expr"

// IsStringType reports whether dataType is a string or a named string.
func IsStringType(dataType expr.DataType) bool {
	switch actual := dataType.(type) {
	case expr.Primitive:
		return actual == expr.String
	case *expr.UserTypeExpr:
		return IsStringType(actual.Type)
	case *expr.ResultTypeExpr:
		return IsStringType(actual.Type)
	default:
		return false
	}
}
