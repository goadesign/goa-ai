package codegen

import (
	"reflect"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"goa.design/goa/v3/codegen/service"
)

// TestToolToSkillMappingCompletenessProperty verifies Property 5: Tool-to-Skill Mapping Completeness.
// **Feature: mcp-registry, Property 5: Tool-to-Skill Mapping Completeness**
// *For any* exported toolset, every tool SHALL be mapped to an A2A skill with
// matching name and description.
// **Validates: Requirements 13.2**
func TestToolToSkillMappingCompletenessProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("every exported tool is mapped to a skill", prop.ForAll(
		func(toolsets []*ToolsetData) bool {
			// Build agent data with the generated toolsets
			agent := &AgentData{
				Name:   "test_agent",
				GoName: "TestAgent",
				Service: &service.Data{
					Name:    "TestService",
					Schemes: []*service.SchemeData{},
				},
				ExportedToolsets: toolsets,
			}

			// Build A2A card data
			cardData := buildA2ACardData(agent, nil)

			// Count total tools across all exported toolsets
			totalTools := 0
			for _, ts := range toolsets {
				totalTools += len(ts.Tools)
			}

			// Property 1: Number of skills must equal number of exported tools
			if len(cardData.Skills) != totalTools {
				return false
			}

			// Property 2: Every tool must have a corresponding skill
			for _, ts := range toolsets {
				for _, tool := range ts.Tools {
					found := false
					for _, skill := range cardData.Skills {
						if skill.ID == tool.QualifiedName {
							found = true

							// Property 3: Skill name must match tool title (or name as fallback)
							expectedName := tool.Title
							if expectedName == "" {
								expectedName = tool.Name
							}
							if skill.Name != expectedName {
								return false
							}

							// Property 4: Skill description must match tool description
							if skill.Description != tool.Description {
								return false
							}

							// Property 5: Skill tags must match tool tags
							if !tagsEqual(skill.Tags, tool.Tags) {
								return false
							}

							break
						}
					}
					if !found {
						return false
					}
				}
			}

			return true
		},
		genExportedToolsets(),
	))

	properties.TestingRun(t)
}

// TestToolToSkillMappingPreservesID verifies that skill IDs match tool qualified names.
// **Feature: mcp-registry, Property 5: Tool-to-Skill Mapping Completeness**
// **Validates: Requirements 13.2**
func TestToolToSkillMappingPreservesID(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("skill ID equals tool qualified name", prop.ForAll(
		func(toolsetName, toolName string) bool {
			qualifiedName := toolsetName + "." + toolName
			tool := &ToolData{
				Name:          toolName,
				QualifiedName: qualifiedName,
				Title:         toolName,
				Description:   "Test tool",
			}
			toolset := &ToolsetData{
				Name:  toolsetName,
				Tools: []*ToolData{tool},
			}

			agent := &AgentData{
				Name:   "test_agent",
				GoName: "TestAgent",
				Service: &service.Data{
					Name:    "TestService",
					Schemes: []*service.SchemeData{},
				},
				ExportedToolsets: []*ToolsetData{toolset},
			}

			cardData := buildA2ACardData(agent, nil)

			// Property: skill ID must equal tool qualified name
			if len(cardData.Skills) != 1 {
				return false
			}
			return cardData.Skills[0].ID == qualifiedName
		},
		genToolsetNameForSkill(),
		genToolNameForSkill(),
	))

	properties.TestingRun(t)
}

// TestToolToSkillMappingTitleFallback verifies that skill name falls back to tool name when title is empty.
// **Feature: mcp-registry, Property 5: Tool-to-Skill Mapping Completeness**
// **Validates: Requirements 13.2**
func TestToolToSkillMappingTitleFallback(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("skill name falls back to tool name when title is empty", prop.ForAll(
		func(toolName string) bool {
			tool := &ToolData{
				Name:          toolName,
				QualifiedName: "toolset." + toolName,
				Title:         "", // Empty title
				Description:   "Test tool",
			}
			toolset := &ToolsetData{
				Name:  "toolset",
				Tools: []*ToolData{tool},
			}

			agent := &AgentData{
				Name:   "test_agent",
				GoName: "TestAgent",
				Service: &service.Data{
					Name:    "TestService",
					Schemes: []*service.SchemeData{},
				},
				ExportedToolsets: []*ToolsetData{toolset},
			}

			cardData := buildA2ACardData(agent, nil)

			// Property: skill name must equal tool name when title is empty
			if len(cardData.Skills) != 1 {
				return false
			}
			return cardData.Skills[0].Name == toolName
		},
		genToolNameForSkill(),
	))

	properties.TestingRun(t)
}

// TestToolToSkillMappingPreservesTitle verifies that skill name uses tool title when present.
// **Feature: mcp-registry, Property 5: Tool-to-Skill Mapping Completeness**
// **Validates: Requirements 13.2**
func TestToolToSkillMappingPreservesTitle(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("skill name uses tool title when present", prop.ForAll(
		func(toolName, toolTitle string) bool {
			tool := &ToolData{
				Name:          toolName,
				QualifiedName: "toolset." + toolName,
				Title:         toolTitle,
				Description:   "Test tool",
			}
			toolset := &ToolsetData{
				Name:  "toolset",
				Tools: []*ToolData{tool},
			}

			agent := &AgentData{
				Name:   "test_agent",
				GoName: "TestAgent",
				Service: &service.Data{
					Name:    "TestService",
					Schemes: []*service.SchemeData{},
				},
				ExportedToolsets: []*ToolsetData{toolset},
			}

			cardData := buildA2ACardData(agent, nil)

			// Property: skill name must equal tool title when title is non-empty
			if len(cardData.Skills) != 1 {
				return false
			}
			return cardData.Skills[0].Name == toolTitle
		},
		genToolNameForSkill(),
		genToolTitleForSkill(),
	))

	properties.TestingRun(t)
}

// TestToolToSkillMappingPreservesTags verifies that skill tags match tool tags.
// **Feature: mcp-registry, Property 5: Tool-to-Skill Mapping Completeness**
// **Validates: Requirements 13.2**
func TestToolToSkillMappingPreservesTags(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("skill tags match tool tags", prop.ForAll(
		func(tags []string) bool {
			tool := &ToolData{
				Name:          "test_tool",
				QualifiedName: "toolset.test_tool",
				Title:         "Test Tool",
				Description:   "Test tool",
				Tags:          tags,
			}
			toolset := &ToolsetData{
				Name:  "toolset",
				Tools: []*ToolData{tool},
			}

			agent := &AgentData{
				Name:   "test_agent",
				GoName: "TestAgent",
				Service: &service.Data{
					Name:    "TestService",
					Schemes: []*service.SchemeData{},
				},
				ExportedToolsets: []*ToolsetData{toolset},
			}

			cardData := buildA2ACardData(agent, nil)

			// Property: skill tags must equal tool tags
			if len(cardData.Skills) != 1 {
				return false
			}
			return tagsEqual(cardData.Skills[0].Tags, tags)
		},
		genTagsForSkill(),
	))

	properties.TestingRun(t)
}

// TestToolToSkillMappingMultipleToolsets verifies mapping across multiple toolsets.
// **Feature: mcp-registry, Property 5: Tool-to-Skill Mapping Completeness**
// **Validates: Requirements 13.2**
func TestToolToSkillMappingMultipleToolsets(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("all tools from multiple toolsets are mapped", prop.ForAll(
		func(numToolsets, toolsPerToolset int) bool {
			toolsets := make([]*ToolsetData, numToolsets)
			expectedTools := make(map[string]*ToolData)

			for i := range numToolsets {
				toolsetName := genToolsetNames()[i%len(genToolsetNames())]
				tools := make([]*ToolData, toolsPerToolset)
				for j := range toolsPerToolset {
					toolName := genToolNames()[j%len(genToolNames())]
					qualifiedName := toolsetName + "." + toolName
					tool := &ToolData{
						Name:          toolName,
						QualifiedName: qualifiedName,
						Title:         toolName,
						Description:   "Test tool",
					}
					tools[j] = tool
					expectedTools[qualifiedName] = tool
				}
				toolsets[i] = &ToolsetData{
					Name:  toolsetName,
					Tools: tools,
				}
			}

			agent := &AgentData{
				Name:   "test_agent",
				GoName: "TestAgent",
				Service: &service.Data{
					Name:    "TestService",
					Schemes: []*service.SchemeData{},
				},
				ExportedToolsets: toolsets,
			}

			cardData := buildA2ACardData(agent, nil)

			// Property: number of skills must equal total number of tools
			if len(cardData.Skills) != len(expectedTools) {
				return false
			}

			// Property: every expected tool must have a corresponding skill
			for qualifiedName, tool := range expectedTools {
				found := false
				for _, skill := range cardData.Skills {
					if skill.ID == qualifiedName {
						found = true
						if skill.Name != tool.Title && skill.Name != tool.Name {
							return false
						}
						break
					}
				}
				if !found {
					return false
				}
			}

			return true
		},
		gen.IntRange(1, 3),
		gen.IntRange(1, 4),
	))

	properties.TestingRun(t)
}

// TestToolToSkillMappingEmptyToolsets verifies that empty toolsets produce no skills.
// **Feature: mcp-registry, Property 5: Tool-to-Skill Mapping Completeness**
// **Validates: Requirements 13.2**
func TestToolToSkillMappingEmptyToolsets(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("empty toolsets produce no skills", prop.ForAll(
		func(numToolsets int) bool {
			toolsets := make([]*ToolsetData, numToolsets)
			for i := range numToolsets {
				toolsets[i] = &ToolsetData{
					Name:  genToolsetNames()[i%len(genToolsetNames())],
					Tools: []*ToolData{}, // Empty tools
				}
			}

			agent := &AgentData{
				Name:   "test_agent",
				GoName: "TestAgent",
				Service: &service.Data{
					Name:    "TestService",
					Schemes: []*service.SchemeData{},
				},
				ExportedToolsets: toolsets,
			}

			cardData := buildA2ACardData(agent, nil)

			// Property: no skills should be generated for empty toolsets
			return len(cardData.Skills) == 0
		},
		gen.IntRange(0, 5),
	))

	properties.TestingRun(t)
}

// Helper functions

// tagsEqual compares two tag slices for equality.
func tagsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i, v := range a {
		if v != b[i] {
			return false
		}
	}
	return true
}

// Generators

// genExportedToolsets generates a slice of ToolsetData with tools for property testing.
func genExportedToolsets() gopter.Gen {
	return gen.IntRange(0, 3).FlatMap(func(numToolsets any) gopter.Gen {
		n := numToolsets.(int)
		if n == 0 {
			return gen.Const([]*ToolsetData{})
		}
		return gen.IntRange(1, 4).Map(func(toolsPerToolset int) []*ToolsetData {
			toolsets := make([]*ToolsetData, n)
			for i := range n {
				toolsetName := genToolsetNames()[i%len(genToolsetNames())]
				tools := make([]*ToolData, toolsPerToolset)
				for j := range toolsPerToolset {
					toolName := genToolNames()[j%len(genToolNames())]
					tools[j] = &ToolData{
						Name:          toolName,
						QualifiedName: toolsetName + "." + toolName,
						Title:         genToolTitles()[j%len(genToolTitles())],
						Description:   genToolDescriptions()[j%len(genToolDescriptions())],
						Tags:          genTagSets()[j%len(genTagSets())],
					}
				}
				toolsets[i] = &ToolsetData{
					Name:  toolsetName,
					Tools: tools,
				}
			}
			return toolsets
		})
	}, reflect.TypeOf([]*ToolsetData{}))
}

// genToolsetNameForSkill generates valid toolset names for skill mapping tests.
func genToolsetNameForSkill() gopter.Gen {
	return gen.OneConstOf(
		"data_tools", "search_tools", "admin_tools",
		"analytics", "utilities", "core_tools",
	)
}

// genToolNameForSkill generates valid tool names for skill mapping tests.
func genToolNameForSkill() gopter.Gen {
	return gen.OneConstOf(
		"analyze", "search", "query", "process", "transform",
		"get_data", "set_config", "run_task", "fetch_results",
	)
}

// genToolTitleForSkill generates valid tool titles for skill mapping tests.
func genToolTitleForSkill() gopter.Gen {
	return gen.OneConstOf(
		"Analyze Data", "Search Items", "Query Database",
		"Process Input", "Transform Format",
	)
}

// genTagsForSkill generates valid tag slices for skill mapping tests.
func genTagsForSkill() gopter.Gen {
	return gen.OneConstOf(
		[]string{},
		[]string{"data"},
		[]string{"data", "analytics"},
		[]string{"admin", "config", "system"},
		[]string{"search", "query"},
	)
}

// genToolsetNames returns a list of valid toolset names.
func genToolsetNames() []string {
	return []string{
		"data_tools", "search_tools", "admin_tools",
		"analytics", "utilities", "core_tools",
	}
}

// genToolNames returns a list of valid tool names.
func genToolNames() []string {
	return []string{
		"analyze", "search", "query", "process", "transform",
		"get_data", "set_config", "run_task", "fetch_results",
	}
}

// genToolTitles returns a list of valid tool titles.
func genToolTitles() []string {
	return []string{
		"Analyze Data", "Search Items", "Query Database",
		"Process Input", "Transform Format",
	}
}

// genToolDescriptions returns a list of valid tool descriptions.
func genToolDescriptions() []string {
	return []string{
		"Analyzes data for insights",
		"Searches for matching items",
		"Queries the database",
		"Processes input data",
		"Transforms data format",
	}
}

// genTagSets returns a list of valid tag sets.
func genTagSets() [][]string {
	return [][]string{
		{},
		{"data"},
		{"data", "analytics"},
		{"admin", "config", "system"},
		{"search", "query"},
	}
}
