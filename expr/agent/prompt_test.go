package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/prompt"
	goaexpr "goa.design/goa/v3/expr"
)

func TestPromptExprValidateSuccess(t *testing.T) {
	service := &goaexpr.ServiceExpr{Name: "tickets"}
	agent := &AgentExpr{Name: "triage", Service: service}

	argsAttr := &goaexpr.AttributeExpr{
		Type: &goaexpr.Object{
			&goaexpr.NamedAttributeExpr{
				Name: "assistant_name",
				Attribute: &goaexpr.AttributeExpr{
					Type: goaexpr.String,
				},
			},
			&goaexpr.NamedAttributeExpr{
				Name: "ticket_id",
				Attribute: &goaexpr.AttributeExpr{
					Type: goaexpr.String,
				},
			},
		},
	}

	p := &PromptExpr{
		Name:     "evaluate",
		Role:     prompt.PromptRoleUser,
		Template: "Hello {{ .AssistantName }}, please triage {{ .ticket_id }}! {{ render \"other.id\" . }}",
		Args:     argsAttr,
		Service:  service,
		Agent:    agent,
	}

	err := p.Validate()
	require.NoError(t, err)
}

func TestPromptExprValidateMalformedTemplate(t *testing.T) {
	service := &goaexpr.ServiceExpr{Name: "tickets"}
	agent := &AgentExpr{Name: "triage", Service: service}

	p := &PromptExpr{
		Name:     "evaluate",
		Role:     prompt.PromptRoleUser,
		Template: "Hello {{ .Unclosed",
		Service:  service,
		Agent:    agent,
	}

	err := p.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse template")
}

func TestPromptExprValidateUndeclaredField(t *testing.T) {
	service := &goaexpr.ServiceExpr{Name: "tickets"}
	agent := &AgentExpr{Name: "triage", Service: service}

	argsAttr := &goaexpr.AttributeExpr{
		Type: &goaexpr.Object{
			&goaexpr.NamedAttributeExpr{
				Name: "assistant_name",
				Attribute: &goaexpr.AttributeExpr{
					Type: goaexpr.String,
				},
			},
		},
	}

	t.Run("typo field with args declared", func(t *testing.T) {
		p := &PromptExpr{
			Name:     "evaluate",
			Role:     prompt.PromptRoleUser,
			Template: "Hello {{ .TypoField }}!",
			Args:     argsAttr,
			Service:  service,
			Agent:    agent,
		}

		err := p.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "template references undeclared argument \".TypoField\" on Args contract")
	})

	t.Run("field with no args declared", func(t *testing.T) {
		p := &PromptExpr{
			Name:     "evaluate",
			Role:     prompt.PromptRoleUser,
			Template: "Hello {{ .AnyField }}!",
			Service:  service,
			Agent:    agent,
		}

		err := p.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "template references undeclared argument \".AnyField\" but prompt declares no Args")
	})
}

func TestPromptExprValidateRole(t *testing.T) {
	service := &goaexpr.ServiceExpr{Name: "tickets"}
	p := &PromptExpr{
		Name:     "test",
		Role:     prompt.PromptRole("admin"),
		Template: "Valid template",
		Service:  service,
	}

	err := p.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prompt role \"admin\" must be one of \"system\", \"user\", \"tool\", or \"synthesis\"")
}

func TestPromptExprValidateEmptyFields(t *testing.T) {
	service := &goaexpr.ServiceExpr{Name: "tickets"}

	t.Run("empty name", func(t *testing.T) {
		p := &PromptExpr{
			Role:     prompt.PromptRoleSystem,
			Template: "Valid template",
			Service:  service,
		}
		err := p.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "prompt name cannot be empty")
	})

	t.Run("empty template", func(t *testing.T) {
		p := &PromptExpr{
			Name:    "sys",
			Role:    prompt.PromptRoleSystem,
			Service: service,
		}
		err := p.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "prompt template cannot be empty")
	})
}

func TestPromptExprIDAndVersion(t *testing.T) {
	service := &goaexpr.ServiceExpr{Name: "support"}
	agent := &AgentExpr{Name: "assistant", Service: service}

	tmpl := "You are a customer service assistant."
	agentPrompt := &PromptExpr{
		Name:     "system",
		Role:     prompt.PromptRoleSystem,
		Template: tmpl,
		Service:  service,
		Agent:    agent,
	}
	assert.Equal(t, prompt.Ident("support.assistant.system"), agentPrompt.ID())
	assert.Equal(t, prompt.VersionFromTemplate(tmpl), agentPrompt.Version())

	servicePrompt := &PromptExpr{
		Name:     "global_notice",
		Role:     prompt.PromptRoleSystem,
		Template: tmpl,
		Service:  service,
	}
	assert.Equal(t, prompt.Ident("support.global_notice"), servicePrompt.ID())
}

func TestRootExprValidateRejectsDuplicatePromptIDs(t *testing.T) {
	service := &goaexpr.ServiceExpr{Name: "support"}
	agent := &AgentExpr{Name: "assistant", Service: service}

	p1 := &PromptExpr{
		Name:     "system",
		Role:     prompt.PromptRoleSystem,
		Template: "Template 1",
		Service:  service,
		Agent:    agent,
	}
	p2 := &PromptExpr{
		Name:     "system",
		Role:     prompt.PromptRoleSystem,
		Template: "Template 2",
		Service:  service,
		Agent:    agent,
	}
	agent.Prompts = []*PromptExpr{p1, p2}

	root := &RootExpr{
		Agents: []*AgentExpr{agent},
	}

	err := root.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prompt ID \"support.assistant.system\" duplicates prompt")
}
