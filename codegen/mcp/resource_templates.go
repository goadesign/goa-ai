// Package codegen generates resource-template discovery without treating discovery
// templates as routing or authorization rules. The generated resource reader
// receives the exact URI and returns typed text or binary content.
package codegen

import (
	"github.com/yosida95/uritemplate/v3"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/expr"
)

type (
	// resourceTemplateAdapter contains declared discovery fields and variable names.
	resourceTemplateAdapter struct {
		// Name identifies the template in discovery.
		Name string
		// URI is the exact declared expansion template.
		URI string
		// Description explains the available resources.
		Description string
		// MimeType hints at the expected content type.
		MimeType string
		// Variables contains the names accepted by completion requests.
		Variables []string
	}
)

// buildResourceTemplateAdapters derives completion names during generation.
func buildResourceTemplateAdapters(declarations []*mcpexpr.ResourceTemplateExpr) ([]*resourceTemplateAdapter, error) {
	adapters := make([]*resourceTemplateAdapter, 0, len(declarations))
	for _, declaration := range declarations {
		template, err := uritemplate.New(declaration.URI)
		if err != nil {
			return nil, err
		}
		adapters = append(adapters, &resourceTemplateAdapter{Name: declaration.Name, URI: declaration.URI, Description: declaration.Description, MimeType: declaration.MimeType, Variables: template.Varnames()})
	}
	return adapters, nil
}

// buildResourceTemplatesListMethod returns the parameterized address catalog.
// Resource-capable services also expose this operation with an empty catalog.
func (b *mcpExprBuilder) buildResourceTemplatesListMethod() *expr.MethodExpr {
	return &expr.MethodExpr{
		Name:        "resources/templates/list",
		Description: "List URI templates clients can expand to select resources",
		Payload:     b.userTypeAttr("ResourceTemplatesListPayload", b.buildListPayloadType),
		Result:      b.userTypeAttr("ResourceTemplatesListResult", b.buildResourceTemplatesListResult),
		Errors:      buildMCPMethodErrors(mcpInvalidParamsError),
	}
}

// buildResourceTemplatesListResult retains the declared address and its hints.
func (b *mcpExprBuilder) buildResourceTemplatesListResult() *expr.AttributeExpr {
	info := b.getOrCreateType("ResourceTemplateInfo", b.buildResourceTemplateInfoType)
	return &expr.AttributeExpr{Type: &expr.Object{
		{Name: "resourceTemplates", Attribute: &expr.AttributeExpr{Type: &expr.Array{ElemType: &expr.AttributeExpr{Type: info}, NonNullableElems: true}, Description: "Parameterized addresses advertised by this service"}},
		{Name: "nextCursor", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "Cursor for another catalog page"}},
	}, Validation: &expr.ValidationExpr{Required: []string{"resourceTemplates"}}}
}

// buildResourceTemplateInfoType retains complete typed discovery hints. The
// catalog owner supplies its RFC 6570 address; reading remains URI-based.
func (b *mcpExprBuilder) buildResourceTemplateInfoType() *expr.AttributeExpr {
	fields := b.resourceMetadataFields()
	fields = append(fields, &expr.NamedAttributeExpr{Name: "uriTemplate", Attribute: &expr.AttributeExpr{Type: expr.String, Description: "RFC 6570 template expanded by the client"}})
	return &expr.AttributeExpr{Type: &fields, Validation: &expr.ValidationExpr{Required: []string{"uriTemplate", "name"}}}
}
