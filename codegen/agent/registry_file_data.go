// Package codegen prepares agent registry files before templates render them.
// The generator classifies each toolset once so the template only writes the
// registrations that belong in the final file.
package codegen

type (
	// agentRegistryFileData contains the toolset groups written into one agent's
	// registry file. Import names and generated declarations have already been
	// chosen by the package plan.
	agentRegistryFileData struct {
		*AgentData
		*agentRegistryImports
		// MCPToolsets contains remote toolsets registered during agent startup.
		MCPToolsets []*ToolsetData
		// DirectToolsets contains toolsets supplied by application executors.
		DirectToolsets []*ToolsetData
		// PlanActivity contains the planning activity and its chosen import names.
		PlanActivity *registryActivityData
		// ResumeActivity contains the resume activity and its chosen import names.
		ResumeActivity *registryActivityData
		// ExecuteToolActivity contains the tool activity and its chosen import names.
		ExecuteToolActivity *registryActivityData
	}

	// registryActivityData contains one activity and the import names used by
	// its generated options literal.
	registryActivityData struct {
		*ActivityArtifact
		// EngineAlias names the runtime engine package.
		EngineAlias string
		// TimeAlias names the standard time package.
		TimeAlias string
	}
)

// newAgentRegistryFileData separates remote MCP registrations from direct
// application executors before the registry template is rendered.
func newAgentRegistryFileData(agent *AgentData) *agentRegistryFileData {
	data := &agentRegistryFileData{AgentData: agent}
	if agent.packageFiles != nil {
		data.agentRegistryImports = agent.packageFiles.registry
		data.PlanActivity = newRegistryActivityData(agent.Runtime.PlanActivity, data.agentRegistryImports)
		data.ResumeActivity = newRegistryActivityData(agent.Runtime.ResumeActivity, data.agentRegistryImports)
		data.ExecuteToolActivity = newRegistryActivityData(agent.Runtime.ExecuteTool, data.agentRegistryImports)
	}
	for _, toolset := range agent.AllToolsets {
		if toolset.MCP != nil {
			data.MCPToolsets = append(data.MCPToolsets, toolset)
		}
	}
	for _, toolset := range agent.UsedToolsets {
		if toolset.MCP == nil && toolset.AgentToolsImportPath == "" {
			data.DirectToolsets = append(data.DirectToolsets, toolset)
		}
	}
	return data
}

// newRegistryActivityData attaches the import names required to render one
// activity. A missing activity produces no template data.
func newRegistryActivityData(activity *ActivityArtifact, imports *agentRegistryImports) *registryActivityData {
	if activity == nil {
		return nil
	}
	return &registryActivityData{
		ActivityArtifact: activity,
		EngineAlias:      imports.EngineAlias,
		TimeAlias:        imports.TimeAlias,
	}
}
