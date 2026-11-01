package codegen_test

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	codegen "goa.design/goa-ai/codegen/agent"
	. "goa.design/goa-ai/dsl"
	agentsExpr "goa.design/goa-ai/expr/agent"
	goadsl "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// TestAgentPromptCodegen verifies code generation of agent prompt constants, typed Args structs,
// precomputed PromptSpec factories, PromptSpecs aggregator, typed Render helpers,
// auto-registration in Register<Agent>, and specs/prompt_schemas.json.
func TestAgentPromptCodegen(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("prompt_codegen_test", func() {})

		goadsl.Service("assistant", func() {
			Agent("bot", "Bot agent", func() {
				SystemPrompt("You are {{ .AssistantName }}, an automated assistant.", func() {
					Args(func() {
						goadsl.Attribute("AssistantName", goadsl.String, "Name of the assistant")
						goadsl.Required("AssistantName")
					})
				})

				Prompt("triage", func() {
					goadsl.Description("Triage incoming user issues")
					Role("user")
					Template("Please triage ticket {{ .TicketID }}: {{ .Summary }}")
					Args(func() {
						goadsl.Attribute("TicketID", goadsl.String, "Unique ticket identifier")
						goadsl.Attribute("Summary", goadsl.String, "Short issue summary")
						goadsl.Required("TicketID", "Summary")
					})
				})

				Prompt("farewell", func() {
					goadsl.Description("Static farewell message")
					Role("synthesis")
					Template("Thank you for contacting support.")
				})
			})
		})
	}

	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := codegen.BuildFilesForTest("example.com/test", []eval.Root{goaexpr.Root, agentsExpr.Root}, false)
	require.NoError(t, err)

	// 1. Verify gen/assistant/agents/bot/prompts.go
	promptsPath := filepath.ToSlash("gen/assistant/agents/bot/prompts.go")
	var promptsContent string
	for _, f := range files {
		if filepath.ToSlash(f.Path) == promptsPath {
			var buf bytes.Buffer
			for _, s := range f.SectionTemplates {
				require.NoError(t, s.Write(&buf))
			}
			promptsContent = buf.String()
			break
		}
	}
	require.NotEmpty(t, promptsContent, "expected %s to be generated", promptsPath)

	// Constants
	assert.Contains(t, promptsContent, `SystemPromptID prompt.Ident = "assistant.bot.system"`)
	assert.Contains(t, promptsContent, `PromptTriageID prompt.Ident = "assistant.bot.triage"`)
	assert.Contains(t, promptsContent, `PromptFarewellID prompt.Ident = "assistant.bot.farewell"`)

	// Typed Args
	assert.Contains(t, promptsContent, "type SystemPromptArgs struct")
	assert.Contains(t, promptsContent, "AssistantName string")
	assert.Contains(t, promptsContent, "type TriagePromptArgs struct")
	assert.Contains(t, promptsContent, "TicketID string")
	assert.Contains(t, promptsContent, "Summary string")

	// Precomputed PromptSpecs
	assert.Contains(t, promptsContent, "func PromptSpecSystem() prompt.PromptSpec")
	assert.Contains(t, promptsContent, `AgentID:     "assistant.bot"`)
	assert.Contains(t, promptsContent, "Role:        prompt.PromptRoleSystem")
	assert.Contains(t, promptsContent, `Template:    "You are {{ .AssistantName }}, an automated assistant."`)
	assert.Contains(t, promptsContent, `Version:     "sha256:`)
	assert.Contains(t, promptsContent, `tools.FixedField("AssistantName")`)

	assert.Contains(t, promptsContent, "func PromptSpecTriage() prompt.PromptSpec")
	assert.Contains(t, promptsContent, "Role:        prompt.PromptRoleUser")
	assert.Contains(t, promptsContent, `Description: "Triage incoming user issues"`)
	assert.Contains(t, promptsContent, `Template:    "Please triage ticket {{ .TicketID }}: {{ .Summary }}"`)
	assert.Contains(t, promptsContent, `tools.FixedField("TicketID")`)
	assert.Contains(t, promptsContent, `tools.FixedField("Summary")`)

	assert.Contains(t, promptsContent, "func PromptSpecFarewell() prompt.PromptSpec")
	assert.Contains(t, promptsContent, "Role:        prompt.PromptRoleSynthesis")
	assert.Contains(t, promptsContent, "Fields:      nil")

	// PromptSpecs aggregator
	assert.Contains(t, promptsContent, "func PromptSpecs() []prompt.PromptSpec")
	assert.Contains(t, promptsContent, "PromptSpecFarewell(),")
	assert.Contains(t, promptsContent, "PromptSpecSystem(),")
	assert.Contains(t, promptsContent, "PromptSpecTriage(),")

	// Render helpers
	assert.Contains(t, promptsContent, "func RenderSystemPrompt(ctx context.Context, pctx planner.PlannerContext, args SystemPromptArgs) (*prompt.PromptContent, error)")
	assert.Contains(t, promptsContent, "func RenderTriagePrompt(ctx context.Context, pctx planner.PlannerContext, args TriagePromptArgs) (*prompt.PromptContent, error)")
	assert.Contains(t, promptsContent, "func RenderFarewellPrompt(ctx context.Context, pctx planner.PlannerContext) (*prompt.PromptContent, error)")

	// 2. Verify gen/assistant/agents/bot/registry.go has PromptRegistry auto-registration
	registryPath := filepath.ToSlash("gen/assistant/agents/bot/registry.go")
	var registryContent string
	for _, f := range files {
		if filepath.ToSlash(f.Path) == registryPath {
			var buf bytes.Buffer
			for _, s := range f.SectionTemplates {
				require.NoError(t, s.Write(&buf))
			}
			registryContent = buf.String()
			break
		}
	}
	require.NotEmpty(t, registryContent, "expected %s to be generated", registryPath)
	assert.Contains(t, registryContent, "if rt.PromptRegistry != nil {")
	assert.Contains(t, registryContent, "for _, spec := range PromptSpecs() {")
	assert.Contains(t, registryContent, "if err := rt.PromptRegistry.Register(spec); err != nil && !errors.Is(err, prompt.ErrDuplicatePromptSpec) {")

	// 3. Verify gen/assistant/agents/bot/specs/prompt_schemas.json
	schemasPath := filepath.ToSlash("gen/assistant/agents/bot/specs/prompt_schemas.json")
	var schemasContent string
	for _, f := range files {
		if filepath.ToSlash(f.Path) == schemasPath {
			var buf bytes.Buffer
			for _, s := range f.SectionTemplates {
				require.NoError(t, s.Write(&buf))
			}
			schemasContent = buf.String()
			break
		}
	}
	require.NotEmpty(t, schemasContent, "expected %s to be generated", schemasPath)

	var doc struct {
		AgentID string `json:"agent_id"`
		Prompts []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Service     string `json:"service"`
			Agent       string `json:"agent"`
			Role        string `json:"role"`
			Description string `json:"description"`
			Template    string `json:"template"`
			Version     string `json:"version"`
			Args        *struct {
				Name   string          `json:"name"`
				Schema json.RawMessage `json:"schema"`
			} `json:"args"`
		} `json:"prompts"`
	}
	require.NoError(t, json.Unmarshal([]byte(schemasContent), &doc))
	assert.Equal(t, "assistant.bot", doc.AgentID)
	require.Len(t, doc.Prompts, 3)

	promptByID := make(map[string]any)
	for _, p := range doc.Prompts {
		promptByID[p.ID] = p
	}

	// Verify farewell
	farewell, ok := promptByID["assistant.bot.farewell"]
	require.True(t, ok)
	frec := farewell.(struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Service     string `json:"service"`
		Agent       string `json:"agent"`
		Role        string `json:"role"`
		Description string `json:"description"`
		Template    string `json:"template"`
		Version     string `json:"version"`
		Args        *struct {
			Name   string          `json:"name"`
			Schema json.RawMessage `json:"schema"`
		} `json:"args"`
	})
	assert.Equal(t, "farewell", frec.Name)
	assert.Equal(t, "synthesis", frec.Role)
	assert.Equal(t, "Static farewell message", frec.Description)
	assert.True(t, strings.HasPrefix(frec.Version, "sha256:"))
	assert.Nil(t, frec.Args)

	// Verify triage
	triage, ok := promptByID["assistant.bot.triage"]
	require.True(t, ok)
	trec := triage.(struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Service     string `json:"service"`
		Agent       string `json:"agent"`
		Role        string `json:"role"`
		Description string `json:"description"`
		Template    string `json:"template"`
		Version     string `json:"version"`
		Args        *struct {
			Name   string          `json:"name"`
			Schema json.RawMessage `json:"schema"`
		} `json:"args"`
	})
	assert.Equal(t, "triage", trec.Name)
	assert.Equal(t, "user", trec.Role)
	require.NotNil(t, trec.Args)
	assert.Equal(t, "TriagePromptArgs", trec.Args.Name)
	assert.Contains(t, string(trec.Args.Schema), "TicketID")
	assert.Contains(t, string(trec.Args.Schema), "Summary")
}

// TestServicePromptCodegen verifies code generation of service-level prompt templates.
func TestServicePromptCodegen(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("svc_prompt_test", func() {})

		goadsl.Service("support", func() {
			Prompt("greet", func() {
				goadsl.Description("Service greeting")
				Role("system")
				Template("Welcome to {{ .Service }}")
				Args(func() {
					goadsl.Attribute("Service", goadsl.String)
					goadsl.Required("Service")
				})
			})

			Agent("frontdesk", "Front desk bot", func() {})
		})
	}

	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := codegen.BuildFilesForTest("example.com/test", []eval.Root{goaexpr.Root, agentsExpr.Root}, false)
	require.NoError(t, err)

	// Verify gen/support/prompts/prompts.go
	promptsPath := filepath.ToSlash("gen/support/prompts/prompts.go")
	var promptsContent string
	for _, f := range files {
		if filepath.ToSlash(f.Path) == promptsPath {
			var buf bytes.Buffer
			for _, s := range f.SectionTemplates {
				require.NoError(t, s.Write(&buf))
			}
			promptsContent = buf.String()
			break
		}
	}
	require.NotEmpty(t, promptsContent, "expected %s to be generated", promptsPath)
	assert.Contains(t, promptsContent, `PromptGreetID prompt.Ident = "support.greet"`)
	assert.Contains(t, promptsContent, "type GreetPromptArgs struct")
	assert.Contains(t, promptsContent, "Service string")
	assert.Contains(t, promptsContent, "func PromptSpecGreet() prompt.PromptSpec")
	assert.Contains(t, promptsContent, `AgentID:     ""`)
	assert.Contains(t, promptsContent, "func RenderGreetPrompt(ctx context.Context, pctx planner.PlannerContext, args GreetPromptArgs)")

	// Verify gen/support/prompts/specs/prompt_schemas.json
	schemasPath := filepath.ToSlash("gen/support/prompts/specs/prompt_schemas.json")
	var schemasContent string
	for _, f := range files {
		if filepath.ToSlash(f.Path) == schemasPath {
			var buf bytes.Buffer
			for _, s := range f.SectionTemplates {
				require.NoError(t, s.Write(&buf))
			}
			schemasContent = buf.String()
			break
		}
	}
	require.NotEmpty(t, schemasContent, "expected %s to be generated", schemasPath)
	assert.Contains(t, schemasContent, `"service": "support"`)
	assert.Contains(t, schemasContent, `"id": "support.greet"`)
}

// TestAgentWithoutPromptsCodegen verifies that an agent with no prompt declarations
// does not emit prompt files and does not register prompt specs.
func TestAgentWithoutPromptsCodegen(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("no_prompts_test", func() {})

		goadsl.Service("simple", func() {
			Agent("worker", "Worker agent", func() {})
		})
	}

	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := codegen.BuildFilesForTest("example.com/test", []eval.Root{goaexpr.Root, agentsExpr.Root}, false)
	require.NoError(t, err)

	for _, f := range files {
		p := filepath.ToSlash(f.Path)
		assert.False(t, strings.HasSuffix(p, "prompts.go"), "unexpected prompts.go generated at %s", p)
		assert.False(t, strings.HasSuffix(p, "prompt_schemas.json"), "unexpected prompt_schemas.json generated at %s", p)
		if strings.HasSuffix(p, "registry.go") {
			var buf bytes.Buffer
			for _, s := range f.SectionTemplates {
				require.NoError(t, s.Write(&buf))
			}
			assert.NotContains(t, buf.String(), "PromptSpecs()")
		}
	}
}
