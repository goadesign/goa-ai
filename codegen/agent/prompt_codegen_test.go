package codegen_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	codegen "goa.design/goa-ai/codegen/agent"
	"goa.design/goa-ai/codegen/testhelpers"
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
	assert.Contains(t, promptsContent, `JSONType: "null"`)

	// PromptSpecs aggregator and RegisterPrompts
	assert.Contains(t, promptsContent, "func PromptSpecs() []prompt.PromptSpec")
	assert.Contains(t, promptsContent, "PromptSpecFarewell(),")
	assert.Contains(t, promptsContent, "PromptSpecSystem(),")
	assert.Contains(t, promptsContent, "PromptSpecTriage(),")
	assert.Contains(t, promptsContent, "func RegisterPrompts(reg *prompt.Registry) error")

	// Render helpers
	assert.Contains(t, promptsContent, "func RenderSystemPrompt(ctx context.Context, pctx planner.PlannerContext, args SystemPromptArgs) (*prompt.PromptContent, error)")
	assert.Contains(t, promptsContent, "func RenderSystemPromptWithRegistry(ctx context.Context, reg *prompt.Registry, scope prompt.Scope, args SystemPromptArgs) (*prompt.PromptContent, error)")
	assert.Contains(t, promptsContent, "func RenderTriagePrompt(ctx context.Context, pctx planner.PlannerContext, args TriagePromptArgs) (*prompt.PromptContent, error)")
	assert.Contains(t, promptsContent, "func RenderTriagePromptWithRegistry(ctx context.Context, reg *prompt.Registry, scope prompt.Scope, args TriagePromptArgs) (*prompt.PromptContent, error)")
	assert.Contains(t, promptsContent, "func RenderFarewellPrompt(ctx context.Context, pctx planner.PlannerContext) (*prompt.PromptContent, error)")
	assert.Contains(t, promptsContent, "func RenderFarewellPromptWithRegistry(ctx context.Context, reg *prompt.Registry, scope prompt.Scope) (*prompt.PromptContent, error)")

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
	assert.Contains(t, registryContent, "PromptSpecs: PromptSpecs(),")
	assert.Contains(t, registryContent, "if err := rt.RegisterAgent(ctx, registration); err != nil {")

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

// TestPromptEndToEndCompiledAndExecuted verifies that generated agent and service
// prompt code compiles under standard Go toolchain and executes against runtime.Runtime,
// prompt.Registry, and PlannerContext.
func TestPromptEndToEndCompiledAndExecuted(t *testing.T) {
	design := func() {
		Base := goadsl.Type("Base", func() {
			goadsl.Attribute("created_at", goadsl.String, "Creation timestamp")
		})
		Author := goadsl.Type("Author", func() {
			goadsl.Attribute("name", goadsl.String, "Author name")
		})
		Tag := goadsl.Type("Tag", func() {
			goadsl.Attribute("label", goadsl.String, "Tag label")
		})
		Ticket := goadsl.Type("Ticket", func() {
			goadsl.Extend(Base)
			goadsl.Attribute("ticket_id", goadsl.String, "Unique ticket identifier", func() {
				goadsl.Meta("struct:field:name", "CustomID")
			})
			goadsl.Attribute("author", Author, "Ticket author")
			goadsl.Attribute("tags", goadsl.ArrayOf(Tag), "Ticket tags")
			goadsl.Required("ticket_id", "author")
		})

		goadsl.API("support_api", func() {})
		goadsl.Service("support", func() {
			Prompt("farewell", func() {
				goadsl.Description("Service farewell")
				Role("synthesis")
				Template("Thank you for contacting support.")
			})

			Agent("helpdesk", "Help desk agent", func() {
				SystemPrompt("You are {{ .AssistantName }}.", func() {
					Args(func() {
						goadsl.Attribute("AssistantName", goadsl.String)
						goadsl.Required("AssistantName")
					})
				})

				Prompt("triage", func() {
					goadsl.Description("Triage ticket")
					Role("user")
					Template("Ticket {{ .CustomID }} by {{ .Author.Name }} (Created: {{ .CreatedAt }}): {{ render \"support.farewell\" . }}")
					Args(Ticket)
				})
			})
		})
	}

	files := testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", design)
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	goMod := "module generated.local\n\ngo 1.24\n\nrequire (\n\tgoa.design/goa-ai v0.0.0\n\tgithub.com/stretchr/testify v1.10.0\n)\n\nreplace goa.design/goa-ai => " + filepath.ToSlash(repoRoot) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte(goMod), 0o600))

	for _, file := range files {
		_, err := file.Render(root)
		require.NoErrorf(t, err, "render %s", file.Path)
	}

	testSource := `package helpdesk_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genhelpdesk "generated.local/gen/support/agents/helpdesk"
	gensvc "generated.local/gen/support/prompts"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/runtime"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
)

type mockPlanner struct{}

func (m *mockPlanner) PlanStart(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
	return &planner.PlanResult{}, nil
}

func (m *mockPlanner) PlanResume(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
	return &planner.PlanResult{}, nil
}

type mockPlannerContext struct {
	planner.PlannerContext
	reg   *prompt.Registry
	scope prompt.Scope
}

func (m *mockPlannerContext) RenderPrompt(ctx context.Context, id prompt.Ident, data any) (*prompt.PromptContent, error) {
	return m.reg.Render(ctx, id, m.scope, data)
}

func TestCompiledPromptsIntegration(t *testing.T) {
	ctx := context.Background()

	// 1. Verify prompt specs factories
	sysSpec := genhelpdesk.PromptSpecSystem()
	assert.Equal(t, genhelpdesk.SystemPromptID, sysSpec.ID)
	assert.Equal(t, prompt.PromptRoleSystem, sysSpec.Role)

	triageSpec := genhelpdesk.PromptSpecTriage()
	assert.Equal(t, genhelpdesk.PromptTriageID, triageSpec.ID)
	assert.Equal(t, prompt.PromptRoleUser, triageSpec.Role)

	farewellSpec := gensvc.PromptSpecFarewell()
	assert.Equal(t, gensvc.PromptFarewellID, farewellSpec.ID)
	assert.Equal(t, prompt.PromptRoleSynthesis, farewellSpec.Role)

	// 2. Setup runtime with inmem store and PromptRegistry
	promptStore := prompt.NewInMemoryStore()
	store := storageinmem.New()
	rt := runtime.New(store, runtime.WithPromptStore(promptStore))
	reg := rt.PromptRegistry
	require.NotNil(t, reg)

	// Register service-level prompts
	require.NoError(t, gensvc.RegisterPrompts(reg))

	// 3. NewHelpdeskAgentRegistration contains PromptSpecs
	registration, err := genhelpdesk.NewHelpdeskAgentRegistration(rt, genhelpdesk.HelpdeskAgentConfig{
		Planner: &mockPlanner{},
	})
	require.NoError(t, err)
	require.Len(t, registration.PromptSpecs, 2)

	// 4. Register agent: rt.RegisterAgent registers PromptSpecs
	require.NoError(t, rt.RegisterAgent(ctx, registration))

	// 5. Register agent again returns prompt.ErrDuplicatePromptSpec
	errDup := rt.RegisterAgent(ctx, registration)
	require.Error(t, errDup)
	require.True(t, errors.Is(errDup, prompt.ErrDuplicatePromptSpec), "expected ErrDuplicatePromptSpec, got: %v", errDup)

	// 6. Render with registry directly:
	scope := prompt.Scope{
		SessionID: "session-1",
	}
	strPtr := func(s string) *string { return &s }
	args := genhelpdesk.TriagePromptArgs{
		CustomID:  "T-123",
		CreatedAt: strPtr("2026-11-01"),
		Author: &genhelpdesk.Author{
			Name: strPtr("Alice"),
		},
		Tags: []*genhelpdesk.Tag{
			{Label: strPtr("urgent")},
		},
	}

	content, err := genhelpdesk.RenderTriagePromptWithRegistry(ctx, reg, scope, args)
	require.NoError(t, err)
	require.NotNil(t, content)
	assert.Contains(t, content.Text, "Ticket T-123 by Alice (Created: 2026-11-01): Thank you for contacting support.")

	// 7. Verify SetOverride behavior
	badTmpl := "Undeclared: {{ .NonExistent }}"
	errBad := reg.SetOverride(ctx, genhelpdesk.PromptTriageID, scope, badTmpl, nil)
	require.Error(t, errBad)
	require.True(t, errors.Is(errBad, prompt.ErrTemplateFieldNotFound), "expected ErrTemplateFieldNotFound, got: %v", errBad)

	goodTmpl := "Overridden: {{ .CustomID }} from {{ .Author.Name }}"
	require.NoError(t, reg.SetOverride(ctx, genhelpdesk.PromptTriageID, scope, goodTmpl, nil))

	contentOverridden, err := genhelpdesk.RenderTriagePromptWithRegistry(ctx, reg, scope, args)
	require.NoError(t, err)
	assert.Equal(t, "Overridden: T-123 from Alice", contentOverridden.Text)

	// 8. Test RenderTriagePrompt with PlannerContext
	pctx := &mockPlannerContext{reg: reg, scope: scope}
	contentPctx, err := genhelpdesk.RenderTriagePrompt(ctx, pctx, args)
	require.NoError(t, err)
	assert.Equal(t, "Overridden: T-123 from Alice", contentPctx.Text)
}
`
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/support/agents/helpdesk/prompts_compiled_test.go"), []byte(testSource), 0o600))

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-mod=mod", "-race", "-count=1", "-v", "./gen/support/agents/helpdesk")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}
