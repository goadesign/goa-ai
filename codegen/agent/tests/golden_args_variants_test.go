package tests

import (
	"testing"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

func TestGolden_Args_Primitive(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ArgsPrimitive())
	types := fileContent(t, files, "gen/alpha/agents/scribe/specs/ops/types.go")
	codecs := fileContent(t, files, "gen/alpha/agents/scribe/specs/ops/codecs.go")
	specs := fileContent(t, files, "gen/alpha/agents/scribe/specs/ops/specs.go")
	assertGoldenGo(t, "args_primitive", "types.go.golden", types)
	assertGoldenGo(t, "args_primitive", "codecs.go.golden", codecs)
	assertGoldenGo(t, "args_primitive", "specs.go.golden", specs)
}

func TestGolden_Args_InlineObject(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ArgsInlineObject())
	types := fileContent(t, files, "gen/alpha/agents/scribe/specs/math/types.go")
	codecs := fileContent(t, files, "gen/alpha/agents/scribe/specs/math/codecs.go")
	specs := fileContent(t, files, "gen/alpha/agents/scribe/specs/math/specs.go")
	assertGoldenGo(t, "args_inline", "types.go.golden", types)
	assertGoldenGo(t, "args_inline", "codecs.go.golden", codecs)
	assertGoldenGo(t, "args_inline", "specs.go.golden", specs)
}

func TestGolden_Args_UserType(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ArgsUserType())
	codecs := fileContent(t, files, "gen/alpha/agents/scribe/specs/docs/codecs.go")
	specs := fileContent(t, files, "gen/alpha/agents/scribe/specs/docs/specs.go")
	assertGoldenGo(t, "args_usertype", "codecs.go.golden", codecs)
	assertGoldenGo(t, "args_usertype", "specs.go.golden", specs)
}
