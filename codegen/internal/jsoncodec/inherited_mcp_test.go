// This test loads only DSL in a separate module and uses the actual plugin
// command. A shared parent outside the MCP method selects its direct child.
// The child is nonrecursive because this MCP generator expands schemas inline.
// TestInheritedStandaloneLayoutsAcrossRoots covers recursive original values.
package jsoncodec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDSLInheritedMCPChild(t *testing.T) {
	root := compileModule(t, nil)
	module, err := filepath.Abs("../../..")
	require.NoError(t, err)
	runGo(t, root, "mod", "edit", "-require=goa.design/goa-ai@v0.0.0", "-replace=goa.design/goa-ai="+module)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "design"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "design/design.go"), []byte(inheritedMCPDesign), 0o600))
	runGo(t, root, "mod", "tidy")
	runGo(t, root, "run", "goa.design/goa/v3/cmd/goa", "gen", "codec.local/design")
	target := filepath.Join(root, "gen/mcp_values/internal/codec/inherited_test.go")
	require.NoError(t, os.WriteFile(target, []byte(inheritedMCPBehavior), 0o600))
	runGo(t, root, "mod", "tidy")
	runGo(t, root, "test", "-count=1", "./gen/...")
}

const inheritedMCPDesign = `package design
import (
    ai "goa.design/goa-ai/dsl"
    . "goa.design/goa/v3/dsl"
)
var _ = API("inherited", func() {})
var Child = Type("Child", func() {
    Attribute("value", String)
    Required("value")
})
var SharedRoot = Type("SharedRoot", func() {
    Meta("struct:pkg:path", "shared/types")
    Meta("type:generate:force")
    Attribute("child", Child)
})
var _ = Service("values", func() {
    ai.MCP("values", "1.0.0")
    JSONRPC(func() { POST("/mcp") })
    Method("exchange", func() {
        Payload(Child)
        Result(Child)
        ai.Tool("exchange", "Exchange the supplied value")
    })
})
`

const inheritedMCPBehavior = `package codec
import (
    "reflect"
    "testing"
    shared "codec.local/gen/shared/types"
)
var _ func(*shared.Child) ([]byte, error) = EncodeExchangePayload
var _ func([]byte) (*shared.Child, error) = DecodeExchangeResult
func TestDirectInheritedChild(t *testing.T) {
    input := &shared.Child{Value: "child"}
    data, err := EncodeExchangePayload(input)
    if err != nil { t.Fatal(err) }
    result, err := DecodeExchangeResult(data)
    if err != nil || !reflect.DeepEqual(input, result) {
        t.Fatalf("MCP child changed: %#v %v", result, err)
    }
    if _, err := DecodeExchangeResult([]byte("{\"value\":\"x\",\"extra\":true}")); err == nil {
        t.Fatal("MCP codec accepted an unknown field")
    }
}
`
