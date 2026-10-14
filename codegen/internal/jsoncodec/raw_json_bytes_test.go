// These checks compile raw JSON byte fields and distinct union branches through
// the ordinary complete-value generator. JSON null remains a selected raw value.
package jsoncodec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/dsl"
)

func TestGeneratedRawJSONByteUnions(t *testing.T) {
	files, err := generate(t, func() {
		raw := dsl.Type("RawBytes", dsl.Bytes, func() {
			dsl.Meta("struct:pkg:path", "types")
			dsl.Meta("type:generate:force")
			dsl.Meta("struct:field:type", "stdjson.RawMessage", "encoding/json", "stdjson")
		})
		packet := located("Packet", func() {
			dsl.OneOf("choice", func() {
				dsl.TypeName("RawChoice")
				dsl.Field(1, "first", dsl.Bytes, "First raw JSON value", func() {
					dsl.Meta("struct:field:type", "json.RawMessage", "encoding/json")
				})
				dsl.Field(2, "second", dsl.Bytes, "Second raw JSON value", func() {
					dsl.Meta("struct:field:type", "wirejson.RawMessage", "encoding/json", "wirejson")
				})
				dsl.Field(3, "named", raw, "Named raw JSON value")
				dsl.Field(4, "plain", dsl.Bytes, "Ordinary bytes")
			})
			dsl.Required("choice")
		})
		located("Collection", func() {
			dsl.Attribute("items", dsl.ArrayOf(packet), "Ordered values")
			dsl.Attribute("named", dsl.MapOf(dsl.String, packet), "Values by name")
			dsl.Required("items", "named")
		})
	})
	require.NoError(t, err)
	root := compileModule(t, files)
	source, err := os.ReadFile(filepath.Join("testdata", "raw_json_bytes_test.go"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/types/raw_json_bytes_test.go"), source, 0o600)) // #nosec G703 -- compileModule creates a fresh test directory; the filename is fixed.
	runGo(t, root, "test", "-count=1", "./gen/...")
}
