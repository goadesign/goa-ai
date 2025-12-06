package codegen

import (
	"embed"

	"goa.design/goa/v3/codegen/template"
)

const (
	a2aAdapterCoreFileT        = "a2a_adapter_core"
	a2aAdapterTasksFileT       = "a2a_adapter_tasks"
	a2aAdapterCardFileT        = "a2a_adapter_card"
	a2aAuthFileT               = "a2a_auth"
	a2aCardFileT               = "a2a_card"
	a2aClientFileT             = "a2a_client"
	a2aRegisterFileT           = "a2a_register"
	a2aTypesFileT              = "a2a_types"
	agentFileT                 = "agent"
	agentToolsFileT            = "agent_tools"
	agentToolsConsumerT        = "agent_tools_consumer"
	bootstrapInternalT         = "bootstrap_internal"
	exampleExecutorStubT       = "example_executor_stub"
	configFileT                = "config"
	plannerInternalStubT       = "planner_internal_stub"
	quickstartReadmeT          = "agents_quickstart"
	registryFileT              = "registry"
	registryClientFileT        = "registry_client"
	registryClientOptionsFileT = "registry_client_options"
	registryToolsetSpecsFileT  = "registry_toolset_specs"
	mcpExecutorFileT           = "mcp_executor"
	serviceExecutorFileT       = "service_executor"
	serviceCodecsFileT         = "service_codecs"
	toolCodecsFileT            = "tool_codecs"
	toolSpecFileT              = "tool_spec"
	toolSpecsAggregateT        = "specs_aggregate"
	toolTransformsFileT        = "tool_transforms"
	toolTypesFileT             = "tool_types"
	usedToolsFileT             = "used_tools"
)

//go:embed templates/*.go.tpl
var templateFS embed.FS

var agentsTemplates = &template.TemplateReader{FS: templateFS}
