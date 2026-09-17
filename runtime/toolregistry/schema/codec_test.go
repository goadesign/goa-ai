// Package schema tests codecs compiled from registry-provided JSON Schemas.
// Accepted values must survive encoding without rounding, and invalid model or
// provider JSON must fail before it enters the agent runtime.
package schema

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestCodecPreservesJSONAndValidatesBothDirections(t *testing.T) {
	t.Parallel()

	codec, err := Codec([]byte(`{"type":"object","required":["id"],"additionalProperties":false,"properties":{"id":{"type":"integer","const":9007199254740993}}}`))
	require.NoError(t, err)
	input := []byte(`{"id":9007199254740993}`)
	value, err := codec.FromJSON(input)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"id": json.Number("9007199254740993")}, value)
	input[1] = 'x'
	encoded, err := codec.ToJSON(value)
	require.NoError(t, err)
	assert.Equal(t, `{"id":9007199254740993}`, string(encoded))
	require.NoError(t, Validate([]byte(`{"const":9007199254740993}`), int64(9007199254740993), "result"))

	for _, invalid := range []string{
		`{"id":9007199254740992}`,
		`{"id":"9007199254740993"}`,
		`{"id":9007199254740993,"extra":true}`,
		`{}`,
		`null`,
		`{} {}`,
		``,
	} {
		_, err := codec.FromJSON([]byte(invalid))
		require.Error(t, err, invalid)
		_, err = codec.ToJSON(rawjson.Message(invalid))
		require.Error(t, err, invalid)
	}
}

func TestCodecRejectsMissingAndInvalidSchemas(t *testing.T) {
	t.Parallel()

	for _, invalid := range []string{"", "{", `{"type":"unknown"}`, `{} {}`} {
		_, err := Codec([]byte(invalid))
		require.Error(t, err, invalid)
	}
}
