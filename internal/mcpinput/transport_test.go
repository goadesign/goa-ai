// These tests keep URL-owned fields separate from equally named domain input.
// The recorded binding belongs to one method, never to a shared payload type.
package mcpinput

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/expr"
)

func TestArgumentsSelectMethodPathBindings(t *testing.T) {
	organization := &expr.AttributeExpr{Type: expr.Int64, Meta: expr.MetaExpr{"struct:field:name": {"Organization"}}}
	value := &expr.AttributeExpr{Type: expr.String}
	definition := &expr.AttributeExpr{
		Type:         &expr.Object{{Name: "organization_id", Attribute: organization}, {Name: "value", Attribute: value}},
		Validation:   &expr.ValidationExpr{Required: []string{"organization_id", "value"}},
		UserExamples: []*expr.ExampleExpr{{Value: expr.Val{"organization_id": int64(7), "value": "example"}}},
		Meta:         expr.MetaExpr{"struct:pkg:path": {"example.local/shared"}},
	}
	payload := &expr.AttributeExpr{Type: &expr.UserTypeExpr{TypeName: "Input", AttributeExpr: definition}}
	scoped := &expr.MethodExpr{Name: "scoped", Payload: payload}
	unscoped := &expr.MethodExpr{Name: "unscoped", Payload: payload}
	transport := argumentTestTransport(scoped, "/organizations/{organization_id}")
	BindTransport(transport)

	selected, err := Arguments(scoped)
	require.NoError(t, err)
	assert.Nil(t, selected.Find("organization_id"))
	assert.Same(t, value, selected.Find("value"))
	named := selected.Type.(expr.UserType)
	assert.Equal(t, []string{"value"}, named.Attribute().Validation.Required)
	assert.Equal(t, definition.Meta, named.Attribute().Meta)
	assert.Same(t, payload.Type.(expr.UserType).Origin(), named.Origin())
	assert.NotContains(t, named.Attribute().UserExamples[0].Value, "organization_id")

	domain, err := Arguments(unscoped)
	require.NoError(t, err)
	assert.Same(t, payload, domain)
	assert.Same(t, organization, domain.Find("organization_id"))
	assert.Equal(t, []string{"organization_id", "value"}, definition.Validation.Required)
	assert.Equal(t, int64(7), definition.UserExamples[0].Value.(expr.Val)["organization_id"])
}

func TestBindTransportReplacesPriorDerivedFields(t *testing.T) {
	method := &expr.MethodExpr{
		Name:    "read",
		Payload: &expr.AttributeExpr{Type: &expr.Object{{Name: "organization_id", Attribute: &expr.AttributeExpr{Type: expr.String}}}},
		Meta:    expr.MetaExpr{pathFieldsKey: {"organization_id"}},
	}
	transport := argumentTestTransport(method, "/records")
	BindTransport(transport)
	selected, err := Arguments(method)
	require.NoError(t, err)
	assert.Same(t, method.Payload, selected)
}

// argumentTestTransport supplies a prepared route whose parameters retain the
// method's authored field declarations, just as Goa's evaluated design does.
func argumentTestTransport(method *expr.MethodExpr, prefix string) *expr.HTTPServiceExpr {
	service := &expr.ServiceExpr{Name: "records", Methods: []*expr.MethodExpr{method}}
	method.Service = service
	transport := &expr.HTTPServiceExpr{
		ServiceExpr: service,
		Root:        &expr.HTTPExpr{},
		Paths:       []string{prefix},
	}
	endpoint := &expr.HTTPEndpointExpr{
		MethodExpr: method,
		Service:    transport,
		Params:     expr.NewMappedAttributeExpr(method.Payload),
	}
	endpoint.Routes = []*expr.RouteExpr{{Method: "POST", Path: "/mcp", Endpoint: endpoint}}
	transport.HTTPEndpoints = []*expr.HTTPEndpointExpr{endpoint}
	return transport
}
