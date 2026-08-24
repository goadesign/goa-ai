// This file verifies matching generated conversion functions are declared once
// and that required and optional nested values keep different functions.
package tests

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	agentcodegen "goa.design/goa-ai/codegen/agent"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	"goa.design/goa-ai/codegen/testhelpers"
	goacodegen "goa.design/goa/v3/codegen"
	goaservice "goa.design/goa/v3/codegen/service"
	goaeval "goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

func TestGeneratedTransformHelpersShareMatchingDeclarations(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, testscenarios.SharedTransformHelpers())
	transforms := fileContent(t, files, "gen/alpha/toolsets/helpers/transforms.go")
	codecs := fileContent(t, files, "gen/alpha/toolsets/helpers/codecs.go")

	require.Len(t, regexp.MustCompile(`func transformSharedChildToSharedChild\d*\(`).FindAllString(transforms, -1), 4)
	require.Len(t, regexp.MustCompile(`func decodeSharedChildTransportToSharedChild\d*\(`).FindAllString(codecs, -1), 2)
	require.Len(t, regexp.MustCompile(`func encodeSharedChildToSharedChildTransport\d*\(`).FindAllString(codecs, -1), 2)
	assertGoldenGo(t, "shared_transform_helpers", "transforms.go.golden", transforms)

	root := writeCompleteGeneratedModule(t, files)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "test", "-mod=mod", "./gen/alpha/toolsets/helpers/...")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off")
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	require.NoErrorf(t, command.Run(), "go test failed:\n%s", output.String())
}

// buildCompleteGeneratedFiles combines Goa service files with goa-ai tool
// files from the same generation plan.
func buildCompleteGeneratedFiles(t *testing.T, design func()) []*goacodegen.File {
	t.Helper()
	roots := testhelpers.SetupEvalRoots(t)
	require.True(t, goaeval.Execute(design, nil), goaeval.Context.Error())
	require.NoError(t, goaeval.RunDSL())
	const genpkg = "generated.local/gen"
	require.NoError(t, agentcodegen.Prepare(genpkg, roots))
	generation, err := goacodegen.NewGeneration(genpkg, roots)
	require.NoError(t, err)
	var root *goaexpr.RootExpr
	for _, candidate := range generation.Roots() {
		if current, ok := candidate.(*goaexpr.RootExpr); ok {
			root = current
			break
		}
	}
	require.NotNil(t, root)
	servicePlan, err := goaservice.NewPlan(root, generation, goaexpr.NewExampleGenerator(root.API.RandomizerFactory))
	require.NoError(t, err)
	agentPlan, err := agentcodegen.NewPlan(generation, servicePlan)
	require.NoError(t, err)
	require.NoError(t, generation.Freeze())
	require.NoError(t, servicePlan.Link())
	require.NoError(t, agentPlan.Link())
	files, err := goaservice.Files(servicePlan)
	require.NoError(t, err)
	files, err = agentPlan.Files(files)
	require.NoError(t, err)
	return files
}

// writeCompleteGeneratedModule renders files into a temporary module that uses
// the local goa-ai and Goa checkouts under test.
func writeCompleteGeneratedModule(t *testing.T, files []*goacodegen.File) string {
	t.Helper()
	root := t.TempDir()
	goaAI, err := filepath.Abs("../../..")
	require.NoError(t, err)
	goa := filepath.Clean(filepath.Join(goaAI, "..", "goa"))
	goMod := "module generated.local\n\ngo 1.24\n\n" +
		"require (\n\tgoa.design/goa-ai v0.0.0\n\tgoa.design/goa/v3 v3.0.0\n)\n\n" +
		"replace goa.design/goa-ai => " + filepath.ToSlash(goaAI) + "\n" +
		"replace goa.design/goa/v3 => " + filepath.ToSlash(goa) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte(goMod), 0o600))
	for _, file := range files {
		_, err := file.Render(root)
		require.NoErrorf(t, err, "render %s", file.Path)
	}
	return root
}
