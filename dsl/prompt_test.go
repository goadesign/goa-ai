package dsl_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	. "goa.design/goa-ai/dsl"
	agentsexpr "goa.design/goa-ai/expr/agent"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa-ai/runtime/agent/prompt"
	. "goa.design/goa/v3/dsl"
	goaexpr "goa.design/goa/v3/expr"
)

type qaUnion struct{ text string }

func (u qaUnion) Kind() string { return "text" }
func (u qaUnion) Value() (any, error) { //nolint:unparam // Error return mirrors Goa generated union Value method signature.
	return u.text, nil
}

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
			want: "invalid prompt template syntax",
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
			want: `field "MissingField" not declared in type struct {}`,
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

// TestReviewFollowupNamedEmptyArgs verifies that an empty named user type passed to Args
// rejects undeclared field accesses in prompt templates.
func TestReviewFollowupNamedEmptyArgs(t *testing.T) {
	err := runDSLWithError(t, func() {
		API("test", func() {})
		empty := Type("EmptyArgs", func() {})
		Service("support", func() {
			Prompt("main", func() {
				Template(`{{ .Missing }}`)
				Args(empty)
			})
		})
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `field "Missing" not declared in Args`)
}

// TestReviewFollowupInheritedArgs verifies that inline Args using Extend materialize
// inherited fields before template static validation.
func TestReviewFollowupInheritedArgs(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		base := Type("Base", func() {
			Attribute("ticket_id", String, "Ticket identifier")
			Required("ticket_id")
		})
		Service("support", func() {
			Prompt("main", func() {
				Template(`{{ .TicketID }}`)
				Args(func() {
					Extend(base)
				})
			})
		})
	})
}

// TestReviewFollowupNamedInheritedArgs verifies that named user types extending a base
// type materialize inherited fields before template static validation.
func TestReviewFollowupNamedInheritedArgs(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		base := Type("Base", func() {
			Attribute("ticket_id", String, "Ticket identifier")
			Required("ticket_id")
		})
		derived := Type("Derived", func() {
			Extend(base)
		})
		Service("support", func() {
			Prompt("main", func() {
				Template(`{{ .TicketID }}`)
				Args(derived)
			})
		})
	})
}

// TestReviewFollowupManualCompositionDSL verifies that sub-prompt render calls referencing
// manually registered or external prompts pass DSL evaluation.
func TestReviewFollowupManualCompositionDSL(t *testing.T) {
	runDSL(t, func() {
		API("test", func() {})
		Service("support", func() {
			Prompt("main", func() {
				Template(`{{ render "manual.shared" }}`)
			})
		})
	})
}

// TestQABC70Adapter verifies that template adapters correctly support bytes ranges,
// union methods (Kind, Value), and dynamic Any arguments.
func TestQABC70Adapter(t *testing.T) {
	var dynamic goaexpr.UserType
	tests := []struct {
		name, source string
		schema       func()
		data         any
	}{
		{"bytes_range", `{{range .Data}}{{.}}{{end}}`, func() { Attribute("data", Bytes, "Raw bytes"); Required("data") }, struct{ Data []byte }{[]byte{65, 66}}},
		{"union_kind", `{{.Choice.Kind}}`, func() { OneOf("choice", "Chosen value", func() { Attribute("text", String, "Text value") }); Required("choice") }, struct{ Choice qaUnion }{qaUnion{"Ada"}}},
		{"union_value", `{{.Choice.Value}}`, func() { OneOf("choice", "Chosen value", func() { Attribute("text", String, "Text value") }); Required("choice") }, struct{ Choice qaUnion }{qaUnion{"Ada"}}},
		{"named_any", `{{.Payload.foo}}`, func() { Attribute("payload", dynamic, "Dynamic payload"); Required("payload") }, struct{ Payload any }{map[string]any{"foo": "Ada"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runDSLWithError(t, func() {
				dynamic = Type("Dynamic", Any)
				Service("support", func() {
					Prompt("main", func() {
						Template(tt.source)
						Args(tt.schema)
					})
				})
			})
			require.NoError(t, err)
			r := prompt.NewRegistry(nil)
			registerErr := r.Register(prompt.PromptSpec{
				ID:       "qa.main",
				Role:     prompt.PromptRoleUser,
				Template: tt.source,
				ArgsType: reflect.TypeOf(tt.data),
			})
			require.NoError(t, registerErr)
			out, renderErr := r.Render(context.Background(), "qa.main", prompt.Scope{}, tt.data)
			require.NoError(t, renderErr)
			require.NotNil(t, out)
		})
	}
}

// TestQABC70ExternalOverride verifies that struct:field:type overrides are treated dynamically
// at compile time and enforced against the concrete type at runtime.
func TestQABC70ExternalOverride(t *testing.T) {
	for _, source := range []string{`{{.Timestamp.Year}}`, `{{.Timestamp.Missing}}`} {
		t.Run(source, func(t *testing.T) {
			err := runDSLWithError(t, func() {
				Service("support", func() {
					Prompt("main", func() {
						Template(source)
						Args(func() {
							Attribute("timestamp", String, "Time", func() {
								Meta("struct:field:type", "time.Time", "time")
							})
						})
					})
				})
			})
			require.NoError(t, err)
			r := prompt.NewRegistry(nil)
			registerErr := r.Register(prompt.PromptSpec{
				ID:       "qa.main",
				Role:     prompt.PromptRoleUser,
				Template: source,
				ArgsType: reflect.TypeFor[struct{ Timestamp time.Time }](),
			})
			if source == `{{.Timestamp.Year}}` {
				require.NoError(t, registerErr)
			} else {
				require.ErrorIs(t, registerErr, prompt.ErrTemplateFieldNotFound)
			}
		})
	}
}

// TestReview24NestedExtend checks inherited fields before prompt validation.
func TestReview24NestedExtend(t *testing.T) {
	tests := []struct {
		name, source string
		shape        func(goaexpr.UserType)
	}{
		{"inline_object", `{{.Nested.Query}}`, func(base goaexpr.UserType) {
			Attribute("nested", &goaexpr.Object{}, "Nested input", func() { Extend(base) })
			Required("nested")
		}},
		{"array_element", `{{range .Items}}{{.Query}}{{end}}`, func(base goaexpr.UserType) {
			Attribute("items", ArrayOf(base), "Nested inputs")
			Required("items")
		}},
		{"map_element", `{{range .Items}}{{.Query}}{{end}}`, func(base goaexpr.UserType) {
			Attribute("items", MapOf(String, base), "Nested inputs")
			Required("items")
		}},
		{"array_inline_element", `{{range .Items}}{{.Query}}{{end}}`, func(base goaexpr.UserType) {
			Attribute("items", ArrayOf(&goaexpr.Object{}, func() { Extend(base) }), "Nested inputs")
			Required("items")
		}},
		{"map_inline_element", `{{range .Items}}{{.Query}}{{end}}`, func(base goaexpr.UserType) {
			Attribute("items", MapOf(String, &goaexpr.Object{}, func() { Elem(func() { Extend(base) }) }), "Nested inputs")
			Required("items")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runDSLWithError(t, func() {
				base := Type("Base", func() {
					Attribute("query", String, "Required query", func() { MinLength(2) })
					Required("query")
				})
				derived := Type("Derived", func() { Extend(base) })
				Service("support", func() {
					Prompt("main", func() {
						Template(tt.source)
						Args(func() { tt.shape(derived) })
					})
				})
			})
			require.NoError(t, err)
			require.NotEmpty(t, agentsexpr.Root.Prompts[0].Args)
		})
	}
}

// TestReview24UnionTruth checks the always-true generated union value shape.
func TestReview24UnionTruth(t *testing.T) {
	for _, source := range []string{`{{(or .Choice "fallback").Kind}}`, `{{$x := .Choice}}{{if .Choice}}{{else}}{{$x = "fallback"}}{{end}}{{$x.Kind}}`} {
		t.Run(source, func(t *testing.T) {
			err := runDSLWithError(t, func() {
				Service("support", func() {
					Prompt("main", func() {
						Template(source)
						Args(func() {
							OneOf("choice", "Selected value", func() { Attribute("text", String, "Text value") })
							Required("choice")
						})
					})
				})
			})
			require.NoError(t, err)
		})
	}
}

// TestReview24Preservation checks fresh schema shapes and required rejections.
func TestReview24Preservation(t *testing.T) {
	tests := []struct {
		name, source string
		schema       func()
		valid        bool
	}{
		{"recursive_named", `{{.Root.Child.Child.Label}}`, func() {
			node := Type("Node", func() {
				Attribute("label", String, "Label")
				Attribute("child", "Node", "Child node")
				Required("label")
			})
			Service("support", func() {
				Prompt("main", func() {
					Template(`{{.Root.Child.Child.Label}}`)
					Args(func() { Attribute("root", node, "Root node"); Required("root") })
				})
			})
		}, true},
		{"nested_reference", `{{.Nested.Query}}`, func() {
			base := Type("Base", func() { Attribute("query", String, "Query"); Required("query") })
			Service("support", func() {
				Prompt("main", func() {
					Template(`{{.Nested.Query}}`)
					Args(func() {
						Attribute("nested", &goaexpr.Object{}, "Nested", func() { Reference(base); Attribute("query") })
					})
				})
			})
		}, true},
		{"deep_external_alias", `{{.Payload.Year}}`, func() {
			inner := Type("Inner", String, func() { Meta("struct:field:type", "time.Time", "time") })
			middle := Type("Middle", inner)
			outer := Type("Outer", middle)
			Service("support", func() {
				Prompt("main", func() { Template(`{{.Payload.Year}}`); Args(func() { Attribute("payload", outer, "Timestamp") }) })
			})
		}, true},
		{"closed_empty", `{{.Missing}}`, func() {
			Service("support", func() { Prompt("main", func() { Template(`{{.Missing}}`); Args(func() {}) }) })
		}, false},
		{"two_integer_vars", `{{range $i,$v := .Count}}{{$v}}{{end}}`, func() {
			Service("support", func() {
				Prompt("main", func() {
					Template(`{{range $i,$v := .Count}}{{$v}}{{end}}`)
					Args(func() { Attribute("count", Int64, "Count") })
				})
			})
		}, false},
		{"nested_bad_field", `{{(index .Items "key").Missing}}`, func() {
			item := Type("Item", func() { Attribute("label", String, "Label") })
			Service("support", func() {
				Prompt("main", func() {
					Template(`{{(index .Items "key").Missing}}`)
					Args(func() { Attribute("items", MapOf(String, item), "Items") })
				})
			})
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runDSLWithError(t, tt.schema)
			if tt.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
