package codegen

import (
	"encoding/json"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/prop"
	agentsExpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	goaexpr "goa.design/goa/v3/expr"
)

// TestA2AConsumerProviderConfigCompleteness verifies Property 19:
// Static ProviderConfig completeness for FromA2A consumers.
//
// **Feature: a2a-architecture-redesign, Property 19: Static ProviderConfig Completeness**
// *For any* FromA2A consumer with available provider design, the generated
// consumer configuration data should contain a suite and skills with input
// schemas and example payloads suitable for building TypeSpec entries.
// **Validates: Requirements 9.4**
func TestA2AConsumerProviderConfigCompleteness(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("consumer config contains suite and skills with schema and example", prop.ForAll(
		func(toolNames []string) bool {
			if len(toolNames) == 0 {
				return true
			}

			const (
				genpkg     = "example.com/a2a_consumer/gen"
				serviceName = "a2a_consumer_test"
				suite       = "remote.service.agent.tools"
			)

			svcData := &service.Data{
				Name:     serviceName,
				PathName: codegen.SnakeCase(serviceName),
			}

			tools := make([]*ToolData, 0, len(toolNames))
			for _, name := range toolNames {
				obj := &goaexpr.Object{
					&goaexpr.NamedAttributeExpr{
						Name: "input",
						Attribute: &goaexpr.AttributeExpr{
							Type: goaexpr.String,
						},
					},
				}
				tools = append(tools, &ToolData{
					Name:          name,
					QualifiedName: "remote_tools." + name,
					Description:   "Test tool " + name,
					Args:          &goaexpr.AttributeExpr{Type: obj},
					Return:        &goaexpr.AttributeExpr{Type: goaexpr.Empty},
				})
			}

			provider := &agentsExpr.ProviderExpr{
				Kind:     agentsExpr.ProviderA2A,
				A2ASuite: suite,
				A2AURL:   "https://provider.example.com",
			}
			tsExpr := &agentsExpr.ToolsetExpr{
				Name:     "remote_tools",
				Provider: provider,
			}

			usedToolset := &ToolsetData{
				Expr:  tsExpr,
				Name:  "remote_tools",
				Tools: tools,
			}

			agent := &AgentData{
				Genpkg:       genpkg,
				Name:         "consumer_agent",
				Service:      svcData,
				PackageName:  codegen.SnakeCase("consumer_agent"),
				UsedToolsets: []*ToolsetData{usedToolset},
			}

			svcAgents := &ServiceAgentsData{
				Service: svcData,
				Agents:  []*AgentData{agent},
			}

			pkgs := buildA2AConsumerConfigData(genpkg, svcAgents)
			if len(pkgs) != 1 {
				return false
			}
			cfg := pkgs[0]
			if cfg.Suite != suite {
				return false
			}
			if len(cfg.Skills) != len(toolNames) {
				return false
			}

			for _, sk := range cfg.Skills {
				if sk.ID == "" {
					return false
				}
				if sk.InputSchema == "" {
					return false
				}
				if sk.ExampleArgs == "" {
					return false
				}
				var schema map[string]any
				if err := json.Unmarshal([]byte(sk.InputSchema), &schema); err != nil {
					return false
				}
				var example any
				if err := json.Unmarshal([]byte(sk.ExampleArgs), &example); err != nil {
					return false
				}
			}

			return true
		},
		genUniqueToolNames(),
	))

	properties.TestingRun(t)
}


