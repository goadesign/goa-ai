// Package codegen builds a representative prompt service with all content kinds.
// The compiled HTTP fixture exercises actual Goa types, unions, codecs, clients,
// and server dispatch rather than asserting only generated source fragments.
package codegen

import (
	"slices"

	"goa.design/goa/v3/codegen"

	"goa.design/goa/v3/expr"
)

// methodPromptFixture gives two service methods the same authored result and
// returns every named type that the real Goa generation root must register.
func methodPromptFixture(methods map[string]*expr.MethodExpr) []expr.UserType {
	builder := newMCPExprBuilder(methods["review"].Service, nil)
	content := expr.NewAttributeGraphCopier().Copy(builder.buildContentItemType())
	prefixPromptFixtureTypes(content, make(map[expr.UserType]bool))
	flat := expr.AsObject(content.Type)
	values := make([]*expr.NamedAttributeExpr, 0, 5)
	variants := []struct {
		name             string
		fields, required []string
		binary           string
	}{
		{"text", []string{"text", "annotations", "_meta"}, []string{"text"}, ""},
		{"image", []string{"data", "mimeType", "annotations", "_meta"}, []string{"mimeType"}, "data"},
		{"audio", []string{"data", "mimeType", "annotations", "_meta"}, []string{"mimeType"}, "data"},
		{"resource_link", []string{"name", "uri", "title", "description", "mimeType", "size", "icons", "annotations", "_meta"}, []string{"name", "uri"}, ""},
		{"resource", []string{"resource", "annotations", "_meta"}, []string{"resource"}, ""},
	}
	for _, variant := range variants {
		object := make(expr.Object, 0, len(variant.fields))
		for _, name := range variant.fields {
			attribute := expr.NewAttributeGraphCopier().Copy(flat.Attribute(name))
			if name == variant.binary {
				attribute.Type = expr.Bytes
			}
			if name == "resource" {
				source := expr.AsObject(attribute.Type)
				text := promptFixtureType("AuthoredTextResource", &expr.Object{
					{Name: "uri", Attribute: source.Attribute("uri")},
					{Name: "mimeType", Attribute: source.Attribute("mimeType")},
					{Name: "text", Attribute: source.Attribute("text")},
					{Name: "_meta", Attribute: source.Attribute("_meta")},
				}, "uri", "text")
				blob := promptFixtureType("AuthoredBlobResource", &expr.Object{
					{Name: "uri", Attribute: expr.NewAttributeGraphCopier().Copy(source.Attribute("uri"))},
					{Name: "mimeType", Attribute: expr.NewAttributeGraphCopier().Copy(source.Attribute("mimeType"))},
					{Name: "blob", Attribute: &expr.AttributeExpr{Type: expr.Bytes, Description: "Binary contents"}},
					{Name: "_meta", Attribute: expr.NewAttributeGraphCopier().Copy(source.Attribute("_meta"))},
				}, "uri")
				attribute = &expr.AttributeExpr{Type: &expr.Union{TypeName: "AuthoredResource", Values: []*expr.NamedAttributeExpr{
					{Name: "text", Attribute: &expr.AttributeExpr{Type: text}},
					{Name: "blob", Attribute: &expr.AttributeExpr{Type: blob}},
				}}, Description: "Selected embedded contents"}
			}
			object = append(object, &expr.NamedAttributeExpr{Name: name, Attribute: attribute})
		}
		data := promptFixtureType("Authored"+codegen.Goify(variant.name, true)+"Data", &object, variant.required...)
		values = append(values, &expr.NamedAttributeExpr{Name: variant.name, Attribute: &expr.AttributeExpr{Type: data}})
	}
	role := promptFixtureType("AuthoredRole", expr.String)
	role.Meta = expr.MetaExpr{"struct:pkg:path": {"prompt/shared"}}
	role.Validation = &expr.ValidationExpr{Values: []any{"user", "assistant"}}
	message := promptFixtureType("AuthoredMessage", &expr.Object{
		{Name: "role", Attribute: &expr.AttributeExpr{Type: role, Description: "Message author"}},
		{Name: "content", Attribute: &expr.AttributeExpr{Type: &expr.Union{TypeName: "AuthoredContent", Values: values}, Description: "One selected content kind"}},
	}, "role", "content")
	expr.AsObject(message.Type).Attribute("role").Meta = expr.MetaExpr{"struct:field:name": {"Author"}}
	expr.AsObject(message.Type).Attribute("content").Meta = expr.MetaExpr{"struct:field:name": {"Selected"}}
	result := promptFixtureType("AuthoredPromptResult", &expr.Object{
		{Name: "description", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Prompt purpose"}},
		{Name: "messages", Attribute: &expr.AttributeExpr{Type: &expr.Array{ElemType: &expr.AttributeExpr{Type: message}, NonNullableElems: true}, Description: "Ordered prompt messages"}},
	})
	code := promptFixtureType("AuthoredCode", expr.String)
	code.Validation = &expr.ValidationExpr{MinLength: new(1)}
	methods["review"].Payload = &expr.AttributeExpr{Type: &expr.Object{
		{Name: "code", Attribute: &expr.AttributeExpr{Type: code, Description: "Source code to review"}},
		{Name: "style", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Review detail", Validation: &expr.ValidationExpr{Values: []any{"brief", "detailed"}}, DefaultValue: "brief"}},
	}, Validation: &expr.ValidationExpr{Required: []string{"code"}}}
	methods["review"].Result = &expr.AttributeExpr{Type: result}
	methods["empty"].Payload = &expr.AttributeExpr{Type: expr.Empty}
	methods["empty"].Result = &expr.AttributeExpr{Type: result}
	var types []expr.UserType
	collectPromptFixtureTypes(methods["review"].Payload, make(map[expr.UserType]bool), &types)
	collectPromptFixtureTypes(methods["review"].Result, make(map[expr.UserType]bool), &types)
	return types
}

// promptFixtureType creates one authored type with its normal Goa identity.
func promptFixtureType(name string, dataType expr.DataType, required ...string) *expr.UserTypeExpr {
	return &expr.UserTypeExpr{TypeName: name, UID: "method-prompt-fixture-" + name, AttributeExpr: &expr.AttributeExpr{Type: dataType, Validation: &expr.ValidationExpr{Required: required}}}
}

// prefixPromptFixtureTypes keeps the authored fixture types distinct from the
// protocol types that the MCP plugin adds to the same generation root.
func prefixPromptFixtureTypes(attribute *expr.AttributeExpr, seen map[expr.UserType]bool) {
	switch actual := attribute.Type.(type) {
	case expr.UserType:
		if seen[actual] {
			return
		}
		seen[actual] = true
		actual.(*expr.UserTypeExpr).TypeName = "Authored" + actual.Name()
		prefixPromptFixtureTypes(actual.Attribute(), seen)
	case *expr.Object:
		for _, field := range *actual {
			prefixPromptFixtureTypes(field.Attribute, seen)
		}
	case *expr.Array:
		prefixPromptFixtureTypes(actual.ElemType, seen)
	}
}

// collectPromptFixtureTypes registers each shared named type once, preserving
// the exact type instances referenced by the service attributes.
func collectPromptFixtureTypes(attribute *expr.AttributeExpr, seen map[expr.UserType]bool, types *[]expr.UserType) {
	switch actual := attribute.Type.(type) {
	case expr.UserType:
		if seen[actual] {
			return
		}
		seen[actual] = true
		duplicate := slices.ContainsFunc(*types, func(existing expr.UserType) bool { return existing.Name() == actual.Name() })
		if !duplicate {
			*types = append(*types, actual)
		}
		collectPromptFixtureTypes(actual.Attribute(), seen, types)
	case *expr.Object:
		for _, field := range *actual {
			collectPromptFixtureTypes(field.Attribute, seen, types)
		}
	case *expr.Array:
		collectPromptFixtureTypes(actual.ElemType, seen, types)
	case *expr.Union:
		for _, branch := range actual.Values {
			collectPromptFixtureTypes(branch.Attribute, seen, types)
		}
	}
}

// promptCompletionFixture uses named strings and renamed fields to verify that
// completion construction follows Goa's layouts rather than wire field names.
func promptCompletionFixture(method *expr.MethodExpr) []expr.UserType {
	key := promptFixtureType("CompletionKey", expr.String)
	key.Meta = expr.MetaExpr{"struct:pkg:path": {"prompt/shared"}}
	value := promptFixtureType("CompletionText", expr.String)
	value.Meta = expr.MetaExpr{"struct:pkg:path": {"prompt/shared"}}
	method.Payload = &expr.AttributeExpr{Type: &expr.Object{
		{Name: "value", Attribute: &expr.AttributeExpr{Type: value, Description: "Partial argument text", Meta: expr.MetaExpr{"struct:field:name": {"Partial"}}, Validation: &expr.ValidationExpr{MaxLength: new(20)}}},
		{Name: "arguments", Attribute: &expr.AttributeExpr{Type: &expr.Map{KeyType: &expr.AttributeExpr{Type: key}, ElemType: &expr.AttributeExpr{Type: value}}, Description: "Prior values", Meta: expr.MetaExpr{"struct:field:name": {"Prior"}}}},
	}, Validation: &expr.ValidationExpr{Required: []string{"value"}}}
	result := promptFixtureType("AuthoredSuggestions", &expr.Object{
		{Name: "values", Attribute: &expr.AttributeExpr{Type: &expr.Array{ElemType: &expr.AttributeExpr{Type: expr.String}}, Description: "Ranked suggestions", Validation: &expr.ValidationExpr{MaxLength: new(100)}}},
		{Name: "total", Attribute: &expr.AttributeExpr{Type: expr.Int64, Description: "All matches", Meta: expr.MetaExpr{"struct:field:name": {"Matches"}}}},
		{Name: "hasMore", Attribute: &expr.AttributeExpr{Type: expr.Boolean, Description: "More matches exist"}},
	})
	method.Result = &expr.AttributeExpr{Type: result}
	return []expr.UserType{key, value, result}
}
