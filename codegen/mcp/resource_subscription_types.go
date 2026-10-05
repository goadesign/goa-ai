// Package codegen adds the core subscription filter and finished result to the
// generated protocol service. An authored resource stream receives only URI
// selections; unimplemented catalog notifications are omitted from its acknowledgment.
package codegen

import "goa.design/goa/v3/expr"

// buildSubscriptionsListenMethod adds a distinct long-lived operation only when
// the service declares a resource update source. Its endpoint returns a normal
// finished result; the shared transport sends request-scoped notifications.
func (b *mcpExprBuilder) buildSubscriptionsListenMethod() *expr.MethodExpr {
	filter := b.getOrCreateType("SubscriptionFilter", func() *expr.AttributeExpr {
		return &expr.AttributeExpr{Type: &expr.Object{
			{Name: "toolsListChanged", Attribute: &expr.AttributeExpr{Type: expr.Boolean, Description: "Request changes to the tool catalog"}},
			{Name: "promptsListChanged", Attribute: &expr.AttributeExpr{Type: expr.Boolean, Description: "Request changes to the prompt catalog"}},
			{Name: "resourcesListChanged", Attribute: &expr.AttributeExpr{Type: expr.Boolean, Description: "Request changes to the resource catalog"}},
			{Name: "resourceSubscriptions", Attribute: &expr.AttributeExpr{Type: &expr.Array{ElemType: &expr.AttributeExpr{Type: expr.String, Validation: &expr.ValidationExpr{Format: expr.FormatURI}}}, Description: "Exact resource URIs requested by this listen operation"}},
		}}
	})
	return &expr.MethodExpr{
		Name:        "subscriptions/listen",
		Description: "Authorize requested resource URIs and send their changes until this request ends",
		Payload: b.userTypeAttr("SubscriptionsListenPayload", func() *expr.AttributeExpr {
			return &expr.AttributeExpr{Type: &expr.Object{{Name: "notifications", Attribute: &expr.AttributeExpr{Type: filter, Description: "Notification kinds and resource URIs requested by the client"}}}, Validation: &expr.ValidationExpr{Required: []string{"notifications"}}}
		}),
		Result: b.userTypeAttr("SubscriptionsListenResult", func() *expr.AttributeExpr { return &expr.AttributeExpr{Type: &expr.Object{}} }),
		Errors: buildMCPMethodErrors(mcpDispatchErrors[:]...),
	}
}
