// Package mcpinput separates Goa authentication and URL fields from MCP arguments.
// Design validation and code generation use the same remaining fields, required
// constraints and examples. The original service payload keeps its credentials
// and type identity so configured endpoints still receive their authored input.
package mcpinput

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"goa.design/goa/v3/expr"
)

// Arguments returns the payload fields that clients may supply as MCP arguments.
// Goa authentication annotations, URL bindings and typed continuation fields
// select excluded fields. Unbound domain fields keep their names, types, and examples.
// The original payload is never changed.
func Arguments(method *expr.MethodExpr) (*expr.AttributeExpr, error) {
	payload := resolvedArgumentPayload(method.Payload)
	names := append(Credentials(payload), method.Meta[pathFieldsKey]...)
	mapping, err := InputExchange(method)
	if err != nil {
		return nil, err
	}
	if mapping != nil {
		names = append(names, mapping.ContinuationName)
	}
	if len(names) == 0 {
		return payload, nil
	}
	return selectArguments(payload, names)
}

// DomainArguments removes only the host continuation from a native method input.
// Credential, URL and injected fields retain their ordinary BindTo behavior.
func DomainArguments(method *expr.MethodExpr) (*expr.AttributeExpr, error) {
	mapping, err := InputExchange(method)
	if err != nil {
		return nil, err
	}
	if mapping == nil {
		return method.Payload, nil
	}
	return selectArguments(method.Payload, []string{mapping.ContinuationName})
}

// Credentials returns the annotated top-level credential names in design order.
// Nested fields remain domain input, just as they do in Goa authentication.
func Credentials(payload *expr.AttributeExpr) []string {
	if payload == nil || payload.Type == nil {
		return nil
	}
	object := expr.AsObject(payload.Type)
	if object == nil {
		return nil
	}
	var names []string
	for _, field := range *object {
		if isCredential(field.Attribute.Meta) {
			names = append(names, field.Name)
		}
	}
	return names
}

// selectArguments copies each named wrapper and its top-level object while
// retaining the original field declarations and locations for generated types.
// Examples and required lists lose precisely the excluded transport fields.
func selectArguments(payload *expr.AttributeExpr, names []string) (*expr.AttributeExpr, error) {
	selected := *payload
	if named, ok := payload.Type.(expr.UserType); ok {
		definition, err := selectArguments(named.Attribute(), names)
		if err != nil {
			return nil, err
		}
		selected.Type = named.Dup(definition)
	} else {
		object := expr.AsObject(payload.Type)
		fields := make(expr.Object, 0, len(*object))
		for _, field := range *object {
			if !slices.Contains(names, field.Name) {
				fields = append(fields, field)
			}
		}
		selected.Type = &fields
	}
	if selected.Validation != nil {
		selected.Validation = selected.Validation.Dup()
		selected.Validation.Required = slices.DeleteFunc(selected.Validation.Required, func(name string) bool {
			return slices.Contains(names, name)
		})
	}
	selected.UserExamples = make([]*expr.ExampleExpr, len(payload.UserExamples))
	for index, example := range payload.UserExamples {
		copied := *example
		encoded, err := json.Marshal(example.Value)
		if err != nil {
			return nil, fmt.Errorf("encode MCP payload example: %w", err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &fields); err != nil {
			return nil, fmt.Errorf("MCP payload example must be an object: %w", err)
		}
		if fields == nil {
			return nil, fmt.Errorf("MCP payload example must be an object")
		}
		for _, name := range names {
			delete(fields, name)
		}
		copied.Value = fields
		selected.UserExamples[index] = &copied
	}
	return &selected, nil
}

// isCredential recognizes the exact tags consumed by Goa's authentication
// generator. Other metadata and credential-like field names have no effect.
func isCredential(meta expr.MetaExpr) bool {
	for name := range meta {
		switch name {
		case "security:username", "security:password", "security:bearer", "security:token", "security:accesstoken":
			return true
		}
		if strings.HasPrefix(name, "security:apikey:") {
			return true
		}
	}
	return false
}

// resolvedArgumentPayload asks Goa to merge inherited fields on a detached
// copy before MCP validation. Already resolved payloads keep their original
// fields; the authored method is never finalized or changed by this reader.
func resolvedArgumentPayload(payload *expr.AttributeExpr) *expr.AttributeExpr {
	for attribute := payload; attribute != nil; {
		if len(attribute.Bases) > 0 || len(attribute.References) > 0 {
			resolved := expr.DupAtt(payload)
			resolved.Finalize()
			return resolved
		}
		named, ok := attribute.Type.(expr.UserType)
		if !ok {
			break
		}
		attribute = named.Attribute()
	}
	return payload
}
