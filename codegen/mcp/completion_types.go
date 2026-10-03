// Package codegen declares completion request and reply types for generated
// clients and servers. Goa owns typed decoding and ordinary field validation;
// the adapter checks which name belongs to the selected reference kind.
package codegen

import "goa.design/goa/v3/expr"

// buildCompletionMethod adds the request used while a user fills prompt arguments.
func (b *mcpExprBuilder) buildCompletionMethod() *expr.MethodExpr {
	return &expr.MethodExpr{
		Name:        "completion/complete",
		Description: "Return service-ranked suggestions for the argument currently being entered",
		Payload:     b.userTypeAttr("CompletionCompletePayload", b.buildCompletionPayload),
		Result:      b.userTypeAttr("CompletionCompleteResult", b.buildCompletionResult),
		Errors:      buildMCPMethodErrors(mcpDispatchErrors[:]...),
	}
}

// buildCompletionPayload retains both reference kinds and optional prior values.
// The wire reference is flat, unlike Goa's tagged union envelope.
func (b *mcpExprBuilder) buildCompletionPayload() *expr.AttributeExpr {
	reference := b.getOrCreateType("CompletionReference", func() *expr.AttributeExpr {
		return &expr.AttributeExpr{Type: &expr.Object{
			{Name: "type", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Whether the reference names a prompt or resource", Validation: &expr.ValidationExpr{Values: []any{"ref/prompt", "ref/resource"}}}},
			{Name: "name", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Prompt name when type is ref/prompt"}},
			{Name: "title", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Optional display title for a prompt reference"}},
			{Name: "uri", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Resource URI or URI template when type is ref/resource"}},
		}, Validation: &expr.ValidationExpr{Required: []string{"type"}}}
	})
	argument := b.getOrCreateType("CompletionArgument", func() *expr.AttributeExpr {
		return &expr.AttributeExpr{Type: &expr.Object{
			{Name: "name", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Declared argument currently being entered"}},
			{Name: "value", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Partial text used by the service to find suggestions"}},
		}, Validation: &expr.ValidationExpr{Required: []string{"name", "value"}}}
	})
	context := b.getOrCreateType("CompletionContext", func() *expr.AttributeExpr {
		return &expr.AttributeExpr{Type: &expr.Object{{Name: "arguments", Attribute: &expr.AttributeExpr{
			Type: &expr.Map{KeyType: &expr.AttributeExpr{Type: expr.String}, ElemType: &expr.AttributeExpr{Type: expr.String}}, Description: "Previously resolved argument values",
		}}}}
	})
	return &expr.AttributeExpr{Type: &expr.Object{
		{Name: "ref", Attribute: &expr.AttributeExpr{Type: reference, Description: "Prompt or resource whose argument is being completed"}},
		{Name: "argument", Attribute: &expr.AttributeExpr{Type: argument, Description: "Argument name and partial text"}},
		{Name: "context", Attribute: &expr.AttributeExpr{Type: context, Description: "Prior values used to refine suggestions"}},
	}, Validation: &expr.ValidationExpr{Required: []string{"ref", "argument"}}}
}

// buildCompletionResult limits values within one reply. The total number of
// matches and later requests have no limit derived from this array constraint.
func (b *mcpExprBuilder) buildCompletionResult() *expr.AttributeExpr {
	suggestion := b.getOrCreateType("CompletionSuggestion", func() *expr.AttributeExpr {
		return &expr.AttributeExpr{Type: &expr.Object{
			{Name: "values", Attribute: &expr.AttributeExpr{Type: &expr.Array{ElemType: &expr.AttributeExpr{Type: expr.String}, NonNullableElems: true}, Description: "Suggestions in service-selected relevance order", Validation: &expr.ValidationExpr{MaxLength: new(100)}}},
			{Name: "total", Attribute: &expr.AttributeExpr{Type: expr.Int64, Description: "Total available matches, which can exceed the returned count"}},
			{Name: "hasMore", Attribute: &expr.AttributeExpr{Type: expr.Boolean, Description: "Whether further matches exist"}},
		}, Validation: &expr.ValidationExpr{Required: []string{"values"}}}
	})
	return &expr.AttributeExpr{Type: &expr.Object{{Name: "completion", Attribute: &expr.AttributeExpr{Type: suggestion, Description: "Ranked suggestions for this request"}}}, Validation: &expr.ValidationExpr{Required: []string{"completion"}}}
}
