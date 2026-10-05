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

// PathParams returns Goa's mapped URL fields for the original method. An
// explicit endpoint already carries inherited bindings. For an MCP-only method,
// Goa prepares a detached method copy against the same shared route, so parent
// and API mappings are resolved without changing the original payload.
func PathParams(service *expr.HTTPServiceExpr, method *expr.MethodExpr) *expr.MappedAttributeExpr {
	for _, endpoint := range service.HTTPEndpoints {
		if endpoint.MethodExpr == method {
			return endpoint.PathParams()
		}
	}
	if service.JSONRPCRoute == nil {
		return expr.NewEmptyMappedAttributeExpr()
	}
	route := *service.JSONRPCRoute
	route.Endpoint = &expr.HTTPEndpointExpr{Service: service}
	if len(route.Params()) == 0 {
		return expr.NewEmptyMappedAttributeExpr()
	}
	copy := *method
	copy.Payload = expr.DupAtt(method.Payload)
	endpoint := &expr.HTTPEndpointExpr{
		MethodExpr: &copy,
		Service:    service,
		Routes:     []*expr.RouteExpr{&route},
	}
	route.Endpoint = endpoint
	endpoint.Prepare()
	return endpoint.PathParams()
}

// methodPathFields records only mapped fields present in the original payload.
// Other URL values still address the protocol server, but do not become inputs
// to this method's domain call or disappear from unrelated method arguments.
func methodPathFields(service *expr.HTTPServiceExpr, method *expr.MethodExpr) []string {
	var names []string
	for _, field := range *expr.AsObject(PathParams(service, method).Type) {
		if method.Payload.Find(field.Name) != nil {
			names = append(names, field.Name)
		}
	}
	return names
}
