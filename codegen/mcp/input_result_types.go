// Package codegen represents MCP operation replies with Goa's native union.
// Each reply contains either completed operation data or requests for more
// input. Goa's response-body mapping writes the selected branch directly.
package codegen

import (
	"reflect"
	"strings"

	"goa.design/goa-ai/internal/mcpvalue"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

const (
	completeBranch      = "complete"
	inputRequiredBranch = "input_required"
)

// inputResultAttr builds the two legal results for a tool call, resource read,
// or prompt get. The union owns the resultType discriminator; branch objects
// contain only their own fields and protocol metadata.
func (b *mcpExprBuilder) inputResultAttr(name string, builder func() *expr.AttributeExpr) *expr.AttributeExpr {
	return &expr.AttributeExpr{Type: b.getOrCreateType(name, func() *expr.AttributeExpr {
		prefix := strings.TrimSuffix(name, "Result")
		complete := b.userTypeAttr(prefix+"CompleteResult", builder)
		pending := &expr.AttributeExpr{Type: b.getOrCreateType("InputRequiredResult", b.buildInputRequiredResultType)}
		choice := &expr.Union{
			TypeName: prefix + "Outcome",
			TypeKey:  "resultType",
			Flatten:  true,
			Values: []*expr.NamedAttributeExpr{
				{Name: completeBranch, Attribute: complete},
				{Name: inputRequiredBranch, Attribute: pending},
			},
		}
		if name == "ToolsCallResult" {
			choice.Values = append(choice.Values, &expr.NamedAttributeExpr{Name: "task", Attribute: &expr.AttributeExpr{Type: b.getOrCreateType("TaskCreated", b.buildTaskCreatedType)}})
		}
		return &expr.AttributeExpr{
			Type: &expr.Object{{Name: "outcome", Attribute: &expr.AttributeExpr{
				Type:        choice,
				Description: "Finished tool data, an input round, or a durable task observation",
			}}},
			Validation: &expr.ValidationExpr{Required: []string{"outcome"}},
		}
	})}
}

// buildInputRequiredResultType retains one operation's questions and opaque
// state. The adapter checks their presence and client support before returning
// this branch; clients echo the state without interpreting it.
func (b *mcpExprBuilder) buildInputRequiredResultType() *expr.AttributeExpr {
	form := &expr.AttributeExpr{Type: b.getOrCreateType("ElicitationFormParams", func() *expr.AttributeExpr {
		attribute := elicitationParamsWithExtensions(mcpvalue.FormParams())
		attribute.UserExamples = []*expr.ExampleExpr{{Value: map[string]any{
			"message": "Choose a label", "requestedSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		}}}
		return attribute
	})}
	url := &expr.AttributeExpr{Type: b.getOrCreateType("ElicitationURLParams", func() *expr.AttributeExpr {
		attribute := elicitationParamsWithExtensions(mcpvalue.URLParams())
		attribute.UserExamples = []*expr.ExampleExpr{{Value: map[string]any{
			"message": "Approve the request", "url": "https://example.com/consent",
		}}}
		return attribute
	})}
	request := b.getOrCreateType("InputRequest", func() *expr.AttributeExpr {
		return &expr.AttributeExpr{
			Type: &expr.Object{
				{Name: "method", Attribute: &expr.AttributeExpr{
					Type:        expr.String,
					Description: "Client operation requested to complete this input round",
					Validation:  &expr.ValidationExpr{Values: []any{"elicitation/create"}},
				}},
				{Name: "params", Attribute: &expr.AttributeExpr{
					Type: &expr.Union{TypeName: "ElicitationParams", TypeKey: "mode", Flatten: true, Values: []*expr.NamedAttributeExpr{
						{Name: "form", Attribute: form},
						{Name: "url", Attribute: url},
					}},
					Description: "A typed form request or URL-consent request",
				}},
			},
			Validation: &expr.ValidationExpr{Required: []string{"method", "params"}},
		}
	})
	return &expr.AttributeExpr{Type: &expr.Object{
		{Name: "inputRequests", Attribute: &expr.AttributeExpr{
			Type:        &expr.Map{KeyType: &expr.AttributeExpr{Type: expr.String}, ElemType: &expr.AttributeExpr{Type: request}},
			Description: "Questions indexed by service-assigned identifiers within this input round",
			Meta:        expr.MetaExpr{"struct:tag:json": {"inputRequests,omitzero"}},
		}},
		{Name: "requestState", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Opaque service-owned operation state echoed exactly on continuation"}},
		{Name: "_meta", Attribute: protocolJSONAttribute("Namespaced protocol metadata and extension values")},
	}}
}

// protocolJSONAttribute keeps an open protocol object as raw JSON. Typed domain
// payloads still use their generated codecs; these values are checked at the
// MCP boundary according to the selected protocol operation.
func protocolJSONAttribute(description string) *expr.AttributeExpr {
	return &expr.AttributeExpr{
		Type: expr.Any, Description: description,
		Meta: expr.MetaExpr{"struct:field:type": {"json.RawMessage", "encoding/json"}},
	}
}

// protocolCompletedResult selects the completed protocol branch for content
// converters. Only the three MCP operations that allow input rounds call it.
func protocolCompletedResult(attribute *expr.AttributeExpr) *expr.AttributeExpr {
	choice := expr.AsUnion(attribute.Find("outcome").Type)
	for _, branch := range choice.Values {
		if branch.Name == completeBranch {
			return branch.Attribute
		}
	}
	panic("MCP operation result does not declare its completed branch")
}

// buildInputMethodErrors declares the required-capabilities data with a native
// Goa error type. The ordinary JSON-RPC error encoder supplies its code and
// description and serializes this typed data without another error path.
func (b *mcpExprBuilder) buildInputMethodErrors() []*expr.ErrorExpr {
	errors := buildMCPMethodErrors(mcpDispatchErrors[:]...)
	mode := b.getOrCreateType("ElicitationCapabilities", func() *expr.AttributeExpr {
		return &expr.AttributeExpr{Type: &expr.Object{
			{Name: "form", Attribute: &expr.AttributeExpr{Type: &expr.Object{}, Description: "Support for non-sensitive form input"}},
			{Name: "url", Attribute: &expr.AttributeExpr{Type: &expr.Object{}, Description: "Support for external URL consent"}},
		}}
	})
	capabilities := b.getOrCreateType("RequiredClientCapabilities", func() *expr.AttributeExpr {
		return &expr.AttributeExpr{Type: &expr.Object{
			{Name: "elicitation", Attribute: &expr.AttributeExpr{Type: mode, Description: "Required forms of user input"}},
			{Name: "extensions", Attribute: &expr.AttributeExpr{Type: &expr.Map{KeyType: &expr.AttributeExpr{Type: expr.String}, ElemType: &expr.AttributeExpr{Type: b.getOrCreateType("RequiredExtension", func() *expr.AttributeExpr { return &expr.AttributeExpr{Type: &expr.Object{}} })}}, Description: "Extensions required to fulfill the operation"}},
		}}
	})
	failure := b.getOrCreateType("MissingClientCapabilityError", func() *expr.AttributeExpr {
		return &expr.AttributeExpr{Type: &expr.Object{{Name: "requiredCapabilities", Attribute: &expr.AttributeExpr{
			Type: capabilities, Description: "Capabilities required to fulfill this operation's questions",
		}}}, Description: mcpMissingClientCapabilityError.description, Validation: &expr.ValidationExpr{Required: []string{"requiredCapabilities"}}}
	})
	return append(errors, &expr.ErrorExpr{
		Name:          mcpMissingClientCapabilityError.name,
		AttributeExpr: &expr.AttributeExpr{Type: failure, Description: mcpMissingClientCapabilityError.description},
	})
}

// elicitationParamsWithExtensions adds the protocol's open members to its typed
// form or URL fields. Internal input requests keep their separate saved map.
func elicitationParamsWithExtensions(attribute *expr.AttributeExpr) *expr.AttributeExpr {
	object := expr.AsObject(attribute.Type)
	// Exact field matching keeps an extra member such as MESSAGE separate from
	// message when the ordinary JSON decoder reads these protocol parameters.
	for _, field := range *object {
		tags := strings.Trim(codegen.AttributeTagsWithName(nil, "", field.Attribute), " `")
		tag := reflect.StructTag(tags).Get("json")
		if tag == "" {
			tag = field.Name
		}
		if field.Attribute.Meta == nil {
			field.Attribute.Meta = make(expr.MetaExpr)
		}
		field.Attribute.Meta["struct:tag:json"] = []string{tag + ",case:strict"}
	}
	*object = append(*object, &expr.NamedAttributeExpr{Name: "extensions", Attribute: &expr.AttributeExpr{
		Type:        &expr.Map{KeyType: &expr.AttributeExpr{Type: expr.String}, ElemType: &expr.AttributeExpr{Type: expr.Bytes, Meta: expr.MetaExpr{"struct:field:type": {"json.RawMessage", "encoding/json"}}}},
		Meta:        expr.MetaExpr{"struct:tag:json": {",embed"}, "openapi:generate": {"false"}},
		Description: "Additional protocol members preserved as JSON values",
	}})
	return attribute
}
