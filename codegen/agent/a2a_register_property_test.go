package codegen

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
)

// TestRegistrationCompletenessAllSkillsRegistered verifies Property 11: Registration Completeness.
// **Feature: a2a-codegen-refactor, Property 11: Registration Completeness**
// *For any* agent with exported toolsets, the registration helper should register
// all skills with their complete schemas and metadata.
// **Validates: Requirements 7.2, 7.3**
func TestRegistrationCompletenessAllSkillsRegistered(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("all exported skills are included in registration data", prop.ForAll(
		func(toolNames []string) bool {
			if len(toolNames) == 0 {
				return true // No tools means no registration needed
			}

			// Create agent with exported tools
			agent := createAgentWithTools(toolNames)
			security := &A2ASecurityData{}

			// Build adapter data using the generator
			adapterGen := newA2AAdapterGenerator("example.com/test/gen", agent)
			adapterData := adapterGen.BuildAdapterData(security)

			// Build registration data
			regData := buildA2ARegisterData(adapterData)
			if regData == nil {
				return false // Should have registration data
			}

			// Verify all tools are registered
			if len(regData.Skills) != len(toolNames) {
				return false
			}

			// Verify each tool is present
			registeredSkills := make(map[string]bool)
			for _, skill := range regData.Skills {
				registeredSkills[skill.ID] = true
			}

			for _, name := range toolNames {
				qualifiedName := "test_service.test_agent." + name
				if !registeredSkills[qualifiedName] {
					return false
				}
			}

			return true
		},
		genUniqueToolNames(),
	))

	properties.TestingRun(t)
}

// TestRegistrationCompletenessSchemaIncluded verifies that schemas are included.
// **Feature: a2a-codegen-refactor, Property 11: Registration Completeness**
// **Validates: Requirements 7.3**
func TestRegistrationCompletenessSchemaIncluded(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("registered skills include valid JSON schemas", prop.ForAll(
		func(toolNames []string) bool {
			if len(toolNames) == 0 {
				return true
			}

			agent := createAgentWithToolsAndPayloads(toolNames)
			security := &A2ASecurityData{}

			adapterGen := newA2AAdapterGenerator("example.com/test/gen", agent)
			adapterData := adapterGen.BuildAdapterData(security)
			regData := buildA2ARegisterData(adapterData)

			if regData == nil {
				return false
			}

			// Verify each skill has a valid JSON schema
			for _, skill := range regData.Skills {
				if skill.InputSchema == "" {
					return false
				}
				// Verify it's valid JSON
				var parsed map[string]any
				if err := json.Unmarshal([]byte(skill.InputSchema), &parsed); err != nil {
					return false
				}
			}

			return true
		},
		genUniqueToolNames(),
	))

	properties.TestingRun(t)
}

// TestRegistrationCompletenessExampleArgsIncluded verifies that example args are included.
// **Feature: a2a-codegen-refactor, Property 11: Registration Completeness**
// **Validates: Requirements 7.3**
func TestRegistrationCompletenessExampleArgsIncluded(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("registered skills include valid example arguments", prop.ForAll(
		func(toolNames []string) bool {
			if len(toolNames) == 0 {
				return true
			}

			agent := createAgentWithToolsAndPayloads(toolNames)
			security := &A2ASecurityData{}

			adapterGen := newA2AAdapterGenerator("example.com/test/gen", agent)
			adapterData := adapterGen.BuildAdapterData(security)
			regData := buildA2ARegisterData(adapterData)

			if regData == nil {
				return false
			}

			// Verify each skill has valid example args
			for _, skill := range regData.Skills {
				if skill.ExampleArgs == "" {
					return false
				}
				// Verify it's valid JSON
				var parsed any
				if err := json.Unmarshal([]byte(skill.ExampleArgs), &parsed); err != nil {
					return false
				}
			}

			return true
		},
		genUniqueToolNames(),
	))

	properties.TestingRun(t)
}

// TestRegistrationCompletenessMetadataPreserved verifies that metadata is preserved.
// **Feature: a2a-codegen-refactor, Property 11: Registration Completeness**
// **Validates: Requirements 7.2**
func TestRegistrationCompletenessMetadataPreserved(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("registered skills preserve description metadata", prop.ForAll(
		func(toolNames []string) bool {
			if len(toolNames) == 0 {
				return true
			}

			agent := createAgentWithToolsAndDescriptions(toolNames)
			security := &A2ASecurityData{}

			adapterGen := newA2AAdapterGenerator("example.com/test/gen", agent)
			adapterData := adapterGen.BuildAdapterData(security)
			regData := buildA2ARegisterData(adapterData)

			if regData == nil {
				return false
			}

			// Verify descriptions are preserved
			for i, skill := range regData.Skills {
				expectedDesc := "Description for " + toolNames[i]
				if skill.Description != expectedDesc {
					return false
				}
			}

			return true
		},
		genUniqueToolNames(),
	))

	properties.TestingRun(t)
}

// TestRegistrationFileGeneration verifies that the registration file is generated.
// **Feature: a2a-codegen-refactor, Property 11: Registration Completeness**
// **Validates: Requirements 7.2**
func TestRegistrationFileGeneration(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("registration file is generated for agents with skills", prop.ForAll(
		func(toolNames []string) bool {
			if len(toolNames) == 0 {
				return true
			}

			agent := createAgentWithTools(toolNames)
			security := &A2ASecurityData{}

			adapterGen := newA2AAdapterGenerator("example.com/test/gen", agent)
			adapterData := adapterGen.BuildAdapterData(security)

			regFile := a2aRegisterFile(adapterData)
			if regFile == nil {
				return false
			}

			// Verify file path
			if !strings.Contains(regFile.Path, "register.go") {
				return false
			}

			// Verify sections exist
			if len(regFile.SectionTemplates) == 0 {
				return false
			}

			return true
		},
		genUniqueToolNames(),
	))

	properties.TestingRun(t)
}

// TestRegistrationNoSkillsNoFile verifies no file is generated without skills.
// **Feature: a2a-codegen-refactor, Property 11: Registration Completeness**
// **Validates: Requirements 7.2**
func TestRegistrationNoSkillsNoFile(t *testing.T) {
	agent := &AgentData{
		Name: "empty_agent",
		Service: &service.Data{
			Name: "test_service",
		},
		ExportedToolsets: []*ToolsetData{},
	}
	security := &A2ASecurityData{}

	adapterGen := newA2AAdapterGenerator("example.com/test/gen", agent)
	adapterData := adapterGen.BuildAdapterData(security)

	regFile := a2aRegisterFile(adapterData)
	if regFile != nil {
		t.Error("expected no registration file for agent without skills")
	}
}

// Helper functions

// createAgentWithTools creates an agent with the given tool names.
func createAgentWithTools(toolNames []string) *AgentData {
	tools := make([]*ToolData, 0, len(toolNames))
	for _, name := range toolNames {
		tools = append(tools, &ToolData{
			Name:          name,
			QualifiedName: "test_service.test_agent." + name,
			Description:   "Test tool " + name,
			Args:          &expr.AttributeExpr{Type: expr.Empty},
			Return:        &expr.AttributeExpr{Type: expr.Empty},
		})
	}

	return &AgentData{
		Name: "test_agent",
		Service: &service.Data{
			Name: "test_service",
		},
		ExportedToolsets: []*ToolsetData{
			{
				Name:  "test_toolset",
				Tools: tools,
			},
		},
	}
}

// createAgentWithToolsAndPayloads creates an agent with tools that have payloads.
func createAgentWithToolsAndPayloads(toolNames []string) *AgentData {
	tools := make([]*ToolData, 0, len(toolNames))
	for _, name := range toolNames {
		// Create a simple object payload
		obj := expr.Object{
			&expr.NamedAttributeExpr{
				Name:      "input",
				Attribute: &expr.AttributeExpr{Type: expr.String},
			},
		}
		tools = append(tools, &ToolData{
			Name:          name,
			QualifiedName: "test_service.test_agent." + name,
			Description:   "Test tool " + name,
			Args:          &expr.AttributeExpr{Type: &obj},
			Return:        &expr.AttributeExpr{Type: expr.Empty},
		})
	}

	return &AgentData{
		Name: "test_agent",
		Service: &service.Data{
			Name: "test_service",
		},
		ExportedToolsets: []*ToolsetData{
			{
				Name:  "test_toolset",
				Tools: tools,
			},
		},
	}
}

// createAgentWithToolsAndDescriptions creates an agent with tools that have descriptions.
func createAgentWithToolsAndDescriptions(toolNames []string) *AgentData {
	tools := make([]*ToolData, 0, len(toolNames))
	for _, name := range toolNames {
		tools = append(tools, &ToolData{
			Name:          name,
			QualifiedName: "test_service.test_agent." + name,
			Description:   "Description for " + name,
			Args:          &expr.AttributeExpr{Type: expr.Empty},
			Return:        &expr.AttributeExpr{Type: expr.Empty},
		})
	}

	return &AgentData{
		Name: "test_agent",
		Service: &service.Data{
			Name: "test_service",
		},
		ExportedToolsets: []*ToolsetData{
			{
				Name:  "test_toolset",
				Tools: tools,
			},
		},
	}
}

// genUniqueToolNames generates unique tool names for testing.
func genUniqueToolNames() gopter.Gen {
	return gen.SliceOfN(5, gen.AlphaString()).
		SuchThat(func(names []string) bool {
			seen := make(map[string]bool)
			for _, n := range names {
				if n == "" || seen[n] {
					return false
				}
				seen[n] = true
			}
			return len(names) >= 1
		}).
		Map(func(names []string) []string {
			// Filter out any names that might cause issues
			result := make([]string, 0, len(names))
			for _, n := range names {
				if len(n) > 0 && !strings.ContainsAny(n, " \t\n") {
					result = append(result, codegen.Goify(n, false))
				}
			}
			// Ensure at least one name
			if len(result) == 0 {
				result = append(result, "defaultTool")
			}
			return result
		})
}
