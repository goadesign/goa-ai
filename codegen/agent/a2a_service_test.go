package codegen_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/testhelpers"
	. "goa.design/goa-ai/dsl"
	"goa.design/goa-ai/testutil"
	goadsl "goa.design/goa/v3/dsl"
)

// TestA2AServiceGeneration verifies that the A2A service is generated correctly
// for agents with exported toolsets.
// **Validates: Requirements 13.4**
func TestA2AServiceGeneration(t *testing.T) {
	design := func() {
		goadsl.API("a2a_service_test", func() {})

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

		goadsl.Service("a2a_service_test", func() {
			Agent("test-agent", "Test agent for A2A service generation", func() {
				Use(localTools)
				Export(localTools)
			})
		})
	}

	files := testhelpers.BuildAndGenerateWithPkg(t, "example.com/a2a_service", design)

	// Test service.go generation
	serviceContent := testhelpers.FileContent(t, files, "gen/a2a_test_agent/service.go")
	testutil.AssertGo(t, filepath.Join("testdata", "golden", "a2a_service", "service.go.golden"), serviceContent)

	// Test endpoints.go generation
	endpointsContent := testhelpers.FileContent(t, files, "gen/a2a_test_agent/endpoints.go")
	testutil.AssertGo(t, filepath.Join("testdata", "golden", "a2a_service", "endpoints.go.golden"), endpointsContent)

	// Test protocol_version.go generation
	versionContent := testhelpers.FileContent(t, files, "gen/a2a_test_agent/protocol_version.go")
	testutil.AssertGo(t, filepath.Join("testdata", "golden", "a2a_service", "protocol_version.go.golden"), versionContent)
}

// TestA2AServiceAdapterGeneration verifies that the A2A adapter is generated
// with the correct structure for routing to agent runtime.
// **Validates: Requirements 10.2, 13.4**
func TestA2AServiceAdapterGeneration(t *testing.T) {
	design := func() {
		goadsl.API("a2a_adapter_test", func() {})

		tools := Toolset("action_tools", func() {
			Tool("process", "Process data", func() {
				Args(func() {
					goadsl.Attribute("data", goadsl.String, "Data to process")
				})
				Return(func() {
					goadsl.Attribute("output", goadsl.String, "Processed output")
				})
			})
		})

		goadsl.Service("a2a_adapter_test", func() {
			Agent("adapter-agent", "Agent for adapter testing", func() {
				Use(tools)
				Export(tools)
			})
		})
	}

	files := testhelpers.BuildAndGenerateWithPkg(t, "example.com/a2a_adapter", design)

	// Verify the adapter file is generated at the expected path
	expectedPath := filepath.ToSlash("gen/a2a_adapter_agent/adapter_server.go")
	adapterFile := testhelpers.FindFile(files, expectedPath)
	require.NotNil(t, adapterFile, "expected generated adapter_server.go at %s", expectedPath)

	// Verify the adapter file has the expected section templates
	sectionNames := make([]string, 0, len(adapterFile.SectionTemplates))
	for _, s := range adapterFile.SectionTemplates {
		sectionNames = append(sectionNames, s.Name)
	}

	// Verify required sections are present
	require.Contains(t, sectionNames, "source-header", "adapter should have header section")
	require.Contains(t, sectionNames, "a2a-adapter-core", "adapter should have core section")
	require.Contains(t, sectionNames, "a2a-adapter-tasks", "adapter should have tasks section")
	require.Contains(t, sectionNames, "a2a-adapter-card", "adapter should have card section")

	// Golden file test for adapter content
	adapterContent := testhelpers.FileContent(t, files, expectedPath)
	testutil.AssertGo(t, filepath.Join("testdata", "golden", "a2a_adapter", "adapter_server.go.golden"), adapterContent)
}

// TestA2AServiceNoExportsNoGeneration verifies that no A2A service is generated
// when the agent has no exported toolsets.
// **Validates: Requirements 13.4**
func TestA2AServiceNoExportsNoGeneration(t *testing.T) {
	design := func() {
		goadsl.API("no_export_a2a_test", func() {})

		tools := Toolset("internal_tools", func() {
			Tool("internal_action", "Internal action", func() {
				Args(func() {
					goadsl.Attribute("input", goadsl.String, "Input")
				})
			})
		})

		goadsl.Service("no_export_a2a_test", func() {
			Agent("no-export-agent", "Agent without exports", func() {
				Use(tools)
				// No Export() - agent only consumes tools
			})
		})
	}

	files := testhelpers.BuildAndGenerateWithPkg(t, "example.com/no_export_a2a", design)

	// Verify no A2A service files are generated
	servicePath := "gen/a2a_no_export_agent/service.go"
	adapterPath := "gen/a2a_no_export_agent/adapter_server.go"

	require.False(t, testhelpers.FileExists(files, servicePath), "service.go should not be generated for agent without exports")
	require.False(t, testhelpers.FileExists(files, adapterPath), "adapter_server.go should not be generated for agent without exports")
}

// TestA2AServiceJSONRPCTransportGeneration verifies that JSON-RPC transport
// files are generated for the A2A service.
// **Validates: Requirements 13.4**
func TestA2AServiceJSONRPCTransportGeneration(t *testing.T) {
	design := func() {
		goadsl.API("a2a_jsonrpc_test", func() {})

		tools := Toolset("jsonrpc_tools", func() {
			Tool("execute", "Execute command", func() {
				Args(func() {
					goadsl.Attribute("cmd", goadsl.String, "Command")
				})
				Return(func() {
					goadsl.Attribute("result", goadsl.String, "Result")
				})
			})
		})

		goadsl.Service("a2a_jsonrpc_test", func() {
			Agent("jsonrpc-agent", "Agent for JSON-RPC testing", func() {
				Use(tools)
				Export(tools)
			})
		})
	}

	files := testhelpers.BuildAndGenerateWithPkg(t, "example.com/a2a_jsonrpc", design)

	// Verify JSON-RPC server files are generated
	serverPath := "gen/jsonrpc/a2a_jsonrpc_agent/server/server.go"
	require.True(t, testhelpers.FileExists(files, serverPath), "JSON-RPC server.go should be generated at %s", serverPath)

	// Verify JSON-RPC client files are generated
	clientPath := "gen/jsonrpc/a2a_jsonrpc_agent/client/client.go"
	require.True(t, testhelpers.FileExists(files, clientPath), "JSON-RPC client.go should be generated at %s", clientPath)
}

// TestA2AServiceMultipleTools verifies that A2A service generation works
// correctly with multiple tools in the exported toolset.
// **Validates: Requirements 13.4**
func TestA2AServiceMultipleTools(t *testing.T) {
	design := func() {
		goadsl.API("a2a_multi_tools_test", func() {})

		tools := Toolset("multi_tools", func() {
			Tool("query", "Query data", func() {
				Args(func() {
					goadsl.Attribute("q", goadsl.String, "Query string")
				})
				Return(func() {
					goadsl.Attribute("results", goadsl.ArrayOf(goadsl.String), "Results")
				})
			})
			Tool("transform", "Transform data", func() {
				Args(func() {
					goadsl.Attribute("input", goadsl.String, "Input data")
				})
				Return(func() {
					goadsl.Attribute("output", goadsl.String, "Output data")
				})
			})
			Tool("validate", "Validate data", func() {
				Args(func() {
					goadsl.Attribute("data", goadsl.String, "Data to validate")
				})
				Return(func() {
					goadsl.Attribute("valid", goadsl.Boolean, "Validation result")
				})
			})
		})

		goadsl.Service("a2a_multi_tools_test", func() {
			Agent("multi-tools-agent", "Agent with multiple tools", func() {
				Use(tools)
				Export(tools)
			})
		})
	}

	files := testhelpers.BuildAndGenerateWithPkg(t, "example.com/a2a_multi_tools", design)

	// Test service.go generation with multiple tools
	serviceContent := testhelpers.FileContent(t, files, "gen/a2a_multi_tools_agent/service.go")
	testutil.AssertGo(t, filepath.Join("testdata", "golden", "a2a_multi_tools", "service.go.golden"), serviceContent)
}
