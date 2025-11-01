package codegen

import (
	"embed"

	"goa.design/goa/v3/codegen/template"
)

const (
	activitiesFileT      = "activities"
	agentFileT           = "agent"
	agentToolsFileT      = "agent_tools"
	bootstrapHelperT     = "bootstrap_helper"
	bootstrapInternalT   = "bootstrap_internal"
	exampleExecutorStubT = "example_executor_stub"
	configFileT          = "config"
	exampleAdapterStubT  = "example_adapter_stub"
	plannerInternalStubT = "planner_internal_stub"
	plannerStubT         = "planner_stub"
	quickstartReadmeT    = "agents_quickstart"
	registryFileT        = "registry"
	serviceToolsetFileT  = "service_toolset"
	mcpExecutorFileT     = "mcp_executor"
	toolCodecsFileT      = "tool_codecs"
	toolSpecFileT        = "tool_spec"
	toolSpecsAggregateT  = "specs_aggregate"
	toolTransformsFileT  = "tool_transforms"
	toolTypesFileT       = "tool_types"
	workflowFileT        = "workflow"
)

//go:embed templates/*.go.tpl
var templateFS embed.FS

var agentsTemplates = &template.TemplateReader{FS: templateFS}
