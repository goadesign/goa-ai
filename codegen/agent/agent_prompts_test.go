package codegen_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/testhelpers"
	. "goa.design/goa-ai/dsl"
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
