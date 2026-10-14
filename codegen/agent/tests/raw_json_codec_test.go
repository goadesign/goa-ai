// These checks compile tool codecs with explicit raw JSON fields. The shared
// shape planner must preserve open JSON values without inventing a primitive.
package tests

import (
	"os/exec"
	"testing"

	agentdsl "goa.design/goa-ai/dsl"
	goadsl "goa.design/goa/v3/dsl"
)

func TestGeneratedToolRawJSONCodecs(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, func() {
		goadsl.API("sample", func() {})
		packet := goadsl.Type("Packet", func() {
			goadsl.Attribute("value", goadsl.Bytes, func() {
				goadsl.Meta("struct:field:type", "json.RawMessage", "encoding/json")
			})
			goadsl.Required("value")
		})
		goadsl.Service("sample", func() {
			agentdsl.Agent("reader", "Read records", func() {
				agentdsl.Use("records", func() {
					agentdsl.Tool("read", "Read a record", func() {
						agentdsl.Args(packet)
						agentdsl.Return(packet)
					})
				})
			})
		})
	})
	root := writeCompleteGeneratedModule(t, files)
	writeGeneratedPackageTest(t, root, "gen/sample/toolsets/records/raw_json_test.go", rawToolJSONTests)
	runGeneratedGoTestCommand(t, root, exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "./gen/sample/toolsets/records"))
}

const rawToolJSONTests = `package records

import (
 "testing"

 "github.com/stretchr/testify/assert"
 "github.com/stretchr/testify/require"
 "goa.design/goa-ai/runtime/agent/tools"
)

func TestRawValuesPreserveTheirJSON(t *testing.T) {
 spec := SpecRead()
 for _, codec := range []tools.JSONCodec[any]{spec.Payload.Codec, spec.ExecutionPayloadCodec, spec.Result.Codec} {
  for _, raw := range []string{"null", "true", "9007199254740993", "1e9999999999", "[]", "{}", "[null,9007199254740993]"} {
   wire := "{\"value\":" + raw + "}"
   value, err := codec.FromJSON([]byte(wire))
   require.NoError(t, err, wire)
   encoded, err := codec.ToJSON(value)
   require.NoError(t, err, wire)
   assert.Equal(t, wire, string(encoded))
  }
  for _, invalid := range []string{"{}", "{\"value\":{}} {}", "{\"value\":{\"x\":1,\"x\":2}}", "{\"value\":\"\\ud800\"}", "{\"value\":true,\"unknown\":1}"} {
   _, err := codec.FromJSON([]byte(invalid))
   assert.Error(t, err, invalid)
  }
 }
}
`
