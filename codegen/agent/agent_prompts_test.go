package codegen_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	agentgen "goa.design/goa-ai/codegen/agent"
	"goa.design/goa-ai/codegen/testhelpers"
	. "goa.design/goa-ai/dsl"
	gcodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	goadsl "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// TestReviewPromptArtifacts verifies that all 7 review artifact cases
// (union, collision, nested_collision, prompt_names, located, recursive_map, args_type_name)
// generate valid Go code that compiles cleanly.
func TestReviewPromptArtifacts(t *testing.T) {
	repo, err := filepath.Abs("../..")
	require.NoError(t, err)

	for _, name := range []string{"union", "collision", "nested_collision", "prompt_names", "located", "recursive_map", "args_type_name"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			design := func() {
				goadsl.API("review", func() {})
				var arg goaexpr.UserType
				switch name {
				case "union":
					arg = goadsl.Type("Ticket", func() {
						goadsl.OneOf("value", func() {
							goadsl.Attribute("text", goadsl.String)
							goadsl.Attribute("number", goadsl.Int)
						})
					})
				case "collision":
					arg = goadsl.Type("PromptSpecs", func() {
						goadsl.Attribute("id", goadsl.String)
					})
				case "nested_collision":
					nested := goadsl.Type("PromptSpecs", func() {
						goadsl.Attribute("id", goadsl.String)
					})
					arg = goadsl.Type("Ticket", func() {
						goadsl.Attribute("payload", nested)
					})
				case "located":
					arg = goadsl.Type("Ticket", func() {
						goadsl.Meta("struct:pkg:path", "types")
						goadsl.Attribute("id", goadsl.String)
					})
				case "recursive_map":
					arg = goadsl.Type("Ticket", func() {
						goadsl.Attribute("children", goadsl.ArrayOf("Ticket"))
						goadsl.Attribute("by_id", goadsl.MapOf(goadsl.String, "Ticket"))
					})
				case "args_type_name":
					arg = goadsl.Type("TriagePromptArgs", func() {
						goadsl.Attribute("id", goadsl.String)
					})
				default:
					arg = goadsl.Type("Ticket", func() {
						goadsl.Attribute("id", goadsl.String)
					})
				}
				goadsl.Service("support", func() {
					Agent("helpdesk", "help", func() {
						Prompt("triage", func() {
							Template("ok")
							Args(arg)
						})
						if name == "prompt_names" {
							Prompt("triage_prompt", func() { Template("ok") })
							Prompt("triagePrompt", func() { Template("ok") })
						}
					})
				})
			}

			if name == "prompt_names" {
				testhelpers.SetupEvalRoots(t)
				ok := eval.Execute(design, nil)
				require.True(t, ok, eval.Context.Error())
				err := eval.RunDSL()
				require.Error(t, err)
				require.Contains(t, err.Error(), "duplicates prompt")
				return
			}

			files := testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", design)
			for _, file := range files {
				if !strings.HasSuffix(file.Path, "prompts.go") {
					continue
				}
				_, err := file.Render(root)
				require.NoError(t, err)
			}

			if name == "located" {
				require.NoError(t, os.MkdirAll(filepath.Join(root, "gen/types"), 0o750))
				require.NoError(t, os.WriteFile(filepath.Join(root, "gen/types/types.go"), []byte("package types\ntype Ticket struct { ID *string }\n"), 0o600))
			}

			gomod := fmt.Sprintf("module generated.local\n\ngo 1.24\n\nrequire goa.design/goa-ai v0.0.0\nreplace goa.design/goa-ai => %s\n", filepath.ToSlash(repo))
			require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte(gomod), 0o600))

			testFile := `package helpdesk_test

import (
	_ "generated.local/gen/support/agents/helpdesk"
	"testing"
)

func TestCompiles(t *testing.T) {}
`
			testDir := filepath.Join(root, "gen/support/agents/helpdesk")
			require.NoError(t, os.MkdirAll(testDir, 0o750))
			require.NoError(t, os.WriteFile(filepath.Join(testDir, "compile_test.go"), []byte(testFile), 0o600))

			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "go", "test", "-mod=mod", "./...")
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "CI=true", "GOWORK=off")
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, string(output))
		})
	}
}

// TestReviewBC70 verifies that all 10 review cases compile and pass their runtime contracts
// when generated into complete service and agent packages.
func TestReviewBC70(t *testing.T) {
	repo, err := filepath.Abs("../..")
	require.NoError(t, err)

	cases := []string{
		"union",
		"root_collision",
		"nested_collision",
		"root_located",
		"root_located_method",
		"recursive",
		"custom_names",
		"external_alias",
		"agent_config_collision",
		"noargs",
	}

	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), name)
			require.NoError(t, os.MkdirAll(root, 0o750))
			roots := testhelpers.SetupEvalRoots(t)
			design := func() {
				goadsl.API("review", func() {})
				var arg goaexpr.UserType
				switch name {
				case "union":
					arg = goadsl.Type("Ticket", func() {
						goadsl.OneOf("value", func() {
							goadsl.Attribute("text", goadsl.String)
							goadsl.Attribute("number", goadsl.Int)
						})
					})
				case "root_collision":
					arg = goadsl.Type("PromptSpecs", func() {
						goadsl.Attribute("id", goadsl.String)
					})
				case "nested_collision":
					nested := goadsl.Type("PromptSpecs", func() {
						goadsl.Attribute("id", goadsl.String)
					})
					arg = goadsl.Type("Ticket", func() {
						goadsl.Attribute("payload", nested)
					})
				case "root_located", "root_located_method":
					arg = goadsl.Type("Ticket", func() {
						goadsl.Meta("struct:pkg:path", "types")
						goadsl.Attribute("id", goadsl.String)
					})
				case "recursive":
					arg = goadsl.Type("Ticket", func() {
						goadsl.Attribute("id", goadsl.String)
						goadsl.Attribute("children", goadsl.ArrayOf("Ticket"))
						goadsl.Attribute("by_id", goadsl.MapOf(goadsl.String, "Ticket"))
					})
				case "custom_names":
					inner := goadsl.Type("Thing", func() {
						goadsl.Meta("struct:type:name", "RenamedThing")
						goadsl.Attribute("id", goadsl.String, func() {
							goadsl.Meta("struct:field:name", "CustomID")
						})
					})
					arg = goadsl.Type("Ticket", func() {
						goadsl.Attribute("child", inner)
						goadsl.Attribute("when", goadsl.String, func() {
							goadsl.Meta("struct:field:type", "time.Time", "time")
						})
					})
				case "external_alias":
					arg = goadsl.Type("Ticket", func() {
						goadsl.Attribute("id", goadsl.String, func() {
							goadsl.Meta("struct:field:type", "context.Token", "qa.local/custom/context", "context")
						})
					})
				case "agent_config_collision":
					inner := goadsl.Type("HelpdeskAgentConfig", func() {
						goadsl.Attribute("id", goadsl.String)
					})
					arg = goadsl.Type("Ticket", func() {
						goadsl.Attribute("child", inner)
					})
				}
				goadsl.Service("support", func() {
					if name == "root_located_method" {
						goadsl.Method("read", func() {
							goadsl.Payload(arg)
							goadsl.Result(arg)
						})
					}
					configure := func() {
						Prompt("triage", func() {
							switch name {
							case "noargs":
								Template("{{if .}}present{{else}}absent{{end}}")
							case "custom_names":
								Template("{{.Child.CustomID}}")
								Args(arg)
							default:
								Template("ok")
								Args(arg)
							}
						})
					}
					configure()
					Agent("helpdesk", "help", configure)
				})
			}

			require.True(t, eval.Execute(design, nil), eval.Context.Error())
			require.NoError(t, eval.RunDSL())
			require.NoError(t, agentgen.Prepare("qa.local/gen", roots))
			generation, err := gcodegen.NewGeneration("qa.local/gen", roots)
			require.NoError(t, err)
			servicePlan, err := service.NewPlan(goaexpr.Root, generation, goaexpr.NewExampleGenerator(goaexpr.Root.API.RandomizerFactory))
			require.NoError(t, err)
			plan, err := agentgen.NewPlan(generation, servicePlan)
			require.NoError(t, err)
			require.NoError(t, generation.Freeze())
			require.NoError(t, servicePlan.Link())
			require.NoError(t, plan.Link())
			serviceFiles, err := service.Files(servicePlan)
			require.NoError(t, err)
			files, err := plan.Files(serviceFiles)
			require.NoError(t, err)
			for _, file := range files {
				_, err := file.Render(root)
				require.NoErrorf(t, err, "render %s", file.Path)
			}
			gomod := fmt.Sprintf("module qa.local\n\ngo 1.27.3\n\nrequire goa.design/goa-ai v0.0.0\nreplace goa.design/goa-ai => %s\n", filepath.ToSlash(repo))
			require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte(gomod), 0o600))
			if name == "external_alias" {
				require.NoError(t, os.MkdirAll(filepath.Join(root, "custom/context"), 0o750))
				require.NoError(t, os.WriteFile(filepath.Join(root, "custom/context/types.go"), []byte("package context\ntype Token string\n"), 0o600))
			}
			if name == "root_located_method" {
				matches, err := filepath.Glob(filepath.Join(root, "gen/types/*.go"))
				require.NoError(t, err)
				require.NotEmpty(t, matches)
			}
			if name == "custom_names" {
				require.NoError(t, os.WriteFile(filepath.Join(root, "gen/support/prompts/typed_test.go"), []byte(reviewCustomTest), 0o600))
			}
			if name == "noargs" {
				require.NoError(t, os.WriteFile(filepath.Join(root, "gen/support/agents/helpdesk/observed_test.go"), []byte(reviewNoArgsTest), 0o600))
			}

			ctx, cancel := context.WithTimeout(t.Context(), 75*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "go", "test", "-mod=mod", "-count=1", "-v", "./...")
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "CI=true", "GOWORK=off")
			var out, stderr bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &stderr
			runErr := cmd.Run()
			require.NoError(t, runErr, "inner compile failed\nstdout:\n%s\nstderr:\n%s", out.String(), stderr.String())
		})
	}
}

const reviewCustomTest = `package prompts_test
import("context";"testing";"time"; gen "qa.local/gen/support/agents/helpdesk";svc "qa.local/gen/support/prompts";"goa.design/goa-ai/runtime/agent/planner";"goa.design/goa-ai/runtime/agent/prompt")
type pc struct {planner.PlannerContext;reg *prompt.Registry}
func (p pc) RenderPrompt(c context.Context,id prompt.Ident,data any)(*prompt.PromptContent,error){return p.reg.Render(c,id,prompt.Scope{},data)}
func TestCustomTypedHelpers(t *testing.T){
 reg:=prompt.NewRegistry(nil);if e:=gen.RegisterPrompts(reg);e!=nil{t.Fatal(e)};if e:=svc.RegisterPrompts(reg);e!=nil{t.Fatal(e)}
 id:="Custom-ID";now:=time.Unix(0,0)
 a:=gen.TriagePromptArgs{Child:&gen.Thing{CustomID:&id},When:&now};s:=svc.TriagePromptArgs{Child:&svc.Thing{CustomID:&id},When:&now}
 for _,render:=range []func()(*prompt.PromptContent,error){func()(*prompt.PromptContent,error){return gen.RenderTriagePromptWithRegistry(context.Background(),reg,prompt.Scope{},a)},func()(*prompt.PromptContent,error){return gen.RenderTriagePrompt(context.Background(),pc{reg:reg},a)},func()(*prompt.PromptContent,error){return svc.RenderTriagePromptWithRegistry(context.Background(),reg,prompt.Scope{},s)},func()(*prompt.PromptContent,error){return svc.RenderTriagePrompt(context.Background(),pc{reg:reg},s)}}{out,e:=render();if e!=nil{t.Fatal(e)};if out.Text!=id{t.Fatalf("actual typed value lost: %q",out.Text)}}
}
`

const reviewNoArgsTest = `package helpdesk_test
import (
 "context"
 "errors"
 "reflect"
 "testing"
 gen "qa.local/gen/support/agents/helpdesk"
 svc "qa.local/gen/support/prompts"
 "goa.design/goa-ai/runtime/agent/planner"
 "goa.design/goa-ai/runtime/agent/prompt"
 "goa.design/goa-ai/runtime/agent/runtime"
 inmem "goa.design/goa-ai/runtime/agent/storage/inmem"
)
type mock struct{}
func (*mock) PlanStart(context.Context,*planner.PlanInput)(*planner.PlanResult,error){return &planner.PlanResult{},nil}
func (*mock) PlanResume(context.Context,*planner.PlanResumeInput)(*planner.PlanResult,error){return &planner.PlanResult{},nil}
type pc struct{planner.PlannerContext; reg *prompt.Registry}
func (p pc) RenderPrompt(c context.Context,id prompt.Ident,data any)(*prompt.PromptContent,error){return p.reg.Render(c,id,prompt.Scope{},data)}
func TestContract(t *testing.T){
 c:=context.Background();reg:=prompt.NewRegistry(prompt.NewInMemoryStore())
 if err:=gen.RegisterPrompts(reg);err!=nil{t.Fatal(err)}
 if err:=svc.RegisterPrompts(reg);err!=nil{t.Fatal(err)}
 if gen.PromptSpecTriage().ArgsType!=reflect.TypeFor[struct{}](){t.Fatal("noargs lost closed type")}
 if err:=reg.SetOverride(c,gen.PromptTriageID,prompt.Scope{},"{{.Unexpected}}",nil);!errors.Is(err,prompt.ErrTemplateFieldNotFound){t.Fatalf("override unexpectedly accepted: %v",err)}
 a,err:=gen.RenderTriagePromptWithRegistry(c,reg,prompt.Scope{});if err!=nil{t.Fatal(err)}
 b,err:=reg.Render(c,gen.PromptTriageID,prompt.Scope{},struct{}{});if err!=nil{t.Fatal(err)}
 p,err:=gen.RenderTriagePrompt(c,pc{reg:reg});if err!=nil{t.Fatal(err)}
 s,err:=svc.RenderTriagePromptWithRegistry(c,reg,prompt.Scope{});if err!=nil{t.Fatal(err)}
 t.Logf("noarg declared=%v helper=%q planner=%q service=%q typed_registry=%q",gen.PromptSpecTriage().ArgsType,a.Text,p.Text,s.Text,b.Text)
 if a.Text!=b.Text {t.Errorf("helper passes a different data value than its declared zero-argument struct contract: helper=%q declared-type=%q",a.Text,b.Text)}
 cfg:=gen.HelpdeskAgentConfig{Planner:&mock{}}
 for _,wrapper:=range []bool{false,true}{
  rt:=runtime.New(inmem.New(),runtime.WithPromptStore(prompt.NewInMemoryStore()))
  var e error
  if wrapper {e=gen.RegisterHelpdeskAgent(c,rt,cfg)} else {r,err:=gen.NewHelpdeskAgentRegistration(rt,cfg);if err!=nil{t.Fatal(err)};if len(r.PromptSpecs)!=1{t.Fatal("constructor dropped prompt specs")};e=rt.RegisterAgent(c,r)}
  if e!=nil{t.Fatalf("registration wrapper=%v: %v",wrapper,e)}
  if _,e=gen.RenderTriagePromptWithRegistry(c,rt.PromptRegistry,prompt.Scope{});e!=nil{t.Fatal(e)}
  if e=gen.RegisterHelpdeskAgent(c,rt,cfg);!errors.Is(e,prompt.ErrDuplicatePromptSpec){t.Fatalf("duplicate wrapper=%v error=%v",wrapper,e)}
  t.Logf("registration wrapper=%v and duplicate rejection passed",wrapper)
 }
}
`
