package dsl_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	. "goa.design/goa-ai/dsl"
	agentsexpr "goa.design/goa-ai/expr/agent"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa-ai/runtime/agent/prompt"
	. "goa.design/goa/v3/dsl"
)

func TestStaticSystemPrompt(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		Service("support", func() {
			Agent("assistant", "Support assistant", func() {
				SystemPrompt("You are a helpful customer support assistant.")
			})
		})
	})

	require.Len(t, agentsexpr.Root.Agents, 1)
	agent := agentsexpr.Root.Agents[0]
	require.Len(t, agent.Prompts, 1)
	p := agent.Prompts[0]

	assert.Equal(t, "system", p.Name)
	assert.Equal(t, prompt.PromptRoleSystem, p.Role)
	assert.Equal(t, "You are a helpful customer support assistant.", p.Template)
	assert.Equal(t, prompt.Ident("support.assistant.system"), p.ID())
	assert.Equal(t, prompt.VersionFromTemplate(p.Template), p.Version())
}

func TestParameterizedSystemPrompt(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		Service("support", func() {
			Agent("assistant", "Support assistant", func() {
				SystemPrompt("You are {{ .AssistantName }}.", func() {
					Description("Tenant-customized assistant prompt")
					Args(func() {
						Attribute("assistant_name", String, "Assistant name")
						Required("assistant_name")
					})
				})
			})
		})
	})

	require.Len(t, agentsexpr.Root.Agents, 1)
	agent := agentsexpr.Root.Agents[0]
	require.Len(t, agent.Prompts, 1)
	p := agent.Prompts[0]

	assert.Equal(t, "system", p.Name)
	assert.Equal(t, prompt.PromptRoleSystem, p.Role)
	assert.Equal(t, "You are {{ .AssistantName }}.", p.Template)
	assert.Equal(t, "Tenant-customized assistant prompt", p.Description)
	assert.NotNil(t, p.Args)
	assert.Equal(t, prompt.Ident("support.assistant.system"), p.ID())
}

func TestClosureSystemPrompt(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		Service("support", func() {
			Agent("assistant", "Support assistant", func() {
				SystemPrompt(func() {
					Description("Full closure system prompt")
					Template("You are an AI assistant.")
				})
			})
		})
	})

	require.Len(t, agentsexpr.Root.Agents, 1)
	agent := agentsexpr.Root.Agents[0]
	require.Len(t, agent.Prompts, 1)
	p := agent.Prompts[0]

	assert.Equal(t, "system", p.Name)
	assert.Equal(t, prompt.PromptRoleSystem, p.Role)
	assert.Equal(t, "Full closure system prompt", p.Description)
	assert.Equal(t, "You are an AI assistant.", p.Template)
}

func TestAgentScopedPrompt(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		Service("tickets", func() {
			Agent("triage", "Ticket triage agent", func() {
				Prompt("evaluate", func() {
					Description("Evaluate incoming ticket priority")
					Role("user")
					Template("Evaluate ticket {{ .TicketID }} from {{ .CustomerName }}")
					Args(func() {
						Attribute("ticket_id", String, "Ticket identifier")
						Attribute("customer_name", String, "Customer name")
						Required("ticket_id", "customer_name")
					})
				})
			})
		})
	})

	require.Len(t, agentsexpr.Root.Agents, 1)
	agent := agentsexpr.Root.Agents[0]
	require.Len(t, agent.Prompts, 1)
	p := agent.Prompts[0]

	assert.Equal(t, "evaluate", p.Name)
	assert.Equal(t, prompt.PromptRoleUser, p.Role)
	assert.Equal(t, "Evaluate incoming ticket priority", p.Description)
	assert.Equal(t, prompt.Ident("tickets.triage.evaluate"), p.ID())
	assert.NotNil(t, p.Args)
}

func TestServiceScopedPrompt(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		Service("shared", func() {
			Prompt("summarize", func() {
				Role("user")
				Template("Summarize the following: {{ .Content }}")
				Args(func() {
					Attribute("content", String, "Content to summarize")
					Required("content")
				})
			})
		})
	})

	require.Len(t, agentsexpr.Root.Prompts, 1)
	p := agentsexpr.Root.Prompts[0]

	assert.Equal(t, "summarize", p.Name)
	assert.Nil(t, p.Agent)
	assert.Equal(t, "shared", p.Service.Name)
	assert.Equal(t, prompt.Ident("shared.summarize"), p.ID())
	assert.Equal(t, prompt.PromptRoleUser, p.Role)
}

func TestMCPMethodPromptCoexistence(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		Service("prompts", func() {
			MCP("prompts", "1")
			JSONRPC(func() { POST("/prompts") })
			Method("review", func() {
				Payload(func() {
					Attribute("code", String, "Code snippet")
					Required("code")
				})
				Result(func() {
					Attribute("messages", ArrayOf(String), "Prompt messages")
					Required("messages")
				})
				Prompt("review", "Review code for best practices")
			})
		})
	})

	server := mcpexpr.Root.MCPServers["prompts"]
	require.NotNil(t, server)
	require.Len(t, server.MethodPrompts, 1)
	assert.Equal(t, "review", server.MethodPrompts[0].Name)
	assert.Equal(t, "Review code for best practices", server.MethodPrompts[0].Description)
}

func TestPromptDSLRejections(t *testing.T) {
	tests := []struct {
		name string
		dsl  func()
		want string
	}{
		{
			name: "SystemPrompt outside of agent",
			dsl: func() {
				API("test", func() {})
				Service("svc", func() {
					SystemPrompt("Invalid")
				})
			},
			want: "invalid use",
		},
		{
			name: "Template outside of prompt",
			dsl: func() {
				API("test", func() {})
				Service("svc", func() {
					Agent("agent", "desc", func() {
						Template("Invalid")
					})
				})
			},
			want: "invalid use",
		},
		{
			name: "Role outside of prompt",
			dsl: func() {
				API("test", func() {})
				Service("svc", func() {
					Agent("agent", "desc", func() {
						Role("user")
					})
				})
			},
			want: "invalid use",
		},
		{
			name: "Service prompt without DSL closure",
			dsl: func() {
				API("test", func() {})
				Service("svc", func() {
					Prompt("no_closure", "desc")
				})
			},
			want: "invalid use",
		},
		{
			name: "Empty prompt name",
			dsl: func() {
				API("test", func() {})
				Service("svc", func() {
					Agent("agent", "desc", func() {
						Prompt("", func() {
							Template("Hello")
						})
					})
				})
			},
			want: "prompt name cannot be empty",
		},
		{
			name: "Invalid role value",
			dsl: func() {
				API("test", func() {})
				Service("svc", func() {
					Agent("agent", "desc", func() {
						Prompt("bad_role", func() {
							Role("invalid_role")
							Template("Hello")
						})
					})
				})
			},
			want: "invalid prompt role",
		},
		{
			name: "Duplicate prompt on agent",
			dsl: func() {
				API("test", func() {})
				Service("svc", func() {
					Agent("agent", "desc", func() {
						Prompt("greet", func() { Template("Hello") })
						Prompt("greet", func() { Template("Hi") })
					})
				})
			},
			want: `duplicate prompt "greet" on agent "agent"`,
		},
		{
			name: "Duplicate system prompt on agent",
			dsl: func() {
				API("test", func() {})
				Service("svc", func() {
					Agent("agent", "desc", func() {
						SystemPrompt("You are bot 1.")
						SystemPrompt("You are bot 2.")
					})
				})
			},
			want: `duplicate prompt "system" on agent "agent"`,
		},
		{
			name: "Duplicate prompt on service",
			dsl: func() {
				API("test", func() {})
				Service("svc", func() {
					Prompt("greet", func() { Template("Hello") })
					Prompt("greet", func() { Template("Hi") })
				})
			},
			want: `duplicate prompt "greet" on service "svc"`,
		},
		{
			name: "Agent prompt with empty template fails RunDSL",
			dsl: func() {
				API("test", func() {})
				Service("svc", func() {
					Agent("agent", "desc", func() {
						Prompt("empty_tmpl", func() {
							Role("user")
						})
					})
				})
			},
			want: "prompt template cannot be empty",
		},
		{
			name: "Agent prompt with bad template syntax fails RunDSL",
			dsl: func() {
				API("test", func() {})
				Service("svc", func() {
					Agent("agent", "desc", func() {
						Prompt("bad_syntax", func() {
							Template("Hello {{ .Unclosed")
						})
					})
				})
			},
			want: "prompt template parse failed",
		},
		{
			name: "Agent prompt with undeclared field access fails RunDSL",
			dsl: func() {
				API("test", func() {})
				Service("svc", func() {
					Agent("agent", "desc", func() {
						Prompt("undeclared", func() {
							Template("Hello {{ .MissingField }}")
						})
					})
				})
			},
			want: `field "MissingField" not declared in prompt spec`,
		},
		{
			name: "Agent prompt with non-object Args fails RunDSL",
			dsl: func() {
				API("test", func() {})
				Service("svc", func() {
					Agent("agent", "desc", func() {
						Prompt("bad_args", func() {
							Template("Hello")
							Args(String)
						})
					})
				})
			},
			want: "Prompt Args must be an object or a user type backed by an object",
		},
		{
			name: "Method prompt with extra arguments",
			dsl: func() {
				API("test", func() {})
				Service("svc", func() {
					MCP("prompts", "1")
					Method("m", func() {
						Prompt("p", "desc", func() {})
					})
				})
			},
			want: "too many arguments",
		},
		{
			name: "SystemPrompt inline template with closure Template override",
			dsl: func() {
				API("test", func() {})
				Service("svc", func() {
					Agent("agent", "desc", func() {
						SystemPrompt("inline", func() {
							Template("override")
						})
					})
				})
			},
			want: "prompt template already set",
		},
		{
			name: "Duplicate Template inside Prompt",
			dsl: func() {
				API("test", func() {})
				Service("svc", func() {
					Agent("agent", "desc", func() {
						Prompt("p", func() {
							Template("first")
							Template("second")
						})
					})
				})
			},
			want: "prompt template already set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runDSLWithError(t, tt.dsl)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}
