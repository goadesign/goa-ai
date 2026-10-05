// Package mcpinput records which original method fields Goa reads from the URL.
// Design evaluation and generation derive these private compiler facts from
// authored routes; shared payload types are never annotated or changed.
package mcpinput

import "goa.design/goa/v3/expr"

const pathFieldsKey = "mcp:input:path_fields"

// BindTransport replaces each method's recorded URL fields with its authored
// route bindings. Methods selected only by MCP use the shared JSON-RPC route;
// explicit JSON-RPC endpoints retain their own mapped payload field names.
func BindTransport(service *expr.HTTPServiceExpr) {
	for _, method := range service.ServiceExpr.Methods {
		delete(method.Meta, pathFieldsKey)
		names := methodPathFields(service, method)
		if len(names) == 0 {
			continue
		}
		if method.Meta == nil {
			method.Meta = make(expr.MetaExpr)
		}
		method.Meta[pathFieldsKey] = names
	}
}

// methodPathFields reads explicit endpoint mappings or the shared route's
// inherited URL variables. Only fields present on the method become inputs to
// its service call; route values absent from that payload remain transport data.
func methodPathFields(service *expr.HTTPServiceExpr, method *expr.MethodExpr) []string {
	var names []string
	for _, endpoint := range service.HTTPEndpoints {
		if endpoint.MethodExpr != method {
			continue
		}
		for _, field := range *expr.AsObject(endpoint.PathParams().Type) {
			names = append(names, field.Name)
		}
		return names
	}
	if service.JSONRPCRoute == nil {
		return nil
	}
	route := *service.JSONRPCRoute
	route.Endpoint = &expr.HTTPEndpointExpr{Service: service}
	for _, name := range route.Params() {
		if method.Payload != nil && method.Payload.Find(name) != nil {
			names = append(names, name)
		}
	}
	return names
}
