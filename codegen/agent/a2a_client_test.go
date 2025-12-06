package codegen_test

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	codegen "goa.design/goa-ai/codegen/agent"
	. "goa.design/goa-ai/dsl"
	agentsExpr "goa.design/goa-ai/expr/agent"
	goadsl "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// TestA2AClientSendTaskGenerated verifies that the generated A2A client
// contains the SendTask method for task invocation.
// **Validates: Requirements 14.2**
func TestA2AClientSendTaskGenerated(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("a2a_client_test", func() {})

		localTools := Toolset("local_tools", func() {
			Tool("analyze", "Analyze data", func() {
				Args(func() {
					goadsl.Attribute("input", goadsl.String, "Input data")
				})
				Return(func() {
					goadsl.Attribute("result", goadsl.String, "Analysis result")
				})
			})
		})

		goadsl.Service("a2a_client_test", func() {
			Agent("test-agent", "Test agent for A2A client generation", func() {
				Use(localTools)
				Export(localTools)
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := codegen.Generate("example.com/a2a_client", []eval.Root{goaexpr.Root, agentsExpr.Root}, nil)
	require.NoError(t, err)

	var clientContent string
	expectedPath := filepath.ToSlash("gen/a2a_client_test/agents/test_agent/a2a/client.go")
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

	// Verify SendTask method is generated
	require.Contains(t, clientContent, "func (c *A2AClient) SendTask(ctx context.Context, skillID string, input any) (*TaskResponse, error)")
	require.Contains(t, clientContent, `Method:  "tasks/send"`)
	require.Contains(t, clientContent, "json.Marshal(rpcReq)")
	require.Contains(t, clientContent, "c.httpClient.Do(httpReq)")
}

// TestA2AClientStreamingGenerated verifies that the generated A2A client
// contains the SendTaskStreaming method for streaming responses.
// **Validates: Requirements 14.4**
func TestA2AClientStreamingGenerated(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("a2a_streaming_test", func() {})

		tools := Toolset("streaming_tools", func() {
			Tool("process", "Process data", func() {
				Args(func() {
					goadsl.Attribute("data", goadsl.String, "Data to process")
				})
				Return(func() {
					goadsl.Attribute("output", goadsl.String, "Processed output")
				})
			})
		})

		goadsl.Service("a2a_streaming_test", func() {
			Agent("streaming-agent", "Agent for streaming testing", func() {
				Use(tools)
				Export(tools)
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := codegen.Generate("example.com/a2a_streaming", []eval.Root{goaexpr.Root, agentsExpr.Root}, nil)
	require.NoError(t, err)

	var clientContent string
	expectedPath := filepath.ToSlash("gen/a2a_streaming_test/agents/streaming_agent/a2a/client.go")
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

	// Verify SendTaskStreaming method is generated
	require.Contains(t, clientContent, "func (c *A2AClient) SendTaskStreaming(ctx context.Context, skillID string, input any) (<-chan *TaskEvent, error)")
	require.Contains(t, clientContent, `Method:  "tasks/sendSubscribe"`)
	require.Contains(t, clientContent, `Accept", "text/event-stream"`)
	require.Contains(t, clientContent, "go c.streamEvents(ctx, resp, taskID, events)")
	require.Contains(t, clientContent, "func (c *A2AClient) streamEvents(ctx context.Context, resp *http.Response, taskID string, events chan<- *TaskEvent)")
}

// TestA2AClientBearerAuthGenerated verifies that bearer token authentication
// is generated when the agent has JWT security.
// **Validates: Requirements 14.5**
func TestA2AClientBearerAuthGenerated(t *testing.T) {
	testSetup(t)

	design := func() {
		goadsl.API("a2a_bearer_test", func() {})

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

		goadsl.Service("a2a_bearer_test", func() {
			goadsl.Security(jwtScheme)
			Agent("bearer-agent", "Bearer auth agent", func() {
				Use(tools)
				Export(tools)
			})
		})
	}
	files := testGenerate(t, "example.com/a2a_bearer", design)

	clientContent := testFindFileContent(t, files, "gen/a2a_bearer_test/agents/bearer_agent/a2a/client.go")
	require.NotEmpty(t, clientContent, "expected generated client.go")

	// Verify client has auth provider interface
	require.Contains(t, clientContent, "type A2AAuthProvider interface")
	require.Contains(t, clientContent, "ApplyAuth(req *http.Request) error")

	// Verify auth is applied in SendTask
	require.Contains(t, clientContent, "if c.auth != nil")
	require.Contains(t, clientContent, "c.auth.ApplyAuth(httpReq)")
}

// TestA2AClientAPIKeyAuthGenerated verifies that API key authentication
// is generated when the agent has API key security.
// **Validates: Requirements 14.5**
func TestA2AClientAPIKeyAuthGenerated(t *testing.T) {
	testSetup(t)

	design := func() {
		goadsl.API("a2a_apikey_test", func() {})

		apiKeyScheme := goadsl.APIKeySecurity("api_key", func() {
			goadsl.Description("API key authentication")
		})

		tools := Toolset("apikey_tools", func() {
			Tool("action", "Perform action", func() {
				Args(func() {
					goadsl.Attribute("input", goadsl.String, "Input")
				})
			})
		})

		goadsl.Service("a2a_apikey_test", func() {
			goadsl.Security(apiKeyScheme)
			Agent("apikey-agent", "API key agent", func() {
				Use(tools)
				Export(tools)
			})
		})
	}
	files := testGenerate(t, "example.com/a2a_apikey", design)

	clientContent := testFindFileContent(t, files, "gen/a2a_apikey_test/agents/apikey_agent/a2a/client.go")
	require.NotEmpty(t, clientContent, "expected generated client.go")

	// Verify client has auth provider interface
	require.Contains(t, clientContent, "type A2AAuthProvider interface")
	require.Contains(t, clientContent, "ApplyAuth(req *http.Request) error")

	// Verify auth is applied in SendTask
	require.Contains(t, clientContent, "if c.auth != nil")
	require.Contains(t, clientContent, "c.auth.ApplyAuth(httpReq)")
}

// TestA2AClientBasicAuthGenerated verifies that basic authentication
// is generated when the agent has basic auth security.
// **Validates: Requirements 14.5**
func TestA2AClientBasicAuthGenerated(t *testing.T) {
	testSetup(t)

	design := func() {
		goadsl.API("a2a_basic_test", func() {})

		basicScheme := goadsl.BasicAuthSecurity("basic_auth", func() {
			goadsl.Description("Basic authentication")
		})

		tools := Toolset("basic_tools", func() {
			Tool("action", "Perform action", func() {
				Args(func() {
					goadsl.Attribute("input", goadsl.String, "Input")
				})
			})
		})

		goadsl.Service("a2a_basic_test", func() {
			goadsl.Security(basicScheme)
			Agent("basic-agent", "Basic auth agent", func() {
				Use(tools)
				Export(tools)
			})
		})
	}
	files := testGenerate(t, "example.com/a2a_basic", design)

	clientContent := testFindFileContent(t, files, "gen/a2a_basic_test/agents/basic_agent/a2a/client.go")
	require.NotEmpty(t, clientContent, "expected generated client.go")

	// Verify client has auth provider interface
	require.Contains(t, clientContent, "type A2AAuthProvider interface")
	require.Contains(t, clientContent, "ApplyAuth(req *http.Request) error")

	// Verify auth is applied in SendTask
	require.Contains(t, clientContent, "if c.auth != nil")
	require.Contains(t, clientContent, "c.auth.ApplyAuth(httpReq)")
}

// TestA2AClientOAuth2AuthGenerated verifies that OAuth2 authentication
// is generated when the agent has OAuth2 security.
// **Validates: Requirements 14.5**
func TestA2AClientOAuth2AuthGenerated(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("a2a_oauth2_test", func() {})

		oauth2Scheme := goadsl.OAuth2Security("oauth2_auth", func() {
			goadsl.ClientCredentialsFlow(
				"https://auth.example.com/oauth/token",
				"",
			)
			goadsl.Scope("read", "Read access")
		})

		tools := Toolset("oauth2_tools", func() {
			Tool("action", "Perform action", func() {
				Args(func() {
					goadsl.Attribute("input", goadsl.String, "Input")
				})
			})
		})

		goadsl.Service("a2a_oauth2_test", func() {
			goadsl.Security(oauth2Scheme)
			Agent("oauth2-agent", "OAuth2 agent", func() {
				Use(tools)
				Export(tools)
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := codegen.Generate("example.com/a2a_oauth2", []eval.Root{goaexpr.Root, agentsExpr.Root}, nil)
	require.NoError(t, err)

	// Verify client.go is generated
	var clientContent string
	clientPath := filepath.ToSlash("gen/a2a_oauth2_test/agents/oauth2_agent/a2a/client.go")
	for _, f := range files {
		if filepath.ToSlash(f.Path) == clientPath {
			var buf bytes.Buffer
			for _, s := range f.SectionTemplates {
				require.NoError(t, s.Write(&buf))
			}
			clientContent = buf.String()
			break
		}
	}
	require.NotEmpty(t, clientContent, "expected generated client.go at %s", clientPath)

	// Verify client has auth provider interface
	require.Contains(t, clientContent, "type A2AAuthProvider interface")
	require.Contains(t, clientContent, "ApplyAuth(req *http.Request) error")

	// Verify auth is applied in SendTask
	require.Contains(t, clientContent, "if c.auth != nil")
	require.Contains(t, clientContent, "c.auth.ApplyAuth(httpReq)")
}

// TestA2AClientNoExportsNoGeneration verifies that no A2A client is generated
// when the agent has no exported toolsets.
// **Validates: Requirements 14.2**
func TestA2AClientNoExportsNoGeneration(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("no_export_client_test", func() {})

		tools := Toolset("internal_tools", func() {
			Tool("internal_action", "Internal action", func() {
				Args(func() {
					goadsl.Attribute("input", goadsl.String, "Input")
				})
			})
		})

		goadsl.Service("no_export_client_test", func() {
			Agent("no-export-agent", "Agent without exports", func() {
				Use(tools)
				// No Export() - agent only consumes tools
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := codegen.Generate("example.com/no_export_client", []eval.Root{goaexpr.Root, agentsExpr.Root}, nil)
	require.NoError(t, err)

	// Verify no A2A client files are generated
	clientPath := filepath.ToSlash("gen/no_export_client_test/agents/no_export_agent/a2a/client.go")

	for _, f := range files {
		p := filepath.ToSlash(f.Path)
		require.NotEqual(t, clientPath, p, "client.go should not be generated for agent without exports")
	}
}

// TestA2AClientOptionsGenerated verifies that the generated A2A client
// contains all configuration options.
// **Validates: Requirements 14.2**
func TestA2AClientOptionsGenerated(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("a2a_options_test", func() {})

		tools := Toolset("options_tools", func() {
			Tool("action", "Perform action", func() {
				Args(func() {
					goadsl.Attribute("input", goadsl.String, "Input")
				})
			})
		})

		goadsl.Service("a2a_options_test", func() {
			Agent("options-agent", "Agent for options testing", func() {
				Use(tools)
				Export(tools)
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := codegen.Generate("example.com/a2a_options", []eval.Root{goaexpr.Root, agentsExpr.Root}, nil)
	require.NoError(t, err)

	var clientContent string
	expectedPath := filepath.ToSlash("gen/a2a_options_test/agents/options_agent/a2a/client.go")
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

	// Verify client struct is generated
	require.Contains(t, clientContent, "type A2AClient struct")
	require.Contains(t, clientContent, "endpoint string")
	require.Contains(t, clientContent, "httpClient *http.Client")
	require.Contains(t, clientContent, "auth A2AAuthProvider")
	require.Contains(t, clientContent, "timeout time.Duration")

	// Verify option type and functions are generated
	require.Contains(t, clientContent, "type A2AClientOption func(*A2AClient)")
	require.Contains(t, clientContent, "func NewA2AClient(card *AgentCard, opts ...A2AClientOption) *A2AClient")
	require.Contains(t, clientContent, "func WithA2AHTTPClient(client *http.Client) A2AClientOption")
	require.Contains(t, clientContent, "func WithA2AAuth(auth A2AAuthProvider) A2AClientOption")
	require.Contains(t, clientContent, "func WithA2ATimeout(timeout time.Duration) A2AClientOption")
	require.Contains(t, clientContent, "func WithA2AEndpoint(endpoint string) A2AClientOption")

	// Verify auth provider interface is generated
	require.Contains(t, clientContent, "type A2AAuthProvider interface")
	require.Contains(t, clientContent, "ApplyAuth(req *http.Request) error")
}

// TestA2AClientJSONRPCTypesGenerated verifies that the generated A2A client
// contains JSON-RPC request/response types.
// **Validates: Requirements 14.2**
func TestA2AClientJSONRPCTypesGenerated(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("a2a_jsonrpc_types_test", func() {})

		tools := Toolset("jsonrpc_tools", func() {
			Tool("action", "Perform action", func() {
				Args(func() {
					goadsl.Attribute("input", goadsl.String, "Input")
				})
			})
		})

		goadsl.Service("a2a_jsonrpc_types_test", func() {
			Agent("jsonrpc-agent", "Agent for JSON-RPC types testing", func() {
				Use(tools)
				Export(tools)
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := codegen.Generate("example.com/a2a_jsonrpc_types", []eval.Root{goaexpr.Root, agentsExpr.Root}, nil)
	require.NoError(t, err)

	var clientContent string
	expectedPath := filepath.ToSlash("gen/a2a_jsonrpc_types_test/agents/jsonrpc_agent/a2a/client.go")
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

	// Verify JSON-RPC types are generated
	require.Contains(t, clientContent, "jsonRPCRequest struct")
	require.Contains(t, clientContent, `JSONRPC string`)
	require.Contains(t, clientContent, `Method  string`)
	require.Contains(t, clientContent, `Params  any`)
	require.Contains(t, clientContent, `ID      string`)

	require.Contains(t, clientContent, "jsonRPCResponse struct")
	require.Contains(t, clientContent, `Result  json.RawMessage`)
	require.Contains(t, clientContent, `Error   *A2AError`)
}
