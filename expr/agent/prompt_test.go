package agent

import (
	"bytes"
	"context"
	"reflect"
	"testing"
	"text/template"

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
	assert.Contains(t, err.Error(), "invalid prompt template syntax")
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
		assert.Contains(t, err.Error(), `field "TypoField" not declared in Args`)
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
		assert.Contains(t, err.Error(), `field "AnyField" not declared in type struct {}`)
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

func TestRootExprValidateAllowsManualComposition(t *testing.T) {
	service := &goaexpr.ServiceExpr{Name: "support"}
	agent := &AgentExpr{Name: "assistant", Service: service}

	p := &PromptExpr{
		Name:     "main",
		Role:     prompt.PromptRoleUser,
		Template: `Welcome {{ render "manual.shared" . }}`,
		Service:  service,
		Agent:    agent,
	}
	agent.Prompts = []*PromptExpr{p}

	root := &RootExpr{
		Agents: []*AgentExpr{agent},
	}

	err := root.Validate()
	require.NoError(t, err)
}

func TestReviewFollowupCollectionMetadata(t *testing.T) {
	tests := []struct {
		name   string
		source string
		field  string
		typ    goaexpr.DataType
		data   any
	}{
		{"scalar_array_index", `{{ index .Names 0 }}`, "names", &goaexpr.Array{ElemType: &goaexpr.AttributeExpr{Type: goaexpr.String}}, struct{ Names []string }{[]string{"Ada"}}},
		{"scalar_map_bad_descendant", `{{ .Labels.foo.Missing }}`, "labels", &goaexpr.Map{KeyType: &goaexpr.AttributeExpr{Type: goaexpr.String}, ElemType: &goaexpr.AttributeExpr{Type: goaexpr.String}}, struct{ Labels map[string]string }{map[string]string{"foo": "bar"}}},
		{"scalar_array_range", `{{ range .Names }}{{ . }}{{ end }}`, "names", &goaexpr.Array{ElemType: &goaexpr.AttributeExpr{Type: goaexpr.String}}, struct{ Names []string }{[]string{"Ada"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &PromptExpr{
				Name:     "main",
				Role:     prompt.PromptRoleUser,
				Template: tt.source,
				Service:  &goaexpr.ServiceExpr{Name: "test"},
				Args:     &goaexpr.AttributeExpr{Type: &goaexpr.Object{&goaexpr.NamedAttributeExpr{Name: tt.field, Attribute: &goaexpr.AttributeExpr{Type: tt.typ}}}},
			}
			p.Prepare()
			verr := p.Validate()
			parsed, err := template.New("test").Option("missingkey=error").Parse(tt.source)
			require.NoError(t, err)
			var out bytes.Buffer
			err = parsed.Execute(&out, tt.data)
			assert.Equal(t, err == nil, verr == nil, "static validation agrees with real Go template execution")
		})
	}
}

func TestReviewFollowupEmptyObject(t *testing.T) {
	p := &PromptExpr{
		Name:     "main",
		Role:     prompt.PromptRoleUser,
		Template: `{{ .Missing }}`,
		Service:  &goaexpr.ServiceExpr{Name: "test"},
		Args: &goaexpr.AttributeExpr{
			Type: &goaexpr.UserTypeExpr{
				TypeName:      "EmptyArgs",
				AttributeExpr: &goaexpr.AttributeExpr{Type: &goaexpr.Object{}},
			},
		},
	}
	p.Prepare()
	verr := p.Validate()
	require.Error(t, verr)
	assert.Contains(t, verr.Error(), `field "Missing" not declared in Args`)
}

func TestReviewFollowupManualComposition(t *testing.T) {
	p := &PromptExpr{
		Name:     "main",
		Role:     prompt.PromptRoleUser,
		Template: `{{ render "manual.shared" }}`,
		Service:  &goaexpr.ServiceExpr{Name: "test"},
	}
	rootErr := (&RootExpr{Prompts: []*PromptExpr{p}}).Validate()
	require.NoError(t, rootErr)

	r := prompt.NewRegistry(nil)
	for _, spec := range []prompt.PromptSpec{
		{ID: "manual.shared", Role: prompt.PromptRoleUser, Template: "shared instructions", ArgsType: reflect.TypeFor[struct{}]()},
		{ID: p.ID(), Role: p.Role, Template: p.Template, ArgsType: reflect.TypeFor[struct{}]()},
	} {
		require.NoError(t, r.Register(spec))
	}
	content, renderErr := r.Render(context.Background(), p.ID(), prompt.Scope{}, nil)
	require.NoError(t, renderErr)
	assert.Equal(t, "shared instructions", content.Text)
}

func TestReviewFollowupRecursiveObject(t *testing.T) {
	type node struct {
		Name string
		Next *node
	}
	nodeType := &goaexpr.UserTypeExpr{TypeName: "Node", AttributeExpr: &goaexpr.AttributeExpr{Type: &goaexpr.Object{}}}
	nodeType.Type = &goaexpr.Object{
		&goaexpr.NamedAttributeExpr{Name: "name", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String}},
		&goaexpr.NamedAttributeExpr{Name: "next", Attribute: &goaexpr.AttributeExpr{Type: nodeType}},
	}
	for _, source := range []string{`{{ .Next.Name }}`, `{{ .Next.Missing }}`} {
		t.Run(source, func(t *testing.T) {
			p := &PromptExpr{
				Name:     "main",
				Role:     prompt.PromptRoleUser,
				Template: source,
				Service:  &goaexpr.ServiceExpr{Name: "test"},
				Args:     &goaexpr.AttributeExpr{Type: nodeType},
			}
			p.Prepare()
			validationErr := p.Validate()
			r := prompt.NewRegistry(nil)
			registrationErr := r.Register(prompt.PromptSpec{
				ID:       p.ID(),
				Role:     p.Role,
				Template: p.Template,
				ArgsType: reflect.TypeFor[node](),
			})
			assert.Equal(t, validationErr == nil, registrationErr == nil, "recursive object validation agrees with registration")
			if registrationErr == nil {
				content, renderErr := r.Render(context.Background(), p.ID(), prompt.Scope{}, &node{Name: "root", Next: &node{Name: "child"}})
				require.NoError(t, renderErr)
				assert.Equal(t, "child", content.Text)
			}
		})
	}
}
