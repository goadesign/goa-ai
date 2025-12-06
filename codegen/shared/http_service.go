package shared

import (
	"goa.design/goa/v3/expr"
)

// BuildHTTPServiceBase creates the HTTP/JSON-RPC service expression with
// common configuration for routes and SSE endpoints. It uses the provided
// ProtocolConfig to determine the JSON-RPC path.
func BuildHTTPServiceBase(service *expr.ServiceExpr, config ProtocolConfig) *expr.HTTPServiceExpr {
	jsonrpcPath := config.JSONRPCPath()

	httpService := &expr.HTTPServiceExpr{
		ServiceExpr: service,
		JSONRPCRoute: &expr.RouteExpr{
			Method: "POST",
			Path:   jsonrpcPath,
		},
		Paths: []string{},
		SSE:   &expr.HTTPSSEExpr{},
	}
	// Ensure the JSONRPCRoute can compute full paths
	httpService.JSONRPCRoute.Endpoint = &expr.HTTPEndpointExpr{Service: httpService}

	// Create endpoints for each method
	for _, method := range service.Methods {
		endpoint := &expr.HTTPEndpointExpr{
			MethodExpr: method,
			Service:    httpService,
			Meta: expr.MetaExpr{
				"jsonrpc": []string{},
			},
			Routes: []*expr.RouteExpr{},
		}
		// Ensure JSON-RPC decoders decode params into payload by setting body to method payload
		endpoint.Body = method.Payload
		// Ensure mapped attributes are non-nil for codegen analyze paths
		endpoint.Params = expr.NewEmptyMappedAttributeExpr()
		endpoint.Headers = expr.NewEmptyMappedAttributeExpr()
		endpoint.Cookies = expr.NewEmptyMappedAttributeExpr()
		// Create the route and set its Endpoint back-reference
		rt := &expr.RouteExpr{Method: "POST", Path: jsonrpcPath, Endpoint: endpoint}
		endpoint.Routes = []*expr.RouteExpr{rt}

		// For streaming methods, configure SSE
		if method.Stream == expr.ServerStreamKind {
			endpoint.SSE = &expr.HTTPSSEExpr{}
		}

		httpService.HTTPEndpoints = append(httpService.HTTPEndpoints, endpoint)
	}

	return httpService
}
