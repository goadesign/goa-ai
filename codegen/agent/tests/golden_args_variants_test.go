package tests

import (
	"testing"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

func TestGolden_Args_Primitive(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ArgsPrimitive())
	types := fileContent(t, files, "gen/alpha/toolsets/ops/types.go")
	codecs := fileContent(t, files, "gen/alpha/toolsets/ops/codecs.go")
	specs := fileContent(t, files, "gen/alpha/toolsets/ops/specs.go")
	assertGoldenGo(t, "args_primitive", "types.go.golden", types)
	assertGoldenGo(t, "args_primitive", "codecs.go.golden", codecs)
	assertGoldenGo(t, "args_primitive", "specs.go.golden", specs)
}

func TestGolden_Args_InlineObject(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ArgsInlineObject())
	types := fileContent(t, files, "gen/alpha/toolsets/math/types.go")
	codecs := fileContent(t, files, "gen/alpha/toolsets/math/codecs.go")
	specs := fileContent(t, files, "gen/alpha/toolsets/math/specs.go")
	assertGoldenGo(t, "args_inline", "types.go.golden", types)
	assertGoldenGo(t, "args_inline", "codecs.go.golden", codecs)
	assertGoldenGo(t, "args_inline", "specs.go.golden", specs)
}

func TestGolden_Args_UserType(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ArgsUserType())
	codecs := fileContent(t, files, "gen/alpha/toolsets/docs/codecs.go")
	specs := fileContent(t, files, "gen/alpha/toolsets/docs/specs.go")
	assertGoldenGo(t, "args_usertype", "codecs.go.golden", codecs)
	assertGoldenGo(t, "args_usertype", "specs.go.golden", specs)
}

func TestGolden_Args_UnionSumTypes(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ArgsUnionSumTypes())
	types := fileContent(t, files, "gen/alpha/toolsets/union/types.go")
	unions := fileContent(t, files, "gen/alpha/toolsets/union/unions.go")
	codecs := fileContent(t, files, "gen/alpha/toolsets/union/codecs.go")
	specs := fileContent(t, files, "gen/alpha/toolsets/union/specs.go")
	assertGoldenGo(t, "args_union_sum_types", "types.go.golden", types)
	assertGoldenGo(t, "args_union_sum_types", "unions.go.golden", unions)
	assertGoldenGo(t, "args_union_sum_types", "codecs.go.golden", codecs)
	assertGoldenGo(t, "args_union_sum_types", "specs.go.golden", specs)
}
