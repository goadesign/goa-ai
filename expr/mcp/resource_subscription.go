// Package mcp checks resource subscription declarations before code generation.
// A typed Goa stream owns the accepted URI subset and subsequent changes;
// transport correlation and framing remain outside the authored service schema.
package mcp

import (
	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

type (
	// ResourceSubscriptionExpr selects one authenticated resource update stream.
	ResourceSubscriptionExpr struct {
		eval.Expression
		// Method receives requested URIs and sends acknowledgment and updates.
		Method *expr.MethodExpr
	}
)

// EvalName identifies the resource subscription source in design errors.
func (s *ResourceSubscriptionExpr) EvalName() string {
	return "MCP resource subscription source"
}

// Validate requires a server-only stream with typed input and exactly two event
// variants. Credentials remain native transport input rather than URI filters.
func (s *ResourceSubscriptionExpr) Validate() error {
	verr := new(eval.ValidationErrors)
	if s.Method == nil {
		verr.Add(s, "resource subscription method is required")
		return verr
	}
	method := s.Method
	if method.Stream != expr.ServerStreamKind || method.HasMixedResults() {
		verr.Add(s, "resource subscription method must use only StreamingResult")
	}
	arguments, err := mcpinput.Arguments(method)
	if err != nil {
		verr.Add(s, "%s", err.Error())
		return verr
	}
	validateSubscriptionResources(verr, s, arguments)
	result := method.StreamingResult
	if !hasValue(result) {
		verr.Add(s, "resource subscription stream must contain a required change OneOf")
		return verr
	}
	if _, viewed := result.Type.(*expr.ResultTypeExpr); viewed {
		verr.Add(s, "resource subscription events must not use result views")
	}
	object := expr.AsObject(result.Type)
	if object == nil || len(*object) != 1 || object.Attribute("change") == nil || !result.IsRequired("change") {
		verr.Add(s, "resource subscription stream must contain only a required change OneOf")
		return verr
	}
	choice := expr.AsUnion(object.Attribute("change").Type)
	if choice == nil || len(choice.Values) != 2 {
		verr.Add(s, "resource subscription change must declare acknowledged and updated branches")
		return verr
	}
	seen := make(map[string]bool, 2)
	for _, branch := range choice.Values {
		seen[branch.Name] = true
		switch branch.Name {
		case "acknowledged":
			validateSubscriptionResources(verr, s, branch.Attribute)
		case "updated":
			update := expr.AsObject(branch.Attribute.Type)
			if update == nil || len(*update) != 1 || update.Attribute("uri") == nil || !branch.Attribute.IsRequired("uri") || !isPrimitive(update.Attribute("uri").Type, expr.String) {
				verr.Add(s, "updated branch must contain only a required uri string")
				continue
			}
			validation := expr.EffectiveValidation(update.Attribute("uri"))
			if validation == nil || validation.Format != expr.FormatURI {
				verr.Add(s, "updated uri must declare FormatURI")
			}
		default:
			verr.Add(s, "resource subscription change has unsupported branch %q", branch.Name)
		}
	}
	if !seen["acknowledged"] || !seen["updated"] {
		verr.Add(s, "resource subscription change must declare acknowledged and updated branches")
	}
	if len(verr.Errors) > 0 {
		return verr
	}
	return nil
}

// validateSubscriptionResources checks a URI filter or accepted subset. Empty
// subsets are valid, so resources must be optional instead of required.
func validateSubscriptionResources(verr *eval.ValidationErrors, source *ResourceSubscriptionExpr, attribute *expr.AttributeExpr) {
	var object *expr.Object
	if hasValue(attribute) {
		object = expr.AsObject(attribute.Type)
	}
	if object == nil || len(*object) != 1 || object.Attribute("resources") == nil {
		verr.Add(source, "resource subscription input and acknowledgment must contain only optional resources")
		return
	}
	field := object.Attribute("resources")
	array := expr.AsArray(field.Type)
	if array == nil || !isPrimitive(array.ElemType.Type, expr.String) || attribute.IsRequired("resources") {
		verr.Add(source, "resource subscription resources must be an optional array of URI strings")
		return
	}
	validation := expr.EffectiveValidation(array.ElemType)
	if validation == nil || validation.Format != expr.FormatURI {
		verr.Add(source, "resource subscription resources elements must declare FormatURI")
	}
}
