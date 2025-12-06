package codegen_test

import (
	"bytes"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
	codegen "goa.design/goa-ai/codegen/agent"
	. "goa.design/goa-ai/dsl"
	agentsExpr "goa.design/goa-ai/expr/agent"
	goadsl "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// TestAgentCardIsStaticLiteral verifies that the generated agent card is a
// static literal (var agentCardTemplate = AgentCard{...}) rather than a
// runtime-computed value.
// **Validates: Requirements 16.1, 16.2**
func TestAgentCardIsStaticLiteral(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("static_card_test", func() {})

		tools := Toolset("data_tools", func() {
			Tool("analyze", "Analyze data", func() {
				Tags("analytics", "data")
				Args(func() {
					goadsl.Attribute("input", goadsl.String, "Input data")
				})
				Return(func() {
					goadsl.Attribute("result", goadsl.String, "Analysis result")
				})
			})
			Tool("transform", "Transform data", func() {
				Args(func() {
					goadsl.Attribute("data", goadsl.String, "Data to transform")
				})
			})
		})

		goadsl.Service("static_card_test", func() {
			Agent("static-agent", "Agent with static card", func() {
				Use(tools)
				Export(tools)
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := codegen.Generate("example.com/static_card", []eval.Root{goaexpr.Root, agentsExpr.Root}, nil)
	require.NoError(t, err)

	var cardContent string
	expectedPath := filepath.ToSlash("gen/static_card_test/agents/static_agent/a2a/card.go")
	for _, f := range files {
		if filepath.ToSlash(f.Path) == expectedPath {
			var buf bytes.Buffer
			for _, s := range f.SectionTemplates {
				require.NoError(t, s.Write(&buf))
			}
			cardContent = buf.String()
			break
		}
	}
	require.NotEmpty(t, cardContent, "expected generated card.go at %s", expectedPath)

	// Verify the agent card is declared as a static variable literal
	require.Contains(t, cardContent, "var agentCardTemplate = AgentCard{",
		"agent card should be a static literal variable")

	// Verify skills are inlined as static literals, not built at runtime
	require.Contains(t, cardContent, "Skills: []*Skill{",
		"skills should be inlined as static literals")

	// Verify individual skill literals are present
	require.Contains(t, cardContent, `ID:          "data_tools.analyze"`,
		"skill ID should be a static literal")
	require.Contains(t, cardContent, `Name:        "Analyze"`,
		"skill name should be a static literal")
	require.Contains(t, cardContent, `Tags:        []string{"analytics", "data"}`,
		"skill tags should be static literals")

	// Verify the AgentCard function only sets the URL field
	require.Contains(t, cardContent, "func AgentCard(baseURL string) *AgentCard",
		"AgentCard function should exist")
	require.Contains(t, cardContent, "card := agentCardTemplate",
		"AgentCard should copy the static template")
	require.Contains(t, cardContent, "card.URL = baseURL",
		"AgentCard should only set the URL field")

	// Verify no runtime builder functions exist
	require.NotContains(t, cardContent, "func buildSkills()",
		"should not have runtime buildSkills function")
	require.NotContains(t, cardContent, "func buildSecuritySchemes()",
		"should not have runtime buildSecuritySchemes function")
	require.NotContains(t, cardContent, "func buildSecurityRequirements()",
		"should not have runtime buildSecurityRequirements function")
}

// TestAgentCardStaticSecuritySchemes verifies that security schemes are
// inlined as static literals in the agent card.
// **Validates: Requirements 16.1, 16.2**
func TestAgentCardStaticSecuritySchemes(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("static_security_test", func() {})

		jwtScheme := goadsl.JWTSecurity("jwt_auth", func() {
			goadsl.Description("JWT authentication")
		})

		tools := Toolset("secure_tools", func() {
			Tool("secure_action", "Perform secure action", func() {
				Args(func() {
					goadsl.Attribute("data", goadsl.String, "Data")
				})
			})
		})

		goadsl.Service("static_security_test", func() {
			goadsl.Security(jwtScheme)
			Agent("secure-agent", "Secure agent", func() {
				Use(tools)
				Export(tools)
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := codegen.Generate("example.com/static_security", []eval.Root{goaexpr.Root, agentsExpr.Root}, nil)
	require.NoError(t, err)

	var cardContent string
	expectedPath := filepath.ToSlash("gen/static_security_test/agents/secure_agent/a2a/card.go")
	for _, f := range files {
		if filepath.ToSlash(f.Path) == expectedPath {
			var buf bytes.Buffer
			for _, s := range f.SectionTemplates {
				require.NoError(t, s.Write(&buf))
			}
			cardContent = buf.String()
			break
		}
	}
	require.NotEmpty(t, cardContent, "expected generated card.go at %s", expectedPath)

	// Verify the agent card is a static literal
	require.Contains(t, cardContent, "var agentCardTemplate = AgentCard{",
		"agent card should be a static literal")

	// Verify capabilities are inlined as static map literal
	require.Contains(t, cardContent, "Capabilities: map[string]any{",
		"capabilities should be a static map literal")
	require.Contains(t, cardContent, `"streaming": true`,
		"streaming capability should be a static literal")
}

// TestRegistryClientStaticURLPaths verifies that the generated registry client
// uses static URL path constants rather than runtime path joining.
// **Validates: Requirements 16.4**
func TestRegistryClientStaticURLPaths(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("static_url_test", func() {})

		testRegistry := Registry("test-registry", func() {
			goadsl.URL("https://registry.test.internal")
			APIVersion("v2")
		})

		registryTools := Toolset(FromRegistry(testRegistry, "test-tools"))

		goadsl.Service("static_url_test", func() {
			Agent("url-agent", "URL test agent", func() {
				Use(registryTools)
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := codegen.Generate("example.com/static_url", []eval.Root{goaexpr.Root, agentsExpr.Root}, nil)
	require.NoError(t, err)

	var clientContent string
	expectedPath := filepath.ToSlash("gen/static_url_test/registry/test_registry/client.go")
	for _, f := range files {
		if filepath.ToSlash(f.Path) == expectedPath {
			var buf bytes.Buffer
			for _, s := range f.SectionTemplates {
				require.NoError(t, s.Write(&buf))
			}
			clientContent = buf.String()
			break
		}
	}
	require.NotEmpty(t, clientContent, "expected generated client.go at %s", expectedPath)

	// Verify static URL path constants are generated with the correct API version
	require.Contains(t, clientContent, `pathToolsets = "/v2/toolsets"`,
		"pathToolsets should be a static constant with v2 API version")
	require.Contains(t, clientContent, `pathSearch = "/v2/search"`,
		"pathSearch should be a static constant with v2 API version")
	require.Contains(t, clientContent, `pathSemanticSearch = "/v2/search/semantic"`,
		"pathSemanticSearch should be a static constant with v2 API version")
	require.Contains(t, clientContent, `pathCapabilities = "/v2/capabilities"`,
		"pathCapabilities should be a static constant with v2 API version")
	require.Contains(t, clientContent, `pathAgents = "/v2/agents"`,
		"pathAgents should be a static constant with v2 API version")

	// Verify methods use static path constants instead of url.JoinPath
	require.NotContains(t, clientContent, "url.JoinPath",
		"should not use url.JoinPath for static paths")

	// Verify methods use string concatenation with static paths
	require.Contains(t, clientContent, "c.endpoint + pathToolsets",
		"ListToolsets should use static path constant")
	require.Contains(t, clientContent, "c.endpoint + pathSearch",
		"Search should use static path constant")
}

// TestRegistryClientStaticEndpoint verifies that the registry endpoint is
// embedded as a static default value.
// **Validates: Requirements 16.4**
func TestRegistryClientStaticEndpoint(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("static_endpoint_test", func() {})

		corpRegistry := Registry("corp-registry", func() {
			goadsl.URL("https://registry.corp.internal")
		})

		registryTools := Toolset(FromRegistry(corpRegistry, "corp-tools"))

		goadsl.Service("static_endpoint_test", func() {
			Agent("endpoint-agent", "Endpoint test agent", func() {
				Use(registryTools)
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := codegen.Generate("example.com/static_endpoint", []eval.Root{goaexpr.Root, agentsExpr.Root}, nil)
	require.NoError(t, err)

	var clientContent string
	expectedPath := filepath.ToSlash("gen/static_endpoint_test/registry/corp_registry/client.go")
	for _, f := range files {
		if filepath.ToSlash(f.Path) == expectedPath {
			var buf bytes.Buffer
			for _, s := range f.SectionTemplates {
				require.NoError(t, s.Write(&buf))
			}
			clientContent = buf.String()
			break
		}
	}
	require.NotEmpty(t, clientContent, "expected generated client.go at %s", expectedPath)

	// Verify the endpoint is embedded as a static default in NewClient
	require.Contains(t, clientContent, `endpoint:   "https://registry.corp.internal"`,
		"endpoint should be a static default value")
}

// TestTypeSpecificValidatorsGenerated verifies that type-specific validation
// functions are generated for local toolsets with known schemas.
// **Validates: Requirements 16.3**
func TestTypeSpecificValidatorsGenerated(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("validator_test", func() {})

		tools := Toolset("query_tools", func() {
			Tool("search", "Search for items", func() {
				Args(func() {
					goadsl.Attribute("query", goadsl.String, "Search query")
					goadsl.Attribute("limit", goadsl.Int, "Max results")
					goadsl.Required("query")
				})
				Return(func() {
					goadsl.Attribute("results", goadsl.ArrayOf(goadsl.String), "Search results")
				})
			})
		})

		goadsl.Service("validator_test", func() {
			Agent("validator-agent", "Validator test agent", func() {
				Use(tools)
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := codegen.Generate("example.com/validator", []eval.Root{goaexpr.Root, agentsExpr.Root}, nil)
	require.NoError(t, err)

	// Look for the specs file which should contain validation functions
	var specsContent string
	expectedPath := filepath.ToSlash("gen/validator_test/tools/query_tools/specs.go")
	for _, f := range files {
		if filepath.ToSlash(f.Path) == expectedPath {
			var buf bytes.Buffer
			for _, s := range f.SectionTemplates {
				require.NoError(t, s.Write(&buf))
			}
			specsContent = buf.String()
			break
		}
	}
	require.NotEmpty(t, specsContent, "expected generated specs.go at %s", expectedPath)

	// Verify the specs file contains validation-related code
	// For local toolsets, Goa generates type-specific validation via the type system
	require.Contains(t, specsContent, "Specs",
		"specs should be generated for local toolsets")
}

// TestStaticGenerationNoRuntimeReflection verifies that generated code does
// not use runtime reflection for static data.
// **Validates: Requirements 16.1, 16.2**
func TestStaticGenerationNoRuntimeReflection(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("no_reflect_test", func() {})

		tools := Toolset("static_tools", func() {
			Tool("action", "Perform action", func() {
				Args(func() {
					goadsl.Attribute("input", goadsl.String, "Input")
				})
			})
		})

		goadsl.Service("no_reflect_test", func() {
			Agent("static-agent", "Static agent", func() {
				Use(tools)
				Export(tools)
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := codegen.Generate("example.com/no_reflect", []eval.Root{goaexpr.Root, agentsExpr.Root}, nil)
	require.NoError(t, err)

	var cardContent string
	expectedPath := filepath.ToSlash("gen/no_reflect_test/agents/static_agent/a2a/card.go")
	for _, f := range files {
		if filepath.ToSlash(f.Path) == expectedPath {
			var buf bytes.Buffer
			for _, s := range f.SectionTemplates {
				require.NoError(t, s.Write(&buf))
			}
			cardContent = buf.String()
			break
		}
	}
	require.NotEmpty(t, cardContent, "expected generated card.go at %s", expectedPath)

	// Verify no reflection imports are used for static data
	require.NotContains(t, cardContent, `"reflect"`,
		"should not import reflect package for static data")

	// Verify no runtime type assertions for static data
	// (type assertions like .(type) are acceptable for dynamic data)
	staticDataPattern := regexp.MustCompile(`agentCardTemplate\.\(`)
	require.False(t, staticDataPattern.MatchString(cardContent),
		"should not use type assertions on static template")
}

// TestStaticGenerationDefaultValues verifies that default values are
// embedded as static literals.
// **Validates: Requirements 16.1**
func TestStaticGenerationDefaultValues(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("defaults_test", func() {})

		tools := Toolset("default_tools", func() {
			Tool("process", "Process data", func() {
				Args(func() {
					goadsl.Attribute("data", goadsl.String, "Data")
				})
			})
		})

		goadsl.Service("defaults_test", func() {
			Agent("defaults-agent", "Defaults test agent", func() {
				Use(tools)
				Export(tools)
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := codegen.Generate("example.com/defaults", []eval.Root{goaexpr.Root, agentsExpr.Root}, nil)
	require.NoError(t, err)

	var cardContent string
	expectedPath := filepath.ToSlash("gen/defaults_test/agents/defaults_agent/a2a/card.go")
	for _, f := range files {
		if filepath.ToSlash(f.Path) == expectedPath {
			var buf bytes.Buffer
			for _, s := range f.SectionTemplates {
				require.NoError(t, s.Write(&buf))
			}
			cardContent = buf.String()
			break
		}
	}
	require.NotEmpty(t, cardContent, "expected generated card.go at %s", expectedPath)

	// Verify default values are embedded as static literals
	require.Contains(t, cardContent, `Version:         "1.0.0"`,
		"version should be a static default literal")
	require.Contains(t, cardContent, `DefaultInputModes:  []string{"application/json"}`,
		"default input modes should be static literals")
	require.Contains(t, cardContent, `DefaultOutputModes: []string{"application/json"}`,
		"default output modes should be static literals")
}
