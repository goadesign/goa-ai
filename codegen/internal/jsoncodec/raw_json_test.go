// These checks exercise explicit raw JSON fields through the ordinary complete
// value codec. Future fields and exact numbers survive; invalid authored bytes
// and unknown outer fields fail before a caller receives a typed value.
package jsoncodec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/dsl"
)

func TestGeneratedRawJSONValueCodec(t *testing.T) {
	files, err := generate(t, func() {
		located("Packet", func() {
			dsl.Attribute("data", dsl.Any, "Open JSON kept as authored bytes", func() {
				dsl.Meta("struct:field:type", "json.RawMessage", "encoding/json")
			})
			dsl.Required("data")
		})
	})
	require.NoError(t, err)
	root := compileModule(t, files)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/types/raw_json_test.go"), []byte(rawJSONValueTests), 0o600)) // #nosec G703 -- root belongs to this generated-code check.
	runGo(t, root, "test", "-count=1", "./gen/...")
}

const rawJSONValueTests = `package types_test
import (
 "encoding/json"
 "testing"
 "github.com/stretchr/testify/assert"
 "github.com/stretchr/testify/require"
 gentypes "codec.local/gen/types"
)
func TestRawJSONRoundTrip(t *testing.T) {
 for _, raw := range []string{
  "null", "true", "9007199254740993", "1e9999999999",
  "[null,1]", "{\"future\":{\"exact\":9007199254740993,\"null\":null}}",
 } {
  value := &gentypes.Packet{Data:json.RawMessage(raw)}
  encoded,err := gentypes.EncodePacket(value)
  require.NoError(t,err,raw)
  decoded,err := gentypes.DecodePacket(encoded)
  require.NoError(t,err,raw)
  assert.Equal(t,raw,string(decoded.Data))
  assert.Equal(t,raw,string(value.Data))
 }
}
func TestRawJSONRejectsInvalidBytes(t *testing.T) {
 for _, raw := range []string{"", "{\"same\":1,\"same\":2}", "{} {}", "\"\\ud800\""} {
  encoded,err := gentypes.EncodePacket(&gentypes.Packet{Data:json.RawMessage(raw)})
  assert.Error(t,err,raw);assert.Nil(t,encoded)
 }
 for _, raw := range []string{
  "{}", "{\"data\":1,\"extra\":true}",
  "{\"data\":{\"same\":1,\"same\":2}}",
  "{\"data\":{}} {}",
 } {
  decoded,err := gentypes.DecodePacket([]byte(raw))
  assert.Error(t,err,raw);assert.Nil(t,decoded)
 }
}
`
