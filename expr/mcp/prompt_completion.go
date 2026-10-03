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
	verr := new(eval.ValidationErrors)
	if c.Prompt == "" || c.Argument == "" {
		verr.Add(c, "completion requires a prompt name and argument name")
	}
	if c.Method == nil {
		verr.Add(c, "completion method is required")
		return verr
	}
	if c.Method.IsStreaming() {
		verr.Add(c, "completion method must be unary")
	}
	var payload *expr.Object
	if hasValue(c.Method.Payload) {
		payload = expr.AsObject(c.Method.Payload.Type)
	}
	if payload == nil {
		verr.Add(c, "completion payload must contain required value and optional arguments")
	} else {
		value := payload.Attribute("value")
		if value == nil || !isPrimitive(value.Type, expr.String) || !c.Method.Payload.IsRequired("value") {
			verr.Add(c, "completion payload value must be a required string")
		}
		if payload.Attribute("arguments") == nil {
			verr.Add(c, "completion payload must declare optional arguments")
		}
		for _, field := range *payload {
			if field.Name == "value" {
				continue
			}
			if field.Name != "arguments" {
				verr.Add(c, "unsupported completion payload field %q", field.Name)
				continue
			}
			arguments := expr.AsMap(field.Attribute.Type)
			if arguments == nil || !isPrimitive(arguments.KeyType.Type, expr.String) || !isPrimitive(arguments.ElemType.Type, expr.String) || c.Method.Payload.IsRequired(field.Name) {
				verr.Add(c, "completion arguments must be an optional map of strings")
			}
		}
	}
	var result *expr.Object
	if hasValue(c.Method.Result) {
		result = expr.AsObject(c.Method.Result.Type)
	}
	if result == nil {
		verr.Add(c, "completion result must contain a values array")
	} else {
		values := result.Attribute("values")
		if values == nil || expr.AsArray(values.Type) == nil || !isPrimitive(expr.AsArray(values.Type).ElemType.Type, expr.String) {
			verr.Add(c, "completion values must be an array of strings")
		} else {
			bounds := expr.EffectiveValidation(values)
			if bounds == nil || bounds.MaxLength == nil || *bounds.MaxLength > 100 {
				verr.Add(c, "completion values must declare MaxLength(100) or a stricter bound")
			}
		}
		for _, field := range *result {
			switch field.Name {
			case "values":
			case "total":
				if !isPrimitive(field.Attribute.Type, expr.Int64) {
					verr.Add(c, "completion total must be Int64")
				}
			case "hasMore":
				if !isPrimitive(field.Attribute.Type, expr.Boolean) {
					verr.Add(c, "completion hasMore must be Boolean")
				}
			default:
				verr.Add(c, "unsupported completion result field %q", field.Name)
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
