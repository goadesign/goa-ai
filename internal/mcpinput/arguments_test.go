package mcpinput

import (
	"encoding/json"
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
			selected, err := Arguments(payload)
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
		selected, err := Arguments(payload)
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
		selected, err := Arguments(&expr.AttributeExpr{Type: &fields})
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
	_, err := Arguments(payload)
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
		selected, err := Arguments(payload)
		require.NoError(t, err)
		fields := selected.UserExamples[0].Value.(map[string]json.RawMessage)
		assert.NotContains(t, fields, "credential")
		assert.JSONEq(t, `"domain"`, string(fields["token"]))
		if number, present := fields["number"]; present {
			assert.Equal(t, "9007199254740993", string(number))
		}
	}
}
