package codegen

import (
	"embed"

	"goa.design/goa/v3/codegen/template"
)

const (
	agentFileT           = "agent"
	agentToolsFileT      = "agent_tools"
	agentToolsConsumerT  = "agent_tools_consumer"
	bootstrapInternalT   = "bootstrap_internal"
	exampleExecutorStubT = "example_executor_stub"
	configFileT          = "config"
	plannerInternalStubT = "planner_internal_stub"
	quickstartReadmeT    = "agents_quickstart"
	registryFileT        = "registry"
	mcpExecutorFileT     = "mcp_executor"
	serviceExecutorFileT = "service_executor"
	serviceCodecsFileT   = "service_codecs"
	toolCodecsFileT      = "tool_codecs"
	toolSpecFileT        = "tool_spec"
	toolSpecsAggregateT  = "specs_aggregate"
	toolTransformsFileT  = "tool_transforms"
	toolTypesFileT       = "tool_types"
	usedToolsFileT       = "used_tools"
)

//go:embed templates/*.go.tpl
var templateFS embed.FS

var agentsTemplates = &template.TemplateReader{FS: templateFS}
