package codegen_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	codegen "goa.design/goa-ai/codegen/agent"
	. "goa.design/goa-ai/dsl"
	agentsExpr "goa.design/goa-ai/expr/agent"
	goadsl "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// TestServicePromptArtifactVariations verifies that all variations of prompt argument
// types (simple, nested user types, root user types, located types, collections, unions,
// name collisions, and empty args) compile and execute as expected.
func TestServicePromptArtifactVariations(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	type testCase struct {
		name         string
		configure    func()
		check        string
		expectDSLErr bool
	}

	cases := []testCase{
		{
			name: "simple_service",
			configure: func() {
				Prompt("greet", func() {
					Template("Hi {{.Name}}")
					Args(func() {
						goadsl.Attribute("Name", goadsl.String)
						goadsl.Required("Name")
					})
				})
			},
			check: `package prompts_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gensvc "review.local/gen/support/prompts"
	"goa.design/goa-ai/runtime/agent/prompt"
)

func TestSimpleService(t *testing.T) {
	reg := prompt.NewRegistry(nil)
	require.NoError(t, gensvc.RegisterPrompts(reg))
	scope := prompt.Scope{}
	content, err := gensvc.RenderGreetPromptWithRegistry(context.Background(), reg, scope, gensvc.GreetPromptArgs{Name: "World"})
	require.NoError(t, err)
	assert.Equal(t, "Hi World", content.Text)
}
`,
		},
		{
			name: "nested_named",
			configure: func() {
				Ticket := goaexpr.Root.UserType("Ticket")
				Prompt("greet", func() {
					Template("Hi {{.Ticket.Title}}")
					Args(func() {
						goadsl.Attribute("ticket", Ticket)
						goadsl.Required("ticket")
					})
				})
			},
			check: `package prompts_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gensvc "review.local/gen/support/prompts"
	"goa.design/goa-ai/runtime/agent/prompt"
)

func TestNestedNamed(t *testing.T) {
	reg := prompt.NewRegistry(nil)
	require.NoError(t, gensvc.RegisterPrompts(reg))
	content, err := gensvc.RenderGreetPromptWithRegistry(context.Background(), reg, prompt.Scope{}, gensvc.GreetPromptArgs{
		Ticket: &gensvc.Ticket{Title: "Issue 1"},
	})
	require.NoError(t, err)
	assert.Equal(t, "Hi Issue 1", content.Text)
}
`,
		},
		{
			name: "root_named",
			configure: func() {
				Ticket := goaexpr.Root.UserType("Ticket")
				Prompt("greet", func() {
					Template("Hi {{.Title}}")
					Args(Ticket)
				})
			},
			check: `package prompts_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gensvc "review.local/gen/support/prompts"
	"goa.design/goa-ai/runtime/agent/prompt"
)

func TestRootNamed(t *testing.T) {
	reg := prompt.NewRegistry(nil)
	require.NoError(t, gensvc.RegisterPrompts(reg))
	content, err := gensvc.RenderGreetPromptWithRegistry(context.Background(), reg, prompt.Scope{}, gensvc.GreetPromptArgs{
		Title: "Direct Ticket",
	})
	require.NoError(t, err)
	assert.Equal(t, "Hi Direct Ticket", content.Text)
}
`,
		},
		{
			name: "located",
			configure: func() {
				Ticket := goaexpr.Root.UserType("Ticket")
				Prompt("greet", func() {
					Template("Hi {{.Ticket.Title}}")
					Args(func() {
						goadsl.Attribute("ticket", Ticket)
					})
				})
			},
			check: `package prompts_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gensvc "review.local/gen/support/prompts"
	"review.local/gen/types"
	"goa.design/goa-ai/runtime/agent/prompt"
)

func TestLocated(t *testing.T) {
	reg := prompt.NewRegistry(nil)
	require.NoError(t, gensvc.RegisterPrompts(reg))
	str := "Located Title"
	content, err := gensvc.RenderGreetPromptWithRegistry(context.Background(), reg, prompt.Scope{}, gensvc.GreetPromptArgs{
		Ticket: &types.Ticket{Title: &str},
	})
	require.NoError(t, err)
	assert.Equal(t, "Hi Located Title", content.Text)
}
`,
		},
		{
			name: "named_collections",
			configure: func() {
				Ticket := goaexpr.Root.UserType("Ticket")
				Prompt("greet", func() {
					Template("Hi")
					Args(func() {
						goadsl.Attribute("tickets", goadsl.ArrayOf(Ticket))
						goadsl.Attribute("by_id", goadsl.MapOf(goadsl.String, Ticket))
					})
				})
			},
			check: `package prompts_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gensvc "review.local/gen/support/prompts"
	"goa.design/goa-ai/runtime/agent/prompt"
)

func TestNamedCollections(t *testing.T) {
	reg := prompt.NewRegistry(nil)
	require.NoError(t, gensvc.RegisterPrompts(reg))
	content, err := gensvc.RenderGreetPromptWithRegistry(context.Background(), reg, prompt.Scope{}, gensvc.GreetPromptArgs{
		Tickets: []*gensvc.Ticket{{Title: "T1"}},
		ByID:    map[string]*gensvc.Ticket{"key": {Title: "T2"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "Hi", content.Text)
}
`,
		},
		{
			name: "union",
			configure: func() {
				Prompt("greet", func() {
					Template("Hi")
					Args(func() {
						goadsl.OneOf("value", func() {
							goadsl.Attribute("number", goadsl.Int)
							goadsl.Attribute("text", goadsl.String)
						})
					})
				})
			},
			check: `package prompts_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gensvc "review.local/gen/support/prompts"
	"goa.design/goa-ai/runtime/agent/prompt"
)

func TestUnion(t *testing.T) {
	reg := prompt.NewRegistry(nil)
	require.NoError(t, gensvc.RegisterPrompts(reg))
	u := gensvc.NewValueNumber(42)
	content, err := gensvc.RenderGreetPromptWithRegistry(context.Background(), reg, prompt.Scope{}, gensvc.GreetPromptArgs{
		Value: u,
	})
	require.NoError(t, err)
	assert.Equal(t, "Hi", content.Text)
}
`,
		},
		{
			name: "package_import_collision",
			configure: func() {
				Prompt("context", func() {
					Template("Hi")
				})
			},
			check: `package prompts_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gensvc "review.local/gen/support/prompts"
	"goa.design/goa-ai/runtime/agent/prompt"
)

func TestPackageImportCollision(t *testing.T) {
	reg := prompt.NewRegistry(nil)
	require.NoError(t, gensvc.RegisterPrompts(reg))
	content, err := gensvc.RenderContextPromptWithRegistry(context.Background(), reg, prompt.Scope{})
	require.NoError(t, err)
	assert.Equal(t, "Hi", content.Text)
}
`,
		},
		{
			name: "empty_args",
			configure: func() {
				Prompt("greet", func() {
					Template("Hi")
					Args(func() {})
				})
			},
			check: `package prompts_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gensvc "review.local/gen/support/prompts"
	"goa.design/goa-ai/runtime/agent/prompt"
)

func TestEmptyArgs(t *testing.T) {
	spec := gensvc.PromptSpecGreet()
	require.NotNil(t, spec.ArgsType)
	assert.Equal(t, reflect.TypeFor[struct{}](), spec.ArgsType)

	reg := prompt.NewRegistry(prompt.NewInMemoryStore())
	require.NoError(t, gensvc.RegisterPrompts(reg))

	scope := prompt.Scope{}
	badOverride := "Hi {{ .Extra }}"
	err := reg.SetOverride(context.Background(), gensvc.PromptGreetID, scope, badOverride, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, prompt.ErrTemplateFieldNotFound), "expected ErrTemplateFieldNotFound, got: %v", err)
}
`,
		},
		{
			name: "same_go_name",
			configure: func() {
				Prompt("greet-one", func() { Template("Hi") })
				Prompt("greet_one", func() { Template("Hi") })
			},
			expectDSLErr: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eval.Reset()
			goaexpr.Root = new(goaexpr.RootExpr)
			goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
			require.NoError(t, eval.Register(goaexpr.Root))
			require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))
			agentsExpr.Root = &agentsExpr.RootExpr{}
			require.NoError(t, eval.Register(agentsExpr.Root))

			design := func() {
				goadsl.API("review", func() {})
				goadsl.Type("Ticket", func() {
					goadsl.Attribute("title", goadsl.String)
					goadsl.Required("title")
					if c.name == "located" {
						goadsl.Meta("struct:pkg:path", "types")
					}
				})
				goadsl.Service("support", c.configure)
			}

			ok := eval.Execute(design, nil)
			if c.expectDSLErr {
				err := eval.RunDSL()
				require.Error(t, err)
				assert.Contains(t, err.Error(), "duplicates")
				return
			}
			require.True(t, ok, eval.Context.Error())
			require.NoError(t, eval.RunDSL())

			files, err := codegen.BuildFilesForTest("review.local/gen", []eval.Root{goaexpr.Root, agentsExpr.Root}, false)
			require.NoError(t, err)

			dir := t.TempDir()
			goMod := "module review.local\n\ngo 1.24\n\nrequire (\n\tgoa.design/goa-ai v0.0.0\n\tgithub.com/stretchr/testify v1.10.0\n)\n\nreplace goa.design/goa-ai => " + filepath.ToSlash(repoRoot) + "\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o600))

			for _, f := range files {
				_, err := f.Render(dir)
				require.NoError(t, err)
			}

			if c.name == "located" {
				typesCode := `package types

type Ticket struct {
	Title *string
}
`
				require.NoError(t, os.MkdirAll(filepath.Join(dir, "gen/types"), 0o750))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "gen/types/types.go"), []byte(typesCode), 0o600))
			}

			if c.check != "" {
				testDir := filepath.Join(dir, "gen/support/prompts")
				require.NoError(t, os.WriteFile(filepath.Join(testDir, "prompts_variation_test.go"), []byte(c.check), 0o600))

				ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "go", "test", "-mod=mod", "-race", "-count=1", "-v", "./gen/support/prompts")
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "GOWORK=off")
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, string(out))
			}
		})
	}
}

// TestAgentPackageNameCollisions verifies agent package generation and execution for agents
// named "bot", "prompt_specs", and "system_prompt_args".
func TestAgentPackageNameCollisions(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	for _, name := range []string{"bot", "prompt_specs", "system_prompt_args"} {
		t.Run(name, func(t *testing.T) {
			eval.Reset()
			goaexpr.Root = new(goaexpr.RootExpr)
			goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
			require.NoError(t, eval.Register(goaexpr.Root))
			require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))
			agentsExpr.Root = &agentsExpr.RootExpr{}
			require.NoError(t, eval.Register(agentsExpr.Root))

			design := func() {
				goadsl.API("review", func() {})
				goadsl.Service("support", func() {
					Agent(name, "review agent", func() {
						SystemPrompt("Hi {{.Name}}", func() {
							Args(func() {
								goadsl.Attribute("name", goadsl.String)
								goadsl.Required("name")
							})
						})
					})
				})
			}

			ok := eval.Execute(design, nil)
			require.True(t, ok, eval.Context.Error())
			require.NoError(t, eval.RunDSL())

			files, err := codegen.BuildFilesForTest("review.local/gen", []eval.Root{goaexpr.Root, agentsExpr.Root}, false)
			require.NoError(t, err)

			dir := t.TempDir()
			goMod := "module review.local\n\ngo 1.24\n\nrequire (\n\tgoa.design/goa-ai v0.0.0\n\tgithub.com/stretchr/testify v1.10.0\n)\n\nreplace goa.design/goa-ai => " + filepath.ToSlash(repoRoot) + "\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o600))

			for _, f := range files {
				_, err := f.Render(dir)
				require.NoError(t, err)
			}

			if name == "bot" {
				check := `package bot_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genbot "review.local/gen/support/agents/bot"
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

type reviewContext struct {
	planner.PlannerContext
	reg *prompt.Registry
}

func (p reviewContext) RenderPrompt(ctx context.Context, id prompt.Ident, data any) (*prompt.PromptContent, error) {
	return p.reg.Render(ctx, id, prompt.Scope{}, data)
}

func TestBotRegistrationAndExecution(t *testing.T) {
	ctx := context.Background()

	// 1. Direct registry render
	reg := prompt.NewRegistry(nil)
	require.NoError(t, genbot.RegisterPrompts(reg))
	out, err := genbot.RenderSystemPrompt(ctx, reviewContext{reg: reg}, genbot.SystemPromptArgs{Name: "Alice"})
	require.NoError(t, err)
	assert.Equal(t, "Hi Alice", out.Text)

	// 2. NewBotAgentRegistration carries PromptSpecs and rt.RegisterAgent registers them
	cfg := genbot.BotAgentConfig{Planner: &mockPlanner{}}
	promptStore := prompt.NewInMemoryStore()
	rt := runtime.New(storageinmem.New(), runtime.WithPromptStore(promptStore))
	agentReg, err := genbot.NewBotAgentRegistration(rt, cfg)
	require.NoError(t, err)
	require.Len(t, agentReg.PromptSpecs, 1)
	require.NoError(t, rt.RegisterAgent(ctx, agentReg))

	outRt, err := genbot.RenderSystemPrompt(ctx, reviewContext{reg: rt.PromptRegistry}, genbot.SystemPromptArgs{Name: "Alice"})
	require.NoError(t, err)
	assert.Equal(t, "Hi Alice", outRt.Text)

	// 3. RegisterBotAgent registers PromptSpecs
	promptStore2 := prompt.NewInMemoryStore()
	rt2 := runtime.New(storageinmem.New(), runtime.WithPromptStore(promptStore2))
	require.NoError(t, genbot.RegisterBotAgent(ctx, rt2, cfg))
	outRt2, err := genbot.RenderSystemPrompt(ctx, reviewContext{reg: rt2.PromptRegistry}, genbot.SystemPromptArgs{Name: "Alice"})
	require.NoError(t, err)
	assert.Equal(t, "Hi Alice", outRt2.Text)

	// 4. Conflicting pre-registered prompt causes RegisterBotAgent / rt.RegisterAgent to return error
	promptStore3 := prompt.NewInMemoryStore()
	rt3 := runtime.New(storageinmem.New(), runtime.WithPromptStore(promptStore3))
	spec := genbot.PromptSpecSystem()
	spec.Template = "CONFLICT"
	spec.Version = "old_version"
	require.NoError(t, rt3.PromptRegistry.Register(spec))
	errConflict := genbot.RegisterBotAgent(ctx, rt3, cfg)
	require.Error(t, errConflict)
	assert.True(t, errors.Is(errConflict, prompt.ErrDuplicatePromptSpec), "expected ErrDuplicatePromptSpec, got: %v", errConflict)
}
`
				testDir := filepath.Join(dir, "gen/support/agents/bot")
				require.NoError(t, os.WriteFile(filepath.Join(testDir, "bot_test.go"), []byte(check), 0o600))
			} else {
				check := `package ` + name + `_test

import (
	"testing"
)

func TestCompiles(t *testing.T) {}
`
				testDir := filepath.Join(dir, "gen/support/agents/"+name)
				require.NoError(t, os.WriteFile(filepath.Join(testDir, "compile_test.go"), []byte(check), 0o600))
			}

			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "go", "test", "-mod=mod", "-race", "-count=1", "-v", "./gen/support/agents/"+name) //nolint:gosec // Fixed test agent names compile in temporary test directory
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GOWORK=off")
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, string(out))
		})
	}
}
