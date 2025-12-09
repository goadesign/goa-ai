package codegen_test

import (
	"path/filepath"
	"testing"

	"goa.design/goa-ai/codegen/testhelpers"
	. "goa.design/goa-ai/dsl"
	"goa.design/goa-ai/testutil"
	goadsl "goa.design/goa/v3/dsl"
)

// TestRegistryClientGeneratesCorrectMethods verifies that the generated registry
// client contains all required methods: ListToolsets, GetToolset, Search,
// SemanticSearch, Register, Deregister, Heartbeat.
// **Validates: Requirements 1.1**
func TestRegistryClientGeneratesCorrectMethods(t *testing.T) {
	design := func() {
		goadsl.API("client_methods_test", func() {})

		corpRegistry := Registry("corp-registry", func() {
			goadsl.URL("https://registry.corp.internal")
			APIVersion("v1")
			goadsl.Timeout("30s")
			Retry(3, "1s")
			SyncInterval("5m")
			CacheTTL("1h")
		})

		registryTools := Toolset(FromRegistry(corpRegistry, "data-tools"))

		goadsl.Service("client_methods_test", func() {
			Agent("test-agent", "Test agent", func() {
				Use(registryTools)
			})
		})
	}

	files := testhelpers.BuildAndGenerateWithPkg(t, "example.com/client_methods", design)

	clientContent := testhelpers.FileContent(t, files, "gen/client_methods_test/registry/corp_registry/client.go")
	testutil.AssertGo(t, filepath.Join("testdata", "golden", "registry_client", "client.go.golden"), clientContent)
}

// TestRegistryClientAPIKeyAuth verifies that API key authentication is properly
// generated when a registry uses APIKeySecurity.
// **Validates: Requirements 1.2**
func TestRegistryClientAPIKeyAuth(t *testing.T) {
	design := func() {
		goadsl.API("apikey_auth_test", func() {})

		apiKeyScheme := goadsl.APIKeySecurity("corp_api_key", func() {
			goadsl.Description("Corporate registry API key")
		})

		corpRegistry := Registry("corp-registry", func() {
			goadsl.URL("https://registry.corp.internal")
			goadsl.Security(apiKeyScheme)
		})

		registryTools := Toolset(FromRegistry(corpRegistry, "data-tools"))

		goadsl.Service("apikey_auth_test", func() {
			Agent("test-agent", "Test agent", func() {
				Use(registryTools)
			})
		})
	}

	files := testhelpers.BuildAndGenerateWithPkg(t, "example.com/apikey_auth", design)

	optionsContent := testhelpers.FileContent(t, files, "gen/apikey_auth_test/registry/corp_registry/options.go")
	testutil.AssertGo(t, filepath.Join("testdata", "golden", "registry_client_apikey", "options.go.golden"), optionsContent)
}

// TestRegistryClientOAuth2Auth verifies that OAuth2 authentication is properly
// generated when a registry uses OAuth2Security.
// **Validates: Requirements 1.2**
func TestRegistryClientOAuth2Auth(t *testing.T) {
	design := func() {
		goadsl.API("oauth2_auth_test", func() {})

		oauth2Scheme := goadsl.OAuth2Security("anthropic_oauth", func() {
			goadsl.ClientCredentialsFlow(
				"https://auth.anthropic.com/oauth/token",
				"",
			)
			goadsl.Scope("registry:read", "Read access to registry")
		})

		anthropicRegistry := Registry("anthropic-registry", func() {
			goadsl.URL("https://registry.anthropic.com")
			goadsl.Security(oauth2Scheme)
		})

		registryTools := Toolset(FromRegistry(anthropicRegistry, "web-search"))

		goadsl.Service("oauth2_auth_test", func() {
			Agent("test-agent", "Test agent", func() {
				Use(registryTools)
			})
		})
	}

	files := testhelpers.BuildAndGenerateWithPkg(t, "example.com/oauth2_auth", design)

	optionsContent := testhelpers.FileContent(t, files, "gen/oauth2_auth_test/registry/anthropic_registry/options.go")
	testutil.AssertGo(t, filepath.Join("testdata", "golden", "registry_client_oauth2", "options.go.golden"), optionsContent)
}

// TestRegistryClientJWTAuth verifies that JWT authentication is properly
// generated when a registry uses JWTSecurity.
// **Validates: Requirements 1.2**
func TestRegistryClientJWTAuth(t *testing.T) {
	design := func() {
		goadsl.API("jwt_auth_test", func() {})

		jwtScheme := goadsl.JWTSecurity("jwt_auth", func() {
			goadsl.Description("JWT authentication")
		})

		jwtRegistry := Registry("jwt-registry", func() {
			goadsl.URL("https://registry.jwt.internal")
			goadsl.Security(jwtScheme)
		})

		registryTools := Toolset(FromRegistry(jwtRegistry, "secure-tools"))

		goadsl.Service("jwt_auth_test", func() {
			Agent("test-agent", "Test agent", func() {
				Use(registryTools)
			})
		})
	}

	files := testhelpers.BuildAndGenerateWithPkg(t, "example.com/jwt_auth", design)

	optionsContent := testhelpers.FileContent(t, files, "gen/jwt_auth_test/registry/jwt_registry/options.go")
	testutil.AssertGo(t, filepath.Join("testdata", "golden", "registry_client_jwt", "options.go.golden"), optionsContent)
}

// TestRegistryClientBasicAuth verifies that Basic authentication is properly
// generated when a registry uses BasicAuthSecurity.
// **Validates: Requirements 1.2**
func TestRegistryClientBasicAuth(t *testing.T) {
	design := func() {
		goadsl.API("basic_auth_test", func() {})

		basicScheme := goadsl.BasicAuthSecurity("basic_auth", func() {
			goadsl.Description("Basic authentication")
		})

		basicRegistry := Registry("basic-registry", func() {
			goadsl.URL("https://registry.basic.internal")
			goadsl.Security(basicScheme)
		})

		registryTools := Toolset(FromRegistry(basicRegistry, "basic-tools"))

		goadsl.Service("basic_auth_test", func() {
			Agent("test-agent", "Test agent", func() {
				Use(registryTools)
			})
		})
	}

	files := testhelpers.BuildAndGenerateWithPkg(t, "example.com/basic_auth", design)

	optionsContent := testhelpers.FileContent(t, files, "gen/basic_auth_test/registry/basic_registry/options.go")
	testutil.AssertGo(t, filepath.Join("testdata", "golden", "registry_client_basic", "options.go.golden"), optionsContent)
}

// TestRegistryClientOptions verifies that the generated options file contains
// all required option functions.
// **Validates: Requirements 1.1**
func TestRegistryClientOptions(t *testing.T) {
	design := func() {
		goadsl.API("options_test", func() {})

		testRegistry := Registry("test-registry", func() {
			goadsl.URL("https://registry.test.internal")
		})

		registryTools := Toolset(FromRegistry(testRegistry, "test-tools"))

		goadsl.Service("options_test", func() {
			Agent("test-agent", "Test agent", func() {
				Use(registryTools)
			})
		})
	}

	files := testhelpers.BuildAndGenerateWithPkg(t, "example.com/options", design)

	optionsContent := testhelpers.FileContent(t, files, "gen/options_test/registry/test_registry/options.go")
	testutil.AssertGo(t, filepath.Join("testdata", "golden", "registry_client_options", "options.go.golden"), optionsContent)
}

// TestRegistryClientDataTypes verifies that the generated client contains
// all required data types for registry operations.
// **Validates: Requirements 1.1**
func TestRegistryClientDataTypes(t *testing.T) {
	design := func() {
		goadsl.API("types_test", func() {})

		testRegistry := Registry("test-registry", func() {
			goadsl.URL("https://registry.test.internal")
		})

		registryTools := Toolset(FromRegistry(testRegistry, "test-tools"))

		goadsl.Service("types_test", func() {
			Agent("test-agent", "Test agent", func() {
				Use(registryTools)
			})
		})
	}

	files := testhelpers.BuildAndGenerateWithPkg(t, "example.com/types", design)

	clientContent := testhelpers.FileContent(t, files, "gen/types_test/registry/test_registry/client.go")
	testutil.AssertGo(t, filepath.Join("testdata", "golden", "registry_client_types", "client.go.golden"), clientContent)
}
