// Package mcp checks methods that supply suggestions for a declared prompt
// argument. The service receives partial text and prior arguments, and returns
// ordered suggestions. Protocol references stay in the generated adapter.
package mcp

import (
	"errors"

	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

// EvalName identifies the prompt and argument in design errors.
func (c *PromptCompletionExpr) EvalName() string {
	return "MCP completion for " + c.Prompt + "." + c.Argument
}

// Validate rejects methods that cannot retain completion input and output.
// Empty suggestions are valid; the protocol caps each response at 100 values.
func (c *PromptCompletionExpr) Validate() error {
	if c.Prompt == "" || c.Argument == "" {
		verr := new(eval.ValidationErrors)
		verr.Add(c, "completion requires a prompt name and argument name")
		return verr
	}
	return validateCompletionMethod(c, c.Method)
}

// validateCompletionMethod checks partial input and ordered output for both
// prompt and resource suggestions, preserving the same per-reply bound.
func validateCompletionMethod(owner eval.Expression, method *expr.MethodExpr) error {
	verr := new(eval.ValidationErrors)
	if method == nil {
		verr.Add(owner, "completion method is required")
		return verr
	}
	if method.IsStreaming() {
		verr.Add(owner, "completion method must be unary")
	}
	var payload *expr.Object
	if hasValue(method.Payload) {
		payload = expr.AsObject(method.Payload.Type)
	}
	if payload == nil {
		verr.Add(owner, "completion payload must contain required value and optional arguments")
	} else {
		value := payload.Attribute("value")
		if value == nil || !isPrimitive(value.Type, expr.String) || !method.Payload.IsRequired("value") {
			verr.Add(owner, "completion payload value must be a required string")
		}
		if payload.Attribute("arguments") == nil {
			verr.Add(owner, "completion payload must declare optional arguments")
		}
		for _, field := range *payload {
			if field.Name == "value" {
				continue
			}
			if field.Name != "arguments" {
				verr.Add(owner, "unsupported completion payload field %q", field.Name)
				continue
			}
			arguments := expr.AsMap(field.Attribute.Type)
			if arguments == nil || !isPrimitive(arguments.KeyType.Type, expr.String) || !isPrimitive(arguments.ElemType.Type, expr.String) || method.Payload.IsRequired(field.Name) {
				verr.Add(owner, "completion arguments must be an optional map of strings")
			}
		}
	}
	var result *expr.Object
	if hasValue(method.Result) {
		result = expr.AsObject(method.Result.Type)
	}
	if result == nil {
		verr.Add(owner, "completion result must contain a values array")
	} else {
		values := result.Attribute("values")
		if values == nil || expr.AsArray(values.Type) == nil || !isPrimitive(expr.AsArray(values.Type).ElemType.Type, expr.String) {
			verr.Add(owner, "completion values must be an array of strings")
		} else {
			bounds := expr.EffectiveValidation(values)
			if bounds == nil || bounds.MaxLength == nil || *bounds.MaxLength > 100 {
				verr.Add(owner, "completion values must declare MaxLength(100) or a stricter bound")
			}
		}
		for _, field := range *result {
			switch field.Name {
			case "values":
			case "total":
				if !isPrimitive(field.Attribute.Type, expr.Int64) {
					verr.Add(owner, "completion total must be Int64")
				}
			case "hasMore":
				if !isPrimitive(field.Attribute.Type, expr.Boolean) {
					verr.Add(owner, "completion hasMore must be Boolean")
				}
			default:
				verr.Add(owner, "unsupported completion result field %q", field.Name)
			}
		}
	}
	if len(verr.Errors) > 0 {
		return verr
	}
	return nil
}

// validatePromptCompletions rejects duplicate bindings and unknown arguments.
// It runs after all prompt declarations exist, so declaration order is irrelevant.
func (m *MCPExpr) validatePromptCompletions(verr *eval.ValidationErrors) {
	seen := make(map[[2]string]bool, len(m.PromptCompletions))
	for _, completion := range m.PromptCompletions {
		key := [2]string{completion.Prompt, completion.Argument}
		if seen[key] {
			verr.Add(completion, "completion binding is used more than once")
		}
		seen[key] = true
		var selected *MethodPromptExpr
		for _, prompt := range m.MethodPrompts {
			if prompt.Name == completion.Prompt {
				selected = prompt
				break
			}
		}
		if selected == nil || selected.Method == nil || !hasValue(selected.Method.Payload) || expr.AsObject(selected.Method.Payload.Type) == nil || expr.AsObject(selected.Method.Payload.Type).Attribute(completion.Argument) == nil {
			verr.Add(completion, "completion must select a declared prompt argument")
		}
		if err := completion.Validate(); err != nil {
			var validation *eval.ValidationErrors
			if errors.As(err, &validation) {
				verr.Merge(validation)
			} else {
				verr.Add(completion, "%s", err.Error())
			}
		}
	}
}
