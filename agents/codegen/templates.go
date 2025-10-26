package codegen

import (
	"embed"

	"goa.design/goa/v3/codegen/template"
)

const (
	agentFileT          = "agent"
	configFileT         = "config"
	workflowFileT       = "workflow"
	activitiesFileT     = "activities"
	registryFileT       = "registry"
	toolTypesFileT      = "tool_types"
	toolCodecsFileT     = "tool_codecs"
	toolSpecFileT       = "tool_spec"
	agentToolsFileT     = "agent_tools"
	serviceToolsetFileT = "service_toolset"
	bootstrapHelperT    = "bootstrap_helper"
	plannerStubT        = "planner_stub"
)

//go:embed templates/*.go.tpl
var templateFS embed.FS

var agentsTemplates = &template.TemplateReader{FS: templateFS}
