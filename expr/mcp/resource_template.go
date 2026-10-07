// Package mcp validates parameterized resource declarations before generation.
// URI templates describe discovery and completion; one service method receives
// the exact resource URI and decides whether the resource exists and is allowed.
package mcp

import (
	"errors"
	"mime"

	"github.com/yosida95/uritemplate/v3"
	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

type (
	// ResourceTemplateExpr advertises a URI template served by a typed reader.
	ResourceTemplateExpr struct {
		eval.Expression
		// Name identifies the template in discovery.
		Name string
		// URI is the RFC 6570 template used by the client to construct a URI.
		URI string
		// MimeType hints at the resource content type in discovery.
		MimeType string
		// Description explains the available resources to the client.
		Description string
		// Method receives the exact URI and returns its typed resource contents.
		Method *expr.MethodExpr
	}
)

// EvalName identifies the template in design errors.
func (r *ResourceTemplateExpr) EvalName() string {
	return "MCP resource template " + r.Name
}

// Validate rejects declarations that cannot preserve the URI or returned content.
func (r *ResourceTemplateExpr) Validate() error {
	verr := new(eval.ValidationErrors)
	if r.Name == "" {
		verr.Add(r, "resource template name is required")
	}
	template, err := uritemplate.New(r.URI)
	if err != nil || r.URI == "" {
		verr.Add(r, "invalid RFC 6570 resource template %q", r.URI)
	} else if len(template.Varnames()) == 0 {
		verr.Add(r, "resource template must declare at least one variable; use Resource for a fixed URI")
	}
	if _, _, err := mime.ParseMediaType(r.MimeType); err != nil {
		verr.Add(r, "resource template MIME type is invalid")
	}
	if r.Method == nil {
		verr.Add(r, "resource template method is required")
		return verr
	}
	if r.Method.IsStreaming() {
		verr.Add(r, "resource template method must be unary")
	}
	arguments, argumentErr := mcpinput.Arguments(r.Method)
	if argumentErr != nil {
		verr.Add(r, "%s", argumentErr.Error())
		return verr
	}
	var payload *expr.Object
	if hasValue(arguments) {
		payload = expr.AsObject(arguments.Type)
	}
	if payload == nil || len(*payload) != 1 || payload.Attribute("uri") == nil || !isPrimitive(payload.Attribute("uri").Type, expr.String) || !arguments.IsRequired("uri") {
		verr.Add(r, "resource template payload must contain only a required uri string")
	}
	completed, resultErr := mcpinput.CompleteResult(r.Method)
	if resultErr != nil {
		verr.Add(r, "%s", resultErr.Error())
		return verr
	}
	var result *expr.Object
	if hasValue(completed) {
		result = expr.AsObject(completed.Type)
	}
	if result == nil || len(*result) != 1 || result.Attribute("contents") == nil {
		verr.Add(r, "resource template result must contain only contents")
	} else {
		contents := result.Attribute("contents")
		array := expr.AsArray(contents.Type)
		bounds := expr.EffectiveValidation(contents)
		if array == nil || !array.NonNullableElems {
			verr.Add(r, "resource contents must use ArrayOfRequired")
		} else {
			if completed.IsRequired("contents") && (bounds == nil || bounds.MinLength == nil || *bounds.MinLength < 1) {
				verr.Add(r, "required resource contents must declare MinLength(1); make contents optional when an empty resource is valid")
			}
			item := expr.AsObject(array.ElemType.Type)
			if item == nil || len(*item) != 1 || item.Attribute("content") == nil || !array.ElemType.IsRequired("content") || expr.AsUnion(item.Attribute("content").Type) == nil {
				verr.Add(r, "each resource item must declare only a required content OneOf")
			}
		}
	}
	if len(verr.Errors) > 0 {
		return verr
	}
	return nil
}

// validateResourceTemplates ensures URI interpretation has one owner instead of
// choosing the first matching template when addresses overlap or lose variables.
func (m *MCPExpr) validateResourceTemplates(verr *eval.ValidationErrors) {
	seen := make(map[string]bool, len(m.ResourceTemplates))
	var reader *expr.MethodExpr
	for _, template := range m.ResourceTemplates {
		if seen[template.URI] {
			verr.Add(template, "resource template URI is used more than once")
		}
		seen[template.URI] = true
		if reader != nil && reader != template.Method {
			verr.Add(template, "all resource templates in one service must use the same reader method")
		}
		reader = template.Method
		if err := template.Validate(); err != nil {
			var validation *eval.ValidationErrors
			if errors.As(err, &validation) {
				verr.Merge(validation)
			}
		}
	}
}
