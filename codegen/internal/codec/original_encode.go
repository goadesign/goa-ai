// Package codec preserves authored JSON omission rules when complete service
// values are converted to the private pointer types used for strict decoding.
package codec

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"goa.design/goa-ai/codegen/internal/jsonshape"
	"goa.design/goa/v3/expr"
)

// originalEncodeGuard leaves an optional scalar absent when its authored JSON
// tag omits empty values and its default is already the Go zero value. Other
// fields retain their existing conversion, including explicit pointer values.
func originalEncodeGuard(source *expr.AttributeExpr, sourceVar string, required, sourcePointer bool) (string, bool) {
	if required || sourcePointer || source.DefaultValue == nil {
		return "", false
	}
	primitive, ok := jsonshape.PrimitiveType(source)
	if !ok || !reflect.ValueOf(source.DefaultValue).IsZero() {
		return "", false
	}
	tag := strings.Split(strings.Join(source.Meta["struct:tag:json"], ","), ",")
	if len(tag) < 2 || !slices.Contains(tag[1:], "omitempty") {
		return "", false
	}
	var zero string
	switch primitive {
	case expr.Boolean:
		zero = "false"
	case expr.String:
		zero = `""`
	case expr.Int, expr.Int32, expr.Int64, expr.UInt, expr.UInt32, expr.UInt64, expr.Float32, expr.Float64:
		zero = "0"
	case expr.Bytes, expr.Any:
		return "", false
	}
	return fmt.Sprintf("if %s != %s {\n", sourceVar, zero), true
}
