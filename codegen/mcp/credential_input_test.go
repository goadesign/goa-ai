// These tests check authentication declarations that cannot share one MCP HTTP
// request. Valid alternatives remain separate; incompatible bindings fail during
// generation before clients or servers can expose a misleading contract.
package codegen

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/expr"
)

func TestNativeCredentialBindings(t *testing.T) {
	for _, test := range []struct {
		name               string
		combined, required bool
		wantError          string
	}{
		{name: "optional alternatives"},
		{name: "combined formats", combined: true, wantError: "combines Basic and Bearer"},
		{name: "required alternatives", required: true, wantError: "require optional credential fields"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, methods := testService("protected", "read")
			method := methods["read"]
			method.Payload = &expr.AttributeExpr{Type: &expr.Object{
				{Name: "username", Attribute: &expr.AttributeExpr{Type: expr.String, Meta: expr.MetaExpr{"security:username": nil}}},
				{Name: "password", Attribute: &expr.AttributeExpr{Type: expr.String, Meta: expr.MetaExpr{"security:password": nil}}},
				{Name: "token", Attribute: &expr.AttributeExpr{Type: expr.String, Meta: expr.MetaExpr{"security:token": nil}}},
			}}
			if test.required {
				method.Payload.Validation = &expr.ValidationExpr{Required: []string{"token"}}
			}
			basic := &expr.SchemeExpr{Kind: expr.BasicAuthKind, SchemeName: "basic"}
			jwt := &expr.SchemeExpr{Kind: expr.JWTKind, SchemeName: "jwt"}
			method.Requirements = []*expr.SecurityExpr{{Schemes: []*expr.SchemeExpr{basic}}, {Schemes: []*expr.SchemeExpr{jwt}}}
			if test.combined {
				method.Requirements = []*expr.SecurityExpr{{Schemes: []*expr.SchemeExpr{basic, jwt}}}
			}
			inputs, err := nativeCredentialInputs(testRootExpr([]*expr.ServiceExpr{service}, nil), service, method)
			if test.wantError != "" {
				assert.ErrorContains(t, err, test.wantError)
				return
			}
			require.NoError(t, err)
			require.Len(t, inputs, 3)
			assert.Equal(t, "Bearer", inputs[0].AlternativeScheme)
			assert.Equal(t, "Bearer", inputs[1].AlternativeScheme)
			assert.Equal(t, "Basic", inputs[2].AlternativeScheme)
		})
	}
}

func TestNativeOAuthRequiresAuthorizationBearer(t *testing.T) {
	for _, binding := range []struct{ location, name string }{
		{"header", "Authorization"}, {"header", "X-Token"}, {"query", "access_token"}, {"cookie", "access_token"}, {"body", "token"},
	} {
		t.Run(binding.location+"/"+binding.name, func(t *testing.T) {
			service, methods := testService("protected", "read")
			method := methods["read"]
			method.Payload = &expr.AttributeExpr{Type: &expr.Object{{Name: "token", Attribute: &expr.AttributeExpr{Type: expr.String, Meta: expr.MetaExpr{"security:accesstoken": nil}}}}}
			method.Requirements = []*expr.SecurityExpr{{Schemes: []*expr.SchemeExpr{{Kind: expr.OAuth2Kind, SchemeName: "oauth", In: binding.location, Name: binding.name}}}}
			inputs, err := nativeCredentialInputs(testRootExpr([]*expr.ServiceExpr{service}, nil), service, method)
			if binding.location == "header" && binding.name == "Authorization" {
				require.NoError(t, err)
				assert.True(t, inputs[0].Bearer)
			} else {
				assert.ErrorContains(t, err, "must use Authorization: Bearer")
			}
		})
	}
}

func TestNativeCredentialRejectsConflictingFieldBindings(t *testing.T) {
	service, methods := testService("protected", "read")
	method := methods["read"]
	method.Payload = &expr.AttributeExpr{Type: &expr.Object{{Name: "token", Attribute: &expr.AttributeExpr{Type: expr.String, Meta: expr.MetaExpr{"security:token": nil}}}}}
	method.Requirements = []*expr.SecurityExpr{
		{Schemes: []*expr.SchemeExpr{{Kind: expr.JWTKind, SchemeName: "one", In: "header", Name: "X-One"}}},
		{Schemes: []*expr.SchemeExpr{{Kind: expr.JWTKind, SchemeName: "two", In: "header", Name: "X-Two"}}},
	}
	_, err := nativeCredentialInputs(testRootExpr([]*expr.ServiceExpr{service}, nil), service, method)
	assert.ErrorContains(t, err, "conflicting native authentication bindings")
}

func TestNativeCredentialRejectsProtocolHeaders(t *testing.T) {
	for _, name := range []string{"Content-Type", "accept", "MCP-Protocol-Version", "Mcp-Method", "Mcp-Name", "Mcp-Param-Region", "X-Native-Key", "Authorization"} {
		t.Run(name, func(t *testing.T) {
			service, methods := testService("protected", "read")
			method := methods["read"]
			method.Payload = &expr.AttributeExpr{Type: &expr.Object{{Name: "key", Attribute: &expr.AttributeExpr{Type: expr.String, Meta: expr.MetaExpr{"security:apikey:key": nil}}}}}
			method.Requirements = []*expr.SecurityExpr{{Schemes: []*expr.SchemeExpr{{Kind: expr.APIKeyKind, SchemeName: "key", In: "header", Name: name}}}}
			inputs, err := nativeCredentialInputs(testRootExpr([]*expr.ServiceExpr{service}, nil), service, method)
			if name == "X-Native-Key" || name == "Authorization" {
				require.NoError(t, err)
				assert.Equal(t, name, inputs[0].transportName)
			} else {
				assert.ErrorContains(t, err, "protocol-owned header")
			}
		})
	}
}
