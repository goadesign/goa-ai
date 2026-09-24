// The descriptive tool schema artifact includes explicit native-image metadata
// while preserving the exact JSON bytes of unmarked evidence declarations.
package codegen_test

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	codegen "goa.design/goa-ai/codegen/agent"
	. "goa.design/goa-ai/dsl"
	agentsExpr "goa.design/goa-ai/expr/agent"
	goadsl "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

func TestNativeImageSchemaArtifactMarkerPreservesUnmarkedBytes(t *testing.T) {
	artifacts := make([][]byte, 0, 2)
	for _, marked := range []bool{false, true} {
		eval.Reset()
		goaexpr.Root = new(goaexpr.RootExpr)
		goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
		require.NoError(t, eval.Register(goaexpr.Root))
		require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))
		agentsExpr.Root = &agentsExpr.RootExpr{}
		require.NoError(t, eval.Register(agentsExpr.Root))
		design := func() {
			goadsl.API("images", func() {})
			source := goadsl.Type("RetainedImage", func() {
				goadsl.Attribute("id", goadsl.String, "Exact image.")
				goadsl.Required("id")
			})
			goadsl.Service("images", func() {
				Agent("reader", "Reads selected images.", func() {
					Use("pictures", func() {
						Tool("view", "View selected image.", func() {
							Args(source)
							Return(source)
							ServerData("fixture.image.v1", source, func() {
								AudienceEvidence()
								if marked {
									NativeImage()
								}
							})
						})
					})
				})
			})
		}
		require.True(t, eval.Execute(design, nil), eval.Context.Error())
		require.NoError(t, eval.RunDSL())
		files, err := codegen.BuildFilesForTest("example.com/images", []eval.Root{goaexpr.Root, agentsExpr.Root}, false)
		require.NoError(t, err)
		var artifact bytes.Buffer
		for _, file := range files {
			if filepath.ToSlash(file.Path) != "gen/images/agents/reader/specs/tool_schemas.json" {
				continue
			}
			for _, section := range file.SectionTemplates {
				require.NoError(t, section.Write(&artifact))
			}
		}
		require.NotEmpty(t, artifact.Bytes())
		artifacts = append(artifacts, bytes.Clone(artifact.Bytes()))
	}
	require.NotContains(t, string(artifacts[0]), "native_image")
	var withoutMarker bytes.Buffer
	markers := 0
	for _, line := range bytes.SplitAfter(artifacts[1], []byte("\n")) {
		if bytes.Contains(line, []byte(`"native_image": true`)) {
			markers++
			continue
		}
		withoutMarker.Write(line)
	}
	require.Equal(t, 1, markers)
	require.Equal(t, artifacts[0], withoutMarker.Bytes())
}
