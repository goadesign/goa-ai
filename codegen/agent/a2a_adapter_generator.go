package codegen

import (
	"encoding/json"
	"fmt"

	"goa.design/goa-ai/codegen/shared"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

type (
	// a2aAdapterGenerator generates the adapter layer between A2A and agent runtime.
	// It mirrors MCP's adapterGenerator pattern.
	a2aAdapterGenerator struct {
		genpkg string
		agent  *AgentData
		scope  *codegen.NameScope
	}
)

// newA2AAdapterGenerator creates a new A2A adapter generator.
func newA2AAdapterGenerator(genpkg string, agent *AgentData) *a2aAdapterGenerator {
	return &a2aAdapterGenerator{
		genpkg: genpkg,
		agent:  agent,
		scope:  codegen.NewNameScope(),
	}
}

// BuildAdapterData creates the data for the A2A adapter template.
// Security data is passed in to avoid duplicate computation.
func (g *a2aAdapterGenerator) BuildAdapterData(security *A2ASecurityData) *A2AAdapterData {
	return &A2AAdapterData{
		Agent:           g.agent,
		A2AServiceName:  "a2a_" + g.agent.Name,
		A2APackage:      "a2a" + codegen.SnakeCase(g.agent.Name),
		ProtocolVersion: "1.0",
		Skills:          g.BuildSkillData(),
		Security:        security,
	}
}

// BuildSkillData creates skill data from exported tools with computed type references.
func (g *a2aAdapterGenerator) BuildSkillData() []*A2ASkillData {
	skills := make([]*A2ASkillData, 0)

	for _, ts := range g.agent.ExportedToolsets {
		for _, tool := range ts.Tools {
			skill := &A2ASkillData{
				ID:          tool.QualifiedName,
				Name:        tool.Title,
				Description: tool.Description,
				Tags:        tool.Tags,
			}
			if skill.Name == "" {
				skill.Name = tool.Name
			}

			// Compute type references using NameScope helpers
			// ToolData uses Args for payload and Return for result
			if tool.Args.Type != expr.Empty {
				skill.PayloadTypeRef = g.getTypeReference(tool.Args)
				skill.InputSchema = g.toJSONSchema(tool.Args)
				skill.ExampleArgs = g.buildExampleJSON(tool.Args)
			} else {
				skill.ExampleArgs = "{}"
			}

			if tool.Return.Type != expr.Empty {
				skill.ResultTypeRef = g.getTypeReference(tool.Return)
			}

			skills = append(skills, skill)
		}
	}

	return skills
}

// getTypeReference returns a Go type reference using NameScope helpers.
// It handles external user types with locators and composites (arrays, maps) correctly.
func (g *a2aAdapterGenerator) getTypeReference(attr *expr.AttributeExpr) string {
	// External user types should be qualified with their locator package alias.
	if ut, ok := attr.Type.(expr.UserType); ok && ut != nil {
		if loc := codegen.UserTypeLocation(ut); loc != nil && loc.PackageName() != "" {
			return g.scope.GoFullTypeRef(attr, loc.PackageName())
		}
	}

	// For composites and service-local user types, qualify with agent service alias.
	svcAlias := codegen.SnakeCase(g.agent.Service.Name)
	return g.scope.GoFullTypeRef(attr, svcAlias)
}

// toJSONSchema generates a JSON Schema string for the given attribute.
// This delegates to the shared implementation used by both MCP and A2A.
func (g *a2aAdapterGenerator) toJSONSchema(attr *expr.AttributeExpr) string {
	return shared.ToJSONSchema(attr)
}

// buildExampleJSON produces a minimal valid JSON string for the given payload attribute.
func (g *a2aAdapterGenerator) buildExampleJSON(attr *expr.AttributeExpr) string {
	if attr == nil || attr.Type == nil || attr.Type == expr.Empty {
		return "{}"
	}
	// Use Goa's example generator with a deterministic randomizer for stable output
	r := &expr.ExampleGenerator{Randomizer: expr.NewDeterministicRandomizer()}
	v := attr.Example(r)
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// SkillSchemas returns a map of skill ID to JSON Schema for registration.
func (g *a2aAdapterGenerator) SkillSchemas() map[string]string {
	schemas := make(map[string]string)
	for _, ts := range g.agent.ExportedToolsets {
		for _, tool := range ts.Tools {
			// ToolData uses Args for payload
			if tool.Args.Type != expr.Empty {
				schemas[tool.QualifiedName] = g.toJSONSchema(tool.Args)
			} else {
				schemas[tool.QualifiedName] = "{}"
			}
		}
	}
	return schemas
}

// String representation for debugging.
func (g *a2aAdapterGenerator) String() string {
	return fmt.Sprintf("a2aAdapterGenerator{agent: %s, genpkg: %s}", g.agent.Name, g.genpkg)
}
