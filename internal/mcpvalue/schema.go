// Package mcpvalue builds shared host-input and content leaf schemas before Goa
// evaluates a service. Public value declarations and MCP generators use the same
// fields, including tests that construct protocol types without running DSL.
package mcpvalue

import (
	"strconv"

	"goa.design/goa/v3/dsl"
	"goa.design/goa/v3/expr"
)

// Define registers an already constructed value schema with Goa. Its optional
// DSL function adds native conversions after the fields are available.
func Define(name, pkg string, attribute *expr.AttributeExpr, fn func()) expr.UserType {
	for index, field := range *expr.AsObject(attribute.Type) {
		if field.Attribute.Meta == nil {
			field.Attribute.Meta = make(expr.MetaExpr)
		}
		field.Attribute.Meta["rpc:tag"] = []string{strconv.Itoa(index + 1)}
	}
	value := dsl.Type(name, attribute.Type, func() {
		dsl.Meta("struct:pkg:path", pkg)
		if fn != nil {
			fn()
		}
	})
	value.Attribute().Validation = attribute.Validation
	value.Attribute().Description = attribute.Description
	return value
}

// FormParams declares the exact message and open flat JSON schema of a form.
func FormParams() *expr.AttributeExpr {
	return &expr.AttributeExpr{Type: &expr.Object{
		{Name: "message", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Non-sensitive information requested from the user"}},
		{Name: "requestedSchema", Attribute: &expr.AttributeExpr{Type: expr.Any, Description: "The flat form schema generated from accepted answer content", Meta: expr.MetaExpr{"struct:field:type": {"json.RawMessage", "encoding/json"}}}},
	}, Validation: &expr.ValidationExpr{Required: []string{"message", "requestedSchema"}}}
}

// URLParams declares the exact message and address of a consent request.
func URLParams() *expr.AttributeExpr {
	return &expr.AttributeExpr{Type: &expr.Object{
		{Name: "message", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "The external interaction the user is asked to approve"}},
		{Name: "url", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Absolute URL opened by the consenting host", Validation: &expr.ValidationExpr{Format: expr.FormatURI}}},
	}, Validation: &expr.ValidationExpr{Required: []string{"message", "url"}}}
}

// Annotations declares the existing content audience, importance and time hints.
func Annotations() *expr.AttributeExpr {
	zero, one := float64(0), float64(1)
	return &expr.AttributeExpr{Type: &expr.Object{
		{Name: "audience", Attribute: &expr.AttributeExpr{Type: &expr.Array{ElemType: &expr.AttributeExpr{Type: expr.String, Validation: &expr.ValidationExpr{Values: []any{"user", "assistant"}}}}, Description: "Roles that should see this content"}},
		{Name: "priority", Attribute: &expr.AttributeExpr{Type: expr.Float64, Description: "Importance from zero through one, inclusive, for this content item", Validation: &expr.ValidationExpr{Minimum: &zero, Maximum: &one}}},
		{Name: "lastModified", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Time this content last changed", Meta: expr.MetaExpr{"struct:field:external": {"LastModified"}}}},
	}}
}

// Icon declares an image reference without fetching or interpreting its address.
func Icon() *expr.AttributeExpr {
	return &expr.AttributeExpr{Type: &expr.Object{
		{Name: "src", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "URI of the icon; decoding does not fetch it", Validation: &expr.ValidationExpr{Format: expr.FormatURI}}},
		{Name: "mimeType", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Optional image MIME type", Meta: expr.MetaExpr{"struct:field:external": {"MIMEType"}, "struct:field:name": {"MIMEType"}}}},
		{Name: "sizes", Attribute: &expr.AttributeExpr{Type: &expr.Array{ElemType: &expr.AttributeExpr{Type: expr.String}}, Description: "Suggested dimensions or any for scalable icons"}},
		{Name: "theme", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Optional light or dark display theme", Validation: &expr.ValidationExpr{Values: []any{"light", "dark"}}}},
	}, Validation: &expr.ValidationExpr{Required: []string{"src"}}}
}
