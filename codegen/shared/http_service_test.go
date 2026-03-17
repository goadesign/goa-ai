package shared

import (
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"goa.design/goa/v3/expr"
)

// testConfig implements ProtocolConfig for testing.
type testConfig struct {
	path string
}

func (c *testConfig) JSONRPCPath() string { return c.path }

// TestProtocolConfigPathUsage verifies Property 15: Protocol Config Path Usage.
// *For any* protocol configuration with a specified JSON-RPC path, all generated
// routes should use that exact path.
func TestProtocolConfigPathUsage(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("all routes use the configured JSON-RPC path", prop.ForAll(
		func(path string) bool {
			config := &testConfig{
				path: path,
			}

			// Create a simple service with methods
			service := &expr.ServiceExpr{
				Name: "test_service",
				Methods: []*expr.MethodExpr{
					{Name: "method1", Payload: &expr.AttributeExpr{Type: expr.String}},
					{Name: "method2", Payload: &expr.AttributeExpr{Type: expr.String}},
				},
			}
			for _, m := range service.Methods {
				m.Service = service
			}

			httpService := BuildHTTPServiceBase(service, config)

			// Verify JSONRPCRoute uses the configured path
			if httpService.JSONRPCRoute.Path != path {
				return false
			}

			// Verify all endpoint routes use the configured path
			for _, endpoint := range httpService.HTTPEndpoints {
				for _, route := range endpoint.Routes {
					if route.Path != path {
						return false
					}
				}
			}

			return true
		},
		genValidJSONRPCPath(),
	))

	properties.TestingRun(t)
}

func TestBuildHTTPServiceBase_ConfiguresStreamingEndpoints(t *testing.T) {
	service := &expr.ServiceExpr{
		Name: "test_service",
		Methods: []*expr.MethodExpr{
			{
				Name:    "watch",
				Payload: &expr.AttributeExpr{Type: expr.String},
				Stream:  expr.ServerStreamKind,
			},
		},
	}
	service.Methods[0].Service = service

	httpService := BuildHTTPServiceBase(service, &testConfig{path: "/rpc"})

	if httpService.JSONRPCRoute.Path != "/rpc" {
		t.Fatalf("JSONRPCRoute.Path = %q, want /rpc", httpService.JSONRPCRoute.Path)
	}
	if len(httpService.HTTPEndpoints) != 1 {
		t.Fatalf("len(HTTPEndpoints) = %d, want 1", len(httpService.HTTPEndpoints))
	}
	if httpService.HTTPEndpoints[0].SSE == nil {
		t.Fatal("expected streaming endpoint SSE configuration")
	}
}

// genValidJSONRPCPath generates valid JSON-RPC paths.
func genValidJSONRPCPath() gopter.Gen {
	return gen.OneConstOf(
		"/rpc",
		"/mcp",
		"/api/v1/jsonrpc",
		"/services/protocol",
	)
}
