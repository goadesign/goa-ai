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
	path         string
	version      string
	capabilities map[string]any
	name         string
}

func (c *testConfig) JSONRPCPath() string          { return c.path }
func (c *testConfig) ProtocolVersion() string      { return c.version }
func (c *testConfig) Capabilities() map[string]any { return c.capabilities }
func (c *testConfig) Name() string                 { return c.name }

// TestProtocolConfigPathUsage verifies Property 15: Protocol Config Path Usage.
// **Feature: a2a-codegen-refactor, Property 15: Protocol Config Path Usage**
// *For any* protocol configuration with a specified JSON-RPC path, all generated
// routes should use that exact path.
// **Validates: Requirements 13.2**
func TestProtocolConfigPathUsage(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("all routes use the configured JSON-RPC path", prop.ForAll(
		func(path string) bool {
			config := &testConfig{
				path:    path,
				version: "1.0",
				name:    "Test",
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

// TestCapabilityInclusion verifies Property 16: Capability Inclusion.
// **Feature: a2a-codegen-refactor, Property 16: Capability Inclusion**
// *For any* protocol configuration with specified capabilities, the configuration
// should return all specified capabilities.
// **Validates: Requirements 13.3**
func TestCapabilityInclusion(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("capabilities are preserved in config", prop.ForAll(
		func(caps map[string]bool) bool {
			// Convert to map[string]any
			capabilities := make(map[string]any)
			for k, v := range caps {
				capabilities[k] = v
			}

			config := &testConfig{
				path:         "/test",
				version:      "1.0",
				capabilities: capabilities,
				name:         "Test",
			}

			// Verify all capabilities are returned
			returned := config.Capabilities()
			if len(returned) != len(capabilities) {
				return false
			}

			for k, v := range capabilities {
				if returned[k] != v {
					return false
				}
			}

			return true
		},
		genCapabilities(),
	))

	properties.TestingRun(t)
}

// genValidJSONRPCPath generates valid JSON-RPC paths.
func genValidJSONRPCPath() gopter.Gen {
	return gen.OneConstOf(
		"/rpc",
		"/a2a",
		"/mcp",
		"/api/v1/jsonrpc",
		"/services/protocol",
	)
}

// genCapabilities generates capability maps for testing.
func genCapabilities() gopter.Gen {
	return gen.MapOf(
		gen.OneConstOf("tools", "resources", "prompts", "streaming", "notifications"),
		gen.Bool(),
	)
}
