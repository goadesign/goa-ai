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
		Template: "Hello {{ .AssistantName }}, please triage {{ .TicketID }}! {{ render \"other.id\" . }}",
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
	assert.Contains(t, err.Error(), "prompt template parse failed")
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
		assert.Contains(t, err.Error(), "field \"TypoField\" not declared in prompt spec")
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
		assert.Contains(t, err.Error(), "field \"AnyField\" not declared in prompt spec")
	})
}

func TestPromptExprValidateRejectNonObjectArgs(t *testing.T) {
	service := &goaexpr.ServiceExpr{Name: "tickets"}
	agent := &AgentExpr{Name: "triage", Service: service}

	t.Run("primitive args", func(t *testing.T) {
		p := &PromptExpr{
			Name:     "evaluate",
			Role:     prompt.PromptRoleUser,
			Template: "Hello {{ . }}",
			Args:     &goaexpr.AttributeExpr{Type: goaexpr.String},
			Service:  service,
			Agent:    agent,
		}
		err := p.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Prompt Args must be an object or a user type backed by an object")
	})

	t.Run("array args", func(t *testing.T) {
		p := &PromptExpr{
			Name:     "evaluate",
			Role:     prompt.PromptRoleUser,
			Template: "Hello",
			Args:     &goaexpr.AttributeExpr{Type: &goaexpr.Array{ElemType: &goaexpr.AttributeExpr{Type: goaexpr.String}}},
			Service:  service,
			Agent:    agent,
		}
		err := p.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Prompt Args must be an object or a user type backed by an object")
	})
}

func TestPromptExprValidateCustomStructFieldName(t *testing.T) {
	service := &goaexpr.ServiceExpr{Name: "tickets"}
	agent := &AgentExpr{Name: "triage", Service: service}

	argsAttr := &goaexpr.AttributeExpr{
		Type: &goaexpr.Object{
			&goaexpr.NamedAttributeExpr{
				Name: "ticket_id",
				Attribute: &goaexpr.AttributeExpr{
					Type: goaexpr.String,
					Meta: goaexpr.MetaExpr{
						"struct:field:name": []string{"CustomTicketID"},
					},
				},
			},
		},
	}

	p := &PromptExpr{
		Name:     "evaluate",
		Role:     prompt.PromptRoleUser,
		Template: "Triage {{ .CustomTicketID }}",
		Args:     argsAttr,
		Service:  service,
		Agent:    agent,
	}

	err := p.Validate()
	require.NoError(t, err)
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

func TestRootExprValidateRejectsGoIdentifierCollision(t *testing.T) {
	service := &goaexpr.ServiceExpr{Name: "support"}
	agent := &AgentExpr{Name: "assistant", Service: service}

	p1 := &PromptExpr{
		Name:     "greet-one",
		Role:     prompt.PromptRoleUser,
		Template: "Hello 1",
		Service:  service,
		Agent:    agent,
	}
	p2 := &PromptExpr{
		Name:     "greet_one",
		Role:     prompt.PromptRoleUser,
		Template: "Hello 2",
		Service:  service,
		Agent:    agent,
	}
	agent.Prompts = []*PromptExpr{p1, p2}

	root := &RootExpr{
		Agents: []*AgentExpr{agent},
	}

	err := root.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "generates Go identifier \"GreetOne\" which duplicates prompt \"greet-one\"")
}

func TestRootExprValidateRejectsUnknownRenderedPrompt(t *testing.T) {
	service := &goaexpr.ServiceExpr{Name: "support"}
	agent := &AgentExpr{Name: "assistant", Service: service}

	p := &PromptExpr{
		Name:     "main",
		Role:     prompt.PromptRoleUser,
		Template: `Welcome {{ render "unknown.prompt.id" . }}`,
		Service:  service,
		Agent:    agent,
	}
	agent.Prompts = []*PromptExpr{p}

	root := &RootExpr{
		Agents: []*AgentExpr{agent},
	}

	err := root.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `renders unknown prompt "unknown.prompt.id"`)
}

func TestRootExprValidateRejectsCompositionCycle(t *testing.T) {
	service := &goaexpr.ServiceExpr{Name: "support"}
	agent := &AgentExpr{Name: "assistant", Service: service}

	p1 := &PromptExpr{
		Name:     "prompt_a",
		Role:     prompt.PromptRoleUser,
		Template: `A calls B: {{ render "support.assistant.prompt_b" . }}`,
		Service:  service,
		Agent:    agent,
	}
	p2 := &PromptExpr{
		Name:     "prompt_b",
		Role:     prompt.PromptRoleUser,
		Template: `B calls A: {{ render "support.assistant.prompt_a" . }}`,
		Service:  service,
		Agent:    agent,
	}
	agent.Prompts = []*PromptExpr{p1, p2}

	root := &RootExpr{
		Agents: []*AgentExpr{agent},
	}

	err := root.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cyclical prompt composition detected: support.assistant.prompt_a -> support.assistant.prompt_b -> support.assistant.prompt_a")
}
