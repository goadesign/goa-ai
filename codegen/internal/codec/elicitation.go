// Package codec converts MCP's form and URL answers into authored Goa unions.
// The external response is validated before decoding its declared fields, so
// permitted extension fields cannot hide invalid or nested form content.
package codec

import (
	"fmt"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

type (
	// elicitationCodec records a statically known answer contract. Its form field
	// distinguishes form input from URL consent without form content.
	elicitationCodec struct {
		form bool
	}
)

// AddElicitation plans an MCP answer decoder using the native accept, decline
// and cancel union. The generator selects form input or URL consent; the
// private JSON union uses MCP's action field without changing the service type.
// Generated Goa validation checks the declared fields after the MCP shape check.
func (p *Plan) AddElicitation(key, preferredName string, attribute *expr.AttributeExpr, layout *codegen.GoTypePlan, form bool) (*Value, error) {
	if expr.AsUnion(attribute.Type) == nil {
		return nil, fmt.Errorf("elicitation answer must be a Goa union")
	}
	profile := &elicitationCodec{form: form}
	if err := p.requireImport(codegen.NewImport("jsonv2", "encoding/json/v2")); err != nil {
		return nil, err
	}
	if err := p.requireImport(codegen.NewImport("mcpruntime", "goa.design/goa-ai/runtime/mcp")); err != nil {
		return nil, err
	}
	return p.add(key, preferredName, attribute, layout, DecodeOnly, profile)
}
