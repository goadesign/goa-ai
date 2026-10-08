// Package mcp checks the service contract selected by a method-backed prompt.
// Prompt arguments are strings on the MCP wire; messages remain typed service
// values until generated code converts their selected content variants.
package mcp

import (
	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

// EvalName identifies the method-backed prompt in design errors.
func (p *MethodPromptExpr) EvalName() string {
	return "MCP prompt " + p.Name
}

// Validate rejects prompt declarations that cannot retain their typed arguments
// and messages. The generator additionally checks each content branch's fields.
func (p *MethodPromptExpr) Validate() error {
	verr := new(eval.ValidationErrors)
	if p.Name == "" {
		verr.Add(p, "prompt name is required")
	}
	if p.Description == "" {
		verr.Add(p, "prompt description is required")
	}
	if p.Method == nil {
		verr.Add(p, "prompt method is required")
		return verr
	}
	if _, declared := p.Method.Meta[mcpinput.TaskExchangeMetaKey]; declared {
		verr.Add(p, "TaskExchange is supported only by MCP tools/call")
	}
	if p.Method.IsStreaming() {
		verr.Add(p, "prompt method must be unary")
	}
	arguments, err := mcpinput.Arguments(p.Method)
	if err != nil {
		verr.Add(p, "%s", err.Error())
		return verr
	}
	if hasValue(arguments) {
		payload := expr.AsObject(arguments.Type)
		if payload == nil {
			verr.Add(p, "prompt payload must be an object of named strings")
		} else {
			for _, field := range *payload {
				if !isPrimitive(field.Attribute.Type, expr.String) {
					verr.Add(p, "prompt argument %q must be a string", field.Name)
				}
			}
		}
	}
	completed, err := mcpinput.CompleteResult(p.Method)
	if err != nil {
		verr.Add(p, "%s", err.Error())
		return verr
	}
	if !hasValue(completed) || expr.AsObject(completed.Type) == nil {
		verr.Add(p, "prompt result must be an object containing messages")
	} else {
		result := expr.AsObject(completed.Type)
		messages := result.Attribute("messages")
		if messages == nil || expr.AsArray(messages.Type) == nil {
			verr.Add(p, "prompt result must contain a messages array")
		} else {
			message := expr.AsObject(expr.AsArray(messages.Type).ElemType.Type)
			if message == nil || message.Attribute("role") == nil || !isPrimitive(message.Attribute("role").Type, expr.String) {
				verr.Add(p, "prompt message must contain a string role")
			}
			if message == nil || message.Attribute("content") == nil || expr.AsUnion(message.Attribute("content").Type) == nil {
				verr.Add(p, "prompt message must contain a content OneOf")
			}
		}
	}
	if len(verr.Errors) > 0 {
		return verr
	}
	return nil
}
