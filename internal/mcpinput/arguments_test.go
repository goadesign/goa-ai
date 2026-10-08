package mcpinput

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/expr"
)

func TestArguments(t *testing.T) {
	for _, tag := range []string{"security:username", "security:password", "security:bearer", "security:token", "security:accesstoken", "security:apikey:private"} {
		t.Run(tag, func(t *testing.T) {
			secret := &expr.AttributeExpr{Type: expr.String, Meta: expr.MetaExpr{tag: nil}}
			domain := &expr.AttributeExpr{Type: expr.String, Description: "An ordinary domain token"}
			definition := &expr.AttributeExpr{
				Type:         &expr.Object{{Name: "credential", Attribute: secret}, {Name: "token", Attribute: domain}},
				Validation:   &expr.ValidationExpr{Required: []string{"credential", "token"}},
				UserExamples: []*expr.ExampleExpr{{Summary: "one", Value: expr.Val{"credential": "secret", "token": "domain"}}},
				Meta:         expr.MetaExpr{"struct:pkg:path": []string{"example.local/types"}},
			}
			named := &expr.UserTypeExpr{TypeName: "Input", AttributeExpr: definition}
			payload := &expr.AttributeExpr{
				Type:         named,
				Validation:   &expr.ValidationExpr{Required: []string{"credential", "token"}},
				UserExamples: []*expr.ExampleExpr{{Value: map[string]any{"credential": "secret", "token": "domain"}}},
			}
			selected, err := Arguments(&expr.MethodExpr{Payload: payload})
			require.NoError(t, err)
			selectedType := selected.Type.(expr.UserType)
			assert.Same(t, named.Origin(), selectedType.Origin())
			assert.Equal(t, definition.Meta, selectedType.Attribute().Meta)
			assert.Equal(t, []string{"token"}, selected.Validation.Required)
			assert.Equal(t, []string{"token"}, selectedType.Attribute().Validation.Required)
			assert.Same(t, domain, expr.AsObject(selected.Type).Attribute("token"))
			assert.Nil(t, expr.AsObject(selected.Type).Attribute("credential"))
			assert.Equal(t, map[string]json.RawMessage{"token": json.RawMessage(`"domain"`)}, selectedType.Attribute().UserExamples[0].Value)
			assert.Equal(t, map[string]json.RawMessage{"token": json.RawMessage(`"domain"`)}, selected.UserExamples[0].Value)
			assert.Equal(t, []string{"credential", "token"}, payload.Validation.Required)
			assert.Equal(t, expr.Val{"credential": "secret", "token": "domain"}, definition.UserExamples[0].Value)
			assert.Same(t, secret, expr.AsObject(payload.Type).Attribute("credential"))
		})
	}
}

func TestArgumentsWithoutCredentials(t *testing.T) {
	for _, payload := range []*expr.AttributeExpr{nil, {Type: expr.Empty}, {Type: expr.String}, {Type: &expr.Object{{Name: "token", Attribute: &expr.AttributeExpr{Type: expr.String}}}}} {
		selected, err := Arguments(&expr.MethodExpr{Payload: payload})
		require.NoError(t, err)
		assert.Same(t, payload, selected)
	}
}

func TestArgumentsCredentialOnlyAndNestedDomain(t *testing.T) {
	nested := &expr.AttributeExpr{Type: &expr.Object{{Name: "token", Attribute: &expr.AttributeExpr{Type: expr.String, Meta: expr.MetaExpr{"security:token": nil}}}}}
	for _, withDomain := range []bool{false, true} {
		fields := expr.Object{{Name: "credential", Attribute: &expr.AttributeExpr{Type: expr.String, Meta: expr.MetaExpr{"security:token": nil}}}}
		if withDomain {
			fields = append(fields, &expr.NamedAttributeExpr{Name: "domain", Attribute: nested})
		}
		selected, err := Arguments(&expr.MethodExpr{Payload: &expr.AttributeExpr{Type: &fields}})
		require.NoError(t, err)
		if withDomain {
			assert.Same(t, nested, expr.AsObject(selected.Type).Attribute("domain"))
		} else {
			assert.Empty(t, *expr.AsObject(selected.Type))
		}
	}
}

func TestArgumentsRejectMalformedExample(t *testing.T) {
	payload := &expr.AttributeExpr{
		Type:         &expr.Object{{Name: "credential", Attribute: &expr.AttributeExpr{Type: expr.String, Meta: expr.MetaExpr{"security:token": nil}}}},
		UserExamples: []*expr.ExampleExpr{{Value: "not an object"}},
	}
	_, err := Arguments(&expr.MethodExpr{Payload: payload})
	assert.ErrorContains(t, err, "MCP payload example must be an object")
}

func TestArgumentExamplesPreserveJSON(t *testing.T) {
	type stringMap map[string]string
	type objectExample struct {
		Credential string `json:"credential"`
		Token      string `json:"token"`
	}
	for _, example := range []any{
		stringMap{"credential": "secret", "token": "domain"},
		objectExample{Credential: "secret", Token: "domain"},
		map[string]any{"credential": "secret", "token": "domain", "number": int64(9007199254740993)},
	} {
		payload := &expr.AttributeExpr{
			Type:         &expr.Object{{Name: "credential", Attribute: &expr.AttributeExpr{Type: expr.String, Meta: expr.MetaExpr{"security:token": nil}}}, {Name: "token", Attribute: &expr.AttributeExpr{Type: expr.String}}, {Name: "number", Attribute: &expr.AttributeExpr{Type: expr.Int64}}},
			UserExamples: []*expr.ExampleExpr{{Value: example}},
		}
		selected, err := Arguments(&expr.MethodExpr{Payload: payload})
		require.NoError(t, err)
		fields := selected.UserExamples[0].Value.(map[string]json.RawMessage)
		assert.NotContains(t, fields, "credential")
		assert.JSONEq(t, `"domain"`, string(fields["token"]))
		if number, present := fields["number"]; present {
			assert.Equal(t, "9007199254740993", string(number))
		}
	}
}

// TestDomainArgumentsKeepsNativeBindings checks that only host answers disappear.
// Credential and URL fields retain their declarations, constraints and examples.
func TestDomainArgumentsKeepsNativeBindings(t *testing.T) {
	method := exchangeMethod()
	credential := &expr.AttributeExpr{Type: expr.String, Meta: expr.MetaExpr{"security:token": nil}}
	route := &expr.AttributeExpr{Type: expr.String, Meta: expr.MetaExpr{"struct:field:name": []string{"ResourceKey"}}}
	*expr.AsObject(method.Payload.Type) = append(*expr.AsObject(method.Payload.Type),
		&expr.NamedAttributeExpr{Name: "credential", Attribute: credential},
		&expr.NamedAttributeExpr{Name: "resource_key", Attribute: route})
	method.Payload.Validation.Required = append(method.Payload.Validation.Required, "credential", "resource_key")
	method.Meta[pathFieldsKey] = []string{"resource_key"}
	method.Payload.UserExamples = []*expr.ExampleExpr{{Value: expr.Val{"destination": "here", "credential": "secret", "resource_key": "key", "continuation": expr.Val{"state": "opaque"}}}}
	native, err := DomainArguments(method)
	require.NoError(t, err)
	assert.Nil(t, native.Find("continuation"))
	assert.Same(t, credential, native.Find("credential"))
	assert.Same(t, route, native.Find("resource_key"))
	assert.Equal(t, []string{"destination", "credential", "resource_key"}, native.Validation.Required)
	assert.Equal(t, map[string]json.RawMessage{"destination": json.RawMessage(`"here"`), "credential": json.RawMessage(`"secret"`), "resource_key": json.RawMessage(`"key"`)}, native.UserExamples[0].Value)
	assert.NotNil(t, method.Payload.Find("continuation"))
	transport, err := Arguments(method)
	require.NoError(t, err)
	assert.Nil(t, transport.Find("credential"))
	assert.Nil(t, transport.Find("resource_key"))
	assert.NotNil(t, transport.Find("destination"))
}

// TestArgumentsInheritedFields checks early DSL validation and later generation
// use the same payload while credentials stay out of the model's arguments.
func TestArgumentsInheritedFields(t *testing.T) {
	for _, named := range []bool{false, true} {
		t.Run(fmt.Sprint(named), func(t *testing.T) {
			credential := &expr.AttributeExpr{Type: expr.String, Meta: expr.MetaExpr{"security:token": nil}}
			cursor := &expr.AttributeExpr{Type: expr.String, Meta: expr.MetaExpr{"struct:field:name": []string{"Page"}}}
			base := &expr.UserTypeExpr{TypeName: "PageInput", AttributeExpr: &expr.AttributeExpr{
				Type:       &expr.Object{{Name: "credential", Attribute: credential}, {Name: "cursor", Attribute: cursor}},
				Validation: &expr.ValidationExpr{Required: []string{"credential"}},
			}}
			payload := &expr.AttributeExpr{Type: &expr.Object{}, Bases: []expr.DataType{base}}
			if named {
				payload = &expr.AttributeExpr{Type: &expr.UserTypeExpr{TypeName: "ExtendedPage", AttributeExpr: payload}}
			}
			method := &expr.MethodExpr{Payload: payload}
			selected, err := Arguments(method)
			require.NoError(t, err)
			assert.NotNil(t, selected.Find("cursor"))
			assert.Nil(t, selected.Find("credential"))
			assert.False(t, selected.IsRequired("credential"))
			assert.Equal(t, cursor.Meta, selected.Find("cursor").Meta)
			original := payload
			if user, ok := payload.Type.(expr.UserType); ok {
				original = user.Attribute()
			}
			assert.Len(t, original.Bases, 1)
			assert.Empty(t, *expr.AsObject(original.Type))
			payload.Finalize()
			generated, err := Arguments(method)
			require.NoError(t, err)
			assert.Equal(t, selected.Find("cursor").Meta, generated.Find("cursor").Meta)
			assert.Nil(t, generated.Find("credential"))
			assert.Same(t, credential, payload.Find("credential"))
		})
	}
}
