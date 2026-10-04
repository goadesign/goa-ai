// Package codegen maps typed prompt messages and resource contents to MCP.
// Authors declare content with Goa OneOf; the generated adapter selects its
// declared branch and returns the flat protocol object without JSON round trips.
package codegen

import (
	"fmt"
	"slices"
	"sort"

	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

type (
	// MethodPromptAdapter contains the static contract for a service-owned prompt.
	MethodPromptAdapter struct {
		// Name identifies the prompt in MCP requests.
		Name string
		// Description explains when the client should select the prompt.
		Description string
		// Endpoint calls the configured Goa endpoint for this method.
		Endpoint *endpointMethodAdapter
		// HasPayload reports whether the service method accepts arguments.
		HasPayload bool
		// Arguments lists the declared string arguments in design order.
		Arguments []*PromptArgumentAdapter
		// Codec names the generated typed argument constructor and result validator.
		Codec *MethodCodecData
		// PayloadTransportRef is the private argument type filled by the adapter.
		PayloadTransportRef string
		// PayloadConstructor validates the arguments and returns the service payload.
		PayloadConstructor string
		// MessagesField names the service result's message array.
		MessagesField string
		// DescriptionField names the optional service description; empty means absent.
		DescriptionField string
		// DescriptionPointer records Goa's optional field representation.
		DescriptionPointer bool
		// RoleField names the message role.
		RoleField string
		// RolePointer records the generated view's presence pointer for the role.
		RolePointer bool
		// ContentField names the message's OneOf value.
		ContentField string
		// ContentConversion names the generated OneOf-to-MCP function.
		ContentConversion string

		prompt          *mcpexpr.MethodPromptExpr
		conversion      *contentConversion
		resultAttribute *expr.AttributeExpr
		resultLayout    *codegen.GoTypePlan
	}
	// PromptArgumentAdapter describes one statically known prompt argument.
	PromptArgumentAdapter struct {
		// Name is the argument's design and wire name.
		Name string
		// Description explains the argument to clients.
		Description string
		// Required records the method's validation requirement.
		Required bool
		// Selector is the private argument record's Go field name.
		Selector string
		// TypeRef names the private string alias used by this field.
		TypeRef string
	}
	// contentConversion plans one selected content or resource union.
	contentConversion struct {
		attribute      *expr.AttributeExpr
		target         *expr.AttributeExpr
		declaration    *codegen.NameDeclaration
		sourceLayout   *codegen.GoTypePlan
		unionLayout    *codegen.GoTypePlan
		sourcePackage  *codegen.GeneratedPackage
		unionAttribute *expr.AttributeExpr
		branches       []*contentBranch
		data           *contentConversionData
	}
	// contentBranch retains one declared variant and its conversion plan.
	contentBranch struct {
		name       string
		attribute  *expr.AttributeExpr
		transform  *codegen.TransformPlan
		nested     *contentConversion
		bytesField string
		metaField  bool
	}
	// contentConversionData contains only final names and emitted Go code.
	contentConversionData struct {
		Name      string
		SourceRef string
		UnionRef  string
		Value     string
		TargetRef string
		HasType   bool
		Branches  []*contentBranchData
		Helpers   []*codegen.TransformFunctionData
	}
	// contentBranchData writes one statically selected branch.
	contentBranchData struct {
		Name             string
		Kind             string
		Getter           string
		Transform        string
		BytesField       string
		BytesPointer     bool
		TargetBytesField string
		ResourceField    string
		NestedConversion string
		MetaField        string
		CheckSize        bool
		CheckPriority    bool
	}
)

const (
	contentResourceField = "resource"
	contentResourceLink  = "resource_link"
)

// buildMethodPromptAdapters checks the authored shapes before generation adds
// any functions. Every accepted field has one declared MCP representation.
func (g *adapterGenerator) buildMethodPromptAdapters() ([]*MethodPromptAdapter, error) {
	builder := newMCPExprBuilder(g.originalService, g.mcp)
	content := builder.getOrCreateType("ContentItem", builder.buildContentItemType)
	adapters := make([]*MethodPromptAdapter, 0, len(g.mcp.MethodPrompts))
	for _, prompt := range g.mcp.MethodPrompts {
		if err := prompt.Validate(); err != nil {
			return nil, err
		}
		if err := checkContentGoType(prompt.Method.Result); err != nil {
			return nil, fmt.Errorf("prompt %q result: %w", prompt.Name, err)
		}
		if hasMCPValue(prompt.Method.Payload) {
			if err := checkContentGoType(prompt.Method.Payload); err != nil {
				return nil, fmt.Errorf("prompt %q arguments: %w", prompt.Name, err)
			}
		}
		result := expr.AsObject(prompt.Method.Result.Type)
		if err := checkContentFields(prompt.Method.Result, []string{"description", "messages"}); err != nil {
			return nil, fmt.Errorf("prompt %q result: %w", prompt.Name, err)
		}
		if description := result.Attribute("description"); description != nil && primitiveType(description.Type) != expr.String {
			return nil, fmt.Errorf("prompt %q result description must be a string", prompt.Name)
		}
		messages := expr.AsArray(result.Attribute("messages").Type)
		if !messages.NonNullableElems {
			return nil, fmt.Errorf("prompt %q messages must use ArrayOfRequired so null messages are rejected", prompt.Name)
		}
		message := expr.AsObject(messages.ElemType.Type)
		if err := checkContentFields(messages.ElemType, []string{"role", "content"}); err != nil {
			return nil, fmt.Errorf("prompt %q message: %w", prompt.Name, err)
		}
		for _, name := range []string{"role", "content"} {
			if !messages.ElemType.IsRequired(name) {
				return nil, fmt.Errorf("prompt %q message %q must be required", prompt.Name, name)
			}
		}
		role := expr.EffectiveValidation(message.Attribute("role"))
		if role == nil || len(role.Values) == 0 {
			return nil, fmt.Errorf("prompt %q message role must declare Enum(\"user\", \"assistant\")", prompt.Name)
		}
		for _, value := range role.Values {
			if value != "user" && value != "assistant" {
				return nil, fmt.Errorf("prompt %q message role permits unsupported value %v", prompt.Name, value)
			}
		}
		conversion, err := buildContentConversion(message.Attribute("content"), &expr.AttributeExpr{Type: content}, true)
		if err != nil {
			return nil, fmt.Errorf("prompt %q content: %w", prompt.Name, err)
		}
		adapter := &MethodPromptAdapter{Name: prompt.Name, Description: prompt.Description, HasPayload: hasMCPValue(prompt.Method.Payload), prompt: prompt, conversion: conversion}
		if adapter.HasPayload {
			for _, argument := range *expr.AsObject(prompt.Method.Payload.Type) {
				adapter.Arguments = append(adapter.Arguments, &PromptArgumentAdapter{Name: argument.Name, Description: argument.Attribute.Description, Required: prompt.Method.Payload.IsRequired(argument.Name)})
			}
		}
		adapters = append(adapters, adapter)
	}
	sort.Slice(adapters, func(i, j int) bool { return adapters[i].Name < adapters[j].Name })
	return adapters, nil
}

// buildContentConversion checks one union against its concrete content
// fields. Binary fields keep bytes in the service and become base64 on the wire.
func buildContentConversion(attribute, target *expr.AttributeExpr, hasType bool) (*contentConversion, error) {
	union := expr.AsUnion(attribute.Type)
	if union == nil || len(union.Values) == 0 {
		return nil, fmt.Errorf("embedded resource must use a text/blob OneOf")
	}
	conversion := &contentConversion{attribute: attribute, target: target}
	for _, branch := range union.Values {
		var fields, required []string
		var bytesField string
		if hasType {
			switch branch.Name {
			case "text":
				fields, required = []string{"text"}, []string{"text"}
			case "image", "audio":
				fields, required, bytesField = []string{"data", "mimeType"}, []string{"mimeType"}, "data"
			case contentResourceLink:
				fields, required = []string{"uri", "name", "title", "description", "mimeType", "size", "icons"}, []string{"uri", "name"}
			case contentResourceField:
				fields, required = []string{contentResourceField}, []string{contentResourceField}
			default:
				return nil, fmt.Errorf("unsupported content branch %q", branch.Name)
			}
			fields = append(fields, "annotations", "_meta")
		} else {
			switch branch.Name {
			case "text":
				fields, required = []string{"uri", "mimeType", "text", "_meta"}, []string{"uri", "text"}
			case "blob":
				fields, required, bytesField = []string{"uri", "mimeType", "blob", "_meta"}, []string{"uri"}, "blob"
			default:
				return nil, fmt.Errorf("unsupported embedded resource branch %q", branch.Name)
			}
		}
		if err := checkContentFields(branch.Attribute, fields); err != nil {
			return nil, fmt.Errorf("%s: %w", branch.Name, err)
		}
		object := expr.AsObject(branch.Attribute.Type)
		for _, name := range required {
			if object.Attribute(name) == nil || !branch.Attribute.IsRequired(name) {
				return nil, fmt.Errorf("%s.%s must be declared and required", branch.Name, name)
			}
		}
		planned := &contentBranch{name: branch.Name, attribute: branch.Attribute, bytesField: bytesField, metaField: object.Attribute("_meta") != nil}
		targetObject := expr.AsObject(target.Type)
		for _, field := range *object {
			if field.Name == bytesField {
				if primitiveType(field.Attribute.Type) != expr.Bytes {
					return nil, fmt.Errorf("%s.%s must use Bytes", branch.Name, field.Name)
				}
				continue
			}
			if hasType && branch.Name == contentResourceField && field.Name == contentResourceField {
				nested, err := buildContentConversion(field.Attribute, targetObject.Attribute(contentResourceField), false)
				if err != nil {
					return nil, err
				}
				planned.nested = nested
				continue
			}
			if err := checkContentFieldType(field.Attribute, targetObject.Attribute(field.Name)); err != nil {
				return nil, fmt.Errorf("%s.%s: %w", branch.Name, field.Name, err)
			}
		}
		if bytesField != "" && object.Attribute(bytesField) == nil {
			return nil, fmt.Errorf("%s.%s must be declared as Bytes", branch.Name, bytesField)
		}
		conversion.branches = append(conversion.branches, planned)
	}
	return conversion, nil
}

// checkContentFields rejects fields that would be silently lost during conversion.
func checkContentFields(attribute *expr.AttributeExpr, allowed []string) error {
	object := expr.AsObject(attribute.Type)
	if object == nil {
		return fmt.Errorf("must be an object")
	}
	for _, field := range *object {
		if !slices.Contains(allowed, field.Name) {
			return fmt.Errorf("field %q has no MCP representation", field.Name)
		}
	}
	return nil
}

// checkContentFieldType keeps author-defined names while requiring the declared
// fields and validations needed by the MCP content contract.
func checkContentFieldType(source, target *expr.AttributeExpr) error {
	sourceObject, targetObject := expr.AsObject(source.Type), expr.AsObject(target.Type)
	if targetObject != nil {
		if sourceObject == nil {
			return fmt.Errorf("must be an object")
		}
		allowed := make([]string, 0, len(*targetObject))
		for _, field := range *targetObject {
			allowed = append(allowed, field.Name)
		}
		if err := checkContentFields(source, allowed); err != nil {
			return err
		}
		for _, field := range *targetObject {
			if target.IsRequired(field.Name) && (sourceObject.Attribute(field.Name) == nil || !source.IsRequired(field.Name)) {
				return fmt.Errorf("%s must be declared and required", field.Name)
			}
		}
		for _, field := range *sourceObject {
			if err := checkContentFieldType(field.Attribute, targetObject.Attribute(field.Name)); err != nil {
				return fmt.Errorf("%s: %w", field.Name, err)
			}
		}
		return nil
	}
	if targetArray := expr.AsArray(target.Type); targetArray != nil {
		sourceArray := expr.AsArray(source.Type)
		if sourceArray == nil || targetArray.NonNullableElems && !sourceArray.NonNullableElems {
			return fmt.Errorf("must use a matching ArrayOfRequired")
		}
		return checkContentFieldType(sourceArray.ElemType, targetArray.ElemType)
	}
	if primitiveType(source.Type) != primitiveType(target.Type) {
		return fmt.Errorf("must use %s", target.Type.Name())
	}
	if primitiveType(target.Type) == expr.Any {
		if !slices.Equal(source.Meta["struct:field:type"], target.Meta["struct:field:type"]) {
			return fmt.Errorf("extension metadata must use json.RawMessage")
		}
		return nil
	}
	expected, actual := expr.EffectiveValidation(target), expr.EffectiveValidation(source)
	if expected == nil {
		return nil
	}
	if actual == nil {
		return fmt.Errorf("must declare the MCP field validation")
	}
	if expected.Format != "" && actual.Format != expected.Format {
		return fmt.Errorf("must declare Format(%q)", expected.Format)
	}
	if len(expected.Values) > 0 {
		if len(actual.Values) == 0 {
			return fmt.Errorf("must declare the MCP enum")
		}
		for _, value := range actual.Values {
			if !slices.Contains(expected.Values, value) {
				return fmt.Errorf("enum permits unsupported value %v", value)
			}
		}
	}
	if expected.Minimum != nil && (actual.Minimum == nil || *actual.Minimum < *expected.Minimum) && (actual.ExclusiveMinimum == nil || *actual.ExclusiveMinimum < *expected.Minimum) {
		return fmt.Errorf("must declare Minimum(%g)", *expected.Minimum)
	}
	if expected.Maximum != nil && (actual.Maximum == nil || *actual.Maximum > *expected.Maximum) && (actual.ExclusiveMaximum == nil || *actual.ExclusiveMaximum > *expected.Maximum) {
		return fmt.Errorf("must declare Maximum(%g)", *expected.Maximum)
	}
	return nil
}

// primitiveType follows named primitive aliases without losing the authored graph.
func primitiveType(dataType expr.DataType) expr.DataType {
	for {
		userType, ok := dataType.(expr.UserType)
		if !ok {
			return dataType
		}
		dataType = userType.Attribute().Type
	}
}

// checkContentGoType rejects Go type replacements that bypass the authored
// field contract. Ordinary Goa aliases and located types keep their schemas.
func checkContentGoType(attribute *expr.AttributeExpr) error {
	return codegen.Walk(attribute, func(current *expr.AttributeExpr) error {
		if len(current.Meta["struct:field:type"]) == 0 {
			return nil
		}
		if primitiveType(current.Type) == expr.Any && slices.Equal(current.Meta["struct:field:type"], []string{"json.RawMessage", "encoding/json"}) {
			return nil
		}
		return fmt.Errorf("struct:field:type is unsupported; use a Goa type with its declared fields")
	})
}
