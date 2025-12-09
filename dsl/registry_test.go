package dsl_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	. "goa.design/goa-ai/dsl"
	agentsexpr "goa.design/goa-ai/expr/agent"
	. "goa.design/goa/v3/dsl"
	goaexpr "goa.design/goa/v3/expr"
)

// TestRegistrySecurityHolder verifies that RegistryExpr implements
// goaexpr.SecurityHolder, allowing Security() DSL to work inside Registry.
func TestRegistrySecurityHolder(t *testing.T) {
	runDSL(t, func() {
		// Define a security scheme at top level
		apiKey := APIKeySecurity("corp_api_key", func() {})

		// Use Security() inside Registry - this works because RegistryExpr
		// implements SecurityHolder interface
		Registry("corp-registry", func() {
			URL("https://registry.corp.internal")
			Security(apiKey)
		})
	})

	require.Len(t, agentsexpr.Root.Registries, 1)
	reg := agentsexpr.Root.Registries[0]
	require.Equal(t, "corp-registry", reg.Name)
	require.Equal(t, "https://registry.corp.internal", reg.URL)
	require.Len(t, reg.Requirements, 1, "Security() should add requirement via SecurityHolder")
	require.Len(t, reg.Requirements[0].Schemes, 1)
	require.Equal(t, "corp_api_key", reg.Requirements[0].Schemes[0].SchemeName)
	require.Equal(t, goaexpr.APIKeyKind, reg.Requirements[0].Schemes[0].Kind)
}

// TestRegistryURLHolder verifies that RegistryExpr implements
// goaexpr.URLHolder, allowing URL() DSL to work inside Registry.
func TestRegistryURLHolder(t *testing.T) {
	runDSL(t, func() {
		// Use URL() inside Registry - this works because RegistryExpr
		// implements URLHolder interface
		Registry("test-registry", func() {
			URL("https://registry.example.com/api")
		})
	})

	require.Len(t, agentsexpr.Root.Registries, 1)
	reg := agentsexpr.Root.Registries[0]
	require.Equal(t, "test-registry", reg.Name)
	require.Equal(t, "https://registry.example.com/api", reg.URL, "URL() should set URL via URLHolder")
}

// TestRegistryWithOAuth2Security verifies OAuth2 security schemes work with Registry.
func TestRegistryWithOAuth2Security(t *testing.T) {
	runDSL(t, func() {
		oauth := OAuth2Security("anthropic_oauth", func() {
			ClientCredentialsFlow(
				"https://auth.anthropic.com/oauth/token",
				"",
			)
			Scope("registry:read", "Read access to registry")
		})

		Registry("anthropic", func() {
			URL("https://registry.anthropic.com/v1")
			Security(oauth)
		})
	})

	require.Len(t, agentsexpr.Root.Registries, 1)
	reg := agentsexpr.Root.Registries[0]
	require.Equal(t, "anthropic", reg.Name)
	require.Len(t, reg.Requirements, 1)
	require.Len(t, reg.Requirements[0].Schemes, 1)
	scheme := reg.Requirements[0].Schemes[0]
	require.Equal(t, "anthropic_oauth", scheme.SchemeName)
	require.Equal(t, goaexpr.OAuth2Kind, scheme.Kind)
}

// TestRegistryWithJWTSecurity verifies JWT security schemes work with Registry.
func TestRegistryWithJWTSecurity(t *testing.T) {
	runDSL(t, func() {
		jwt := JWTSecurity("registry_jwt", func() {
			Scope("read", "Read access")
			Scope("write", "Write access")
		})

		Registry("secure-registry", func() {
			URL("https://secure.registry.io")
			Security(jwt)
		})
	})

	require.Len(t, agentsexpr.Root.Registries, 1)
	reg := agentsexpr.Root.Registries[0]
	require.Len(t, reg.Requirements, 1)
	scheme := reg.Requirements[0].Schemes[0]
	require.Equal(t, "registry_jwt", scheme.SchemeName)
	require.Equal(t, goaexpr.JWTKind, scheme.Kind)
}

// TestRegistryWithMultipleSecuritySchemes verifies multiple security schemes
// can be added to a Registry via SecurityHolder.
func TestRegistryWithMultipleSecuritySchemes(t *testing.T) {
	runDSL(t, func() {
		apiKey := APIKeySecurity("api_key", func() {})
		jwt := JWTSecurity("jwt_auth", func() {})

		Registry("multi-auth-registry", func() {
			URL("https://registry.example.com")
			// Multiple Security() calls add multiple requirements
			Security(apiKey)
			Security(jwt)
		})
	})

	require.Len(t, agentsexpr.Root.Registries, 1)
	reg := agentsexpr.Root.Registries[0]
	require.Len(t, reg.Requirements, 2, "Multiple Security() calls should add multiple requirements")
	require.Equal(t, "api_key", reg.Requirements[0].Schemes[0].SchemeName)
	require.Equal(t, "jwt_auth", reg.Requirements[1].Schemes[0].SchemeName)
}

// TestRegistryFullConfiguration verifies a complete Registry configuration
// using both SecurityHolder and URLHolder interfaces.
func TestRegistryFullConfiguration(t *testing.T) {
	runDSL(t, func() {
		apiKey := APIKeySecurity("corp_key", func() {})

		Registry("corp-registry", func() {
			URL("https://registry.corp.internal")
			APIVersion("v2")
			Security(apiKey)
			Timeout("30s")
			Retry(3, "1s")
			SyncInterval("5m")
			CacheTTL("1h")
			Federation(func() {
				Include("web-search", "code-execution")
				Exclude("experimental/*")
			})
		})
	})

	require.Len(t, agentsexpr.Root.Registries, 1)
	reg := agentsexpr.Root.Registries[0]

	// Verify URLHolder worked
	require.Equal(t, "https://registry.corp.internal", reg.URL)

	// Verify SecurityHolder worked
	require.Len(t, reg.Requirements, 1)
	require.Equal(t, "corp_key", reg.Requirements[0].Schemes[0].SchemeName)

	// Verify other configuration
	require.Equal(t, "v2", reg.APIVersion)
	require.Equal(t, 30*time.Second, reg.Timeout)
	require.NotNil(t, reg.RetryPolicy)
	require.Equal(t, 3, reg.RetryPolicy.MaxRetries)
	require.Equal(t, time.Second, reg.RetryPolicy.BackoffBase)
	require.Equal(t, 5*time.Minute, reg.SyncInterval)
	require.Equal(t, time.Hour, reg.CacheTTL)
	require.NotNil(t, reg.Federation)
	require.Equal(t, []string{"web-search", "code-execution"}, reg.Federation.Include)
	require.Equal(t, []string{"experimental/*"}, reg.Federation.Exclude)
}

// TestRegistrySecurityByName verifies Security() works with scheme name string.
func TestRegistrySecurityByName(t *testing.T) {
	runDSL(t, func() {
		APIKeySecurity("named_key", func() {})

		Registry("named-auth-registry", func() {
			URL("https://registry.example.com")
			Security("named_key") // Reference by name
		})
	})

	require.Len(t, agentsexpr.Root.Registries, 1)
	reg := agentsexpr.Root.Registries[0]
	require.Len(t, reg.Requirements, 1)
	require.Equal(t, "named_key", reg.Requirements[0].Schemes[0].SchemeName)
}

// TestFromRegistryProvider verifies FromRegistry creates a registry-backed toolset.
func TestFromRegistryProvider(t *testing.T) {
	runDSL(t, func() {
		reg := Registry("corp-registry", func() {
			URL("https://registry.corp.internal")
		})

		Toolset(FromRegistry(reg, "data-tools"))
	})

	require.Len(t, agentsexpr.Root.Toolsets, 1)
	ts := agentsexpr.Root.Toolsets[0]
	require.Equal(t, "data-tools", ts.Name, "Name should be derived from toolset name")
	require.NotNil(t, ts.Provider)
	require.Equal(t, agentsexpr.ProviderRegistry, ts.Provider.Kind)
	require.Equal(t, "data-tools", ts.Provider.ToolsetName)
	require.NotNil(t, ts.Provider.Registry)
	require.Equal(t, "corp-registry", ts.Provider.Registry.Name)
}

// TestFromRegistryWithExplicitName verifies FromRegistry with explicit toolset name.
func TestFromRegistryWithExplicitName(t *testing.T) {
	runDSL(t, func() {
		reg := Registry("corp-registry", func() {
			URL("https://registry.corp.internal")
		})

		Toolset("my-tools", FromRegistry(reg, "data-tools"))
	})

	require.Len(t, agentsexpr.Root.Toolsets, 1)
	ts := agentsexpr.Root.Toolsets[0]
	require.Equal(t, "my-tools", ts.Name, "Explicit name should override derived name")
	require.NotNil(t, ts.Provider)
	require.Equal(t, agentsexpr.ProviderRegistry, ts.Provider.Kind)
	require.Equal(t, "data-tools", ts.Provider.ToolsetName)
}

// TestFromRegistryWithVersion verifies version pinning for registry toolsets.
func TestFromRegistryWithVersion(t *testing.T) {
	runDSL(t, func() {
		reg := Registry("corp-registry", func() {
			URL("https://registry.corp.internal")
		})

		Toolset(FromRegistry(reg, "data-tools"), func() {
			Version("1.2.3")
		})
	})

	require.Len(t, agentsexpr.Root.Toolsets, 1)
	ts := agentsexpr.Root.Toolsets[0]
	require.NotNil(t, ts.Provider)
	require.Equal(t, agentsexpr.ProviderRegistry, ts.Provider.Kind)
	require.Equal(t, "1.2.3", ts.Provider.Version)
}

// TestPublishToInExport verifies PublishTo works inside Export.
func TestPublishToInExport(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})

		reg := Registry("corp-registry", func() {
			URL("https://registry.corp.internal")
		})

		localTools := Toolset("utils", func() {
			Tool("summarize", "Summarize text", func() {})
		})

		Service("data-svc", func() {
			Agent("data-agent", "Data processing agent", func() {
				Use(localTools)
				Export(localTools, func() {
					PublishTo(reg)
				})
			})
		})
	})

	require.Len(t, agentsexpr.Root.Agents, 1)
	agent := agentsexpr.Root.Agents[0]
	require.NotNil(t, agent.Exported)
	require.Len(t, agent.Exported.Toolsets, 1)
	exported := agent.Exported.Toolsets[0]
	require.Len(t, exported.PublishTo, 1)
	require.Equal(t, "corp-registry", exported.PublishTo[0].Name)
}

// TestPublishToMultipleRegistries verifies PublishTo can target multiple registries.
func TestPublishToMultipleRegistries(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})

		corpReg := Registry("corp-registry", func() {
			URL("https://registry.corp.internal")
		})
		publicReg := Registry("public-registry", func() {
			URL("https://registry.public.io")
		})

		localTools := Toolset("utils", func() {
			Tool("summarize", "Summarize text", func() {})
		})

		Service("data-svc", func() {
			Agent("data-agent", "Data processing agent", func() {
				Use(localTools)
				Export(localTools, func() {
					PublishTo(corpReg)
					PublishTo(publicReg)
				})
			})
		})
	})

	require.Len(t, agentsexpr.Root.Agents, 1)
	agent := agentsexpr.Root.Agents[0]
	require.NotNil(t, agent.Exported)
	require.Len(t, agent.Exported.Toolsets, 1)
	exported := agent.Exported.Toolsets[0]
	require.Len(t, exported.PublishTo, 2)
	require.Equal(t, "corp-registry", exported.PublishTo[0].Name)
	require.Equal(t, "public-registry", exported.PublishTo[1].Name)
}

// TestFromMCPProviderExpr verifies FromMCP creates a provider expression.
// Note: Full MCP toolset resolution requires a service with MCP enabled,
// which is tested in TestProviderInference_LocalAndMCP in dsl_test.go.
func TestFromMCPProviderExpr(t *testing.T) {
	// Test that FromMCP returns a valid provider expression
	provider := FromMCP("assistant-service", "assistant-mcp")
	require.NotNil(t, provider)
	require.Equal(t, agentsexpr.ProviderMCP, provider.Kind)
	require.Equal(t, "assistant-service", provider.MCPService)
	require.Equal(t, "assistant-mcp", provider.MCPToolset)
}

// TestFromRegistryProviderExpr verifies FromRegistry creates a provider expression.
func TestFromRegistryProviderExpr(t *testing.T) {
	runDSL(t, func() {
		reg := Registry("corp-registry", func() {
			URL("https://registry.corp.internal")
		})

		// Test that FromRegistry returns a valid provider expression
		provider := FromRegistry(reg, "data-tools")
		require.NotNil(t, provider)
		require.Equal(t, agentsexpr.ProviderRegistry, provider.Kind)
		require.Equal(t, "data-tools", provider.ToolsetName)
		require.NotNil(t, provider.Registry)
		require.Equal(t, "corp-registry", provider.Registry.Name)
	})
}

// TestFromA2AProviderExpr verifies FromA2A creates an A2A provider expression.
func TestFromA2AProviderExpr(t *testing.T) {
	provider := FromA2A("svc.agent.tools", "https://provider.example.com")
	require.NotNil(t, provider)
	require.Equal(t, agentsexpr.ProviderA2A, provider.Kind)
	require.Equal(t, "svc.agent.tools", provider.A2ASuite)
	require.Equal(t, "https://provider.example.com", provider.A2AURL)
}

// TestRegistryMinimalConfiguration verifies Registry works with minimal config.
func TestRegistryMinimalConfiguration(t *testing.T) {
	runDSL(t, func() {
		Registry("minimal-registry", func() {
			URL("https://registry.example.com")
		})
	})

	require.Len(t, agentsexpr.Root.Registries, 1)
	reg := agentsexpr.Root.Registries[0]
	require.Equal(t, "minimal-registry", reg.Name)
	require.Equal(t, "https://registry.example.com", reg.URL)
	// APIVersion defaults to "v1" in Prepare()
	require.Equal(t, "v1", reg.APIVersion)
	require.Zero(t, reg.Timeout)
	require.Nil(t, reg.RetryPolicy)
	require.Zero(t, reg.SyncInterval)
	require.Zero(t, reg.CacheTTL)
	require.Nil(t, reg.Federation)
}

// TestFederationIncludeOnly verifies Federation with only Include patterns.
func TestFederationIncludeOnly(t *testing.T) {
	runDSL(t, func() {
		Registry("federated-registry", func() {
			URL("https://registry.example.com")
			Federation(func() {
				Include("web-*", "data-*")
			})
		})
	})

	require.Len(t, agentsexpr.Root.Registries, 1)
	reg := agentsexpr.Root.Registries[0]
	require.NotNil(t, reg.Federation)
	require.Equal(t, []string{"web-*", "data-*"}, reg.Federation.Include)
	require.Empty(t, reg.Federation.Exclude)
}

// TestFederationExcludeOnly verifies Federation with only Exclude patterns.
func TestFederationExcludeOnly(t *testing.T) {
	runDSL(t, func() {
		Registry("federated-registry", func() {
			URL("https://registry.example.com")
			Federation(func() {
				Exclude("experimental/*", "deprecated/*")
			})
		})
	})

	require.Len(t, agentsexpr.Root.Registries, 1)
	reg := agentsexpr.Root.Registries[0]
	require.NotNil(t, reg.Federation)
	require.Empty(t, reg.Federation.Include)
	require.Equal(t, []string{"experimental/*", "deprecated/*"}, reg.Federation.Exclude)
}

// TestToolsetWithDescription verifies Description works inside Toolset.
func TestToolsetWithDescription(t *testing.T) {
	runDSL(t, func() {
		Toolset("described-toolset", func() {
			Description("A toolset with a description")
			Tool("tool1", "A tool", func() {})
		})
	})

	require.Len(t, agentsexpr.Root.Toolsets, 1)
	ts := agentsexpr.Root.Toolsets[0]
	require.Equal(t, "A toolset with a description", ts.Description)
}

// TestLocalToolsetProvider verifies local toolsets have no provider.
func TestLocalToolsetProvider(t *testing.T) {
	runDSL(t, func() {
		Toolset("local-tools", func() {
			Tool("tool1", "A tool", func() {})
		})
	})

	require.Len(t, agentsexpr.Root.Toolsets, 1)
	ts := agentsexpr.Root.Toolsets[0]
	require.Nil(t, ts.Provider, "Local toolsets should have nil provider")
}
