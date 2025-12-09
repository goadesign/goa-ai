package codegen

import (
	"fmt"
	"path/filepath"

	"goa.design/goa/v3/codegen"
)

type (
	// A2ARegisterData drives generation of static A2A configuration for agents.
	A2ARegisterData struct {
		// Package is the package name for the generated file.
		Package string
		// HelperName is the name prefix for generated functions (e.g., "AssistantToolset").
		HelperName string
		// AgentName is the name of the agent.
		AgentName string
		// AgentGoName is the Go-safe name of the agent.
		AgentGoName string
		// SuiteQualifiedName is the fully qualified toolset name (e.g., "service.agent").
		SuiteQualifiedName string
		// Description is the toolset description.
		Description string
		// Skills contains the skill registration data.
		Skills []A2ARegisterSkill
	}

	// A2ARegisterSkill represents a skill entry in the registration helper.
	A2ARegisterSkill struct {
		// ID is the skill identifier.
		ID string
		// QualifiedName is the fully qualified skill name.
		QualifiedName string
		// Description explains what the skill does.
		Description string
		// PayloadType is the Go type name for the payload.
		PayloadType string
		// ResultType is the Go type name for the result.
		ResultType string
		// InputSchema is the JSON Schema for the skill input.
		InputSchema string
		// ExampleArgs contains a minimal valid JSON for skill arguments.
		ExampleArgs string
	}
)

// a2aRegisterFile generates the static configuration file for an A2A agent.
// It contains the ServerConfig and ProviderConfig variables used at runtime.
func a2aRegisterFile(data *A2AAdapterData) *codegen.File {
	if data == nil || len(data.Skills) == 0 {
		return nil
	}

	// Build registration data from adapter data
	regData := buildA2ARegisterData(data)
	if regData == nil {
		return nil
	}

	agentName := codegen.SnakeCase(data.Agent.Name)
	path := filepath.Join(codegen.Gendir, "a2a_"+agentName, "config.go")

	sections := []*codegen.SectionTemplate{
		{
			Name:   "a2a-config",
			Source: agentsTemplates.Read(a2aConfigFileT),
			Data:   regData,
		},
	}

	return &codegen.File{Path: path, SectionTemplates: sections}
}

// buildA2ARegisterData creates registration data from adapter data.
func buildA2ARegisterData(data *A2AAdapterData) *A2ARegisterData {
	if data == nil || data.Agent == nil || len(data.Skills) == 0 {
		return nil
	}

	agentGoName := codegen.Goify(data.Agent.Name, true)
	desc := data.Agent.Description
	if desc == "" {
		desc = fmt.Sprintf("A2A agent %s", data.Agent.Name)
	}

	regData := &A2ARegisterData{
		Package:            data.A2APackage,
		HelperName:         agentGoName + "Agent",
		AgentName:          data.Agent.Name,
		AgentGoName:        agentGoName,
		SuiteQualifiedName: fmt.Sprintf("%s.%s", data.Agent.Service.Name, data.Agent.Name),
		Description:        desc,
		Skills:             make([]A2ARegisterSkill, 0, len(data.Skills)),
	}

	for _, skill := range data.Skills {
		regData.Skills = append(regData.Skills, A2ARegisterSkill{
			ID:            skill.ID,
			QualifiedName: skill.ID, // Skill ID is already qualified
			Description:   skill.Description,
			PayloadType:   skill.PayloadTypeRef,
			ResultType:    skill.ResultTypeRef,
			InputSchema:   skill.InputSchema,
			ExampleArgs:   skill.ExampleArgs,
		})
	}

	return regData
}
