package types_test

import (
	"encoding/json"
	"strings"
	"testing"

	gentypes "codec.local/gen/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRawJSONByteBranches(t *testing.T) {
	for _, raw := range []string{"null", "true", "9007199254740993123456789", "1e9999999999", `[]`, `{}`, `{"nested":[null,9007199254740993123456789]}`, `"` + strings.Repeat("x", 800000) + `"`} {
		for _, branch := range []string{"first", "second", "named"} {
			wire := []byte(`{"choice":{"type":"` + branch + `","value":` + raw + `}}`)
			original, err := gentypes.DecodePacket(wire)
			require.NoError(t, err)
			encoded, err := gentypes.EncodePacket(original)
			require.NoError(t, err)
			assert.Equal(t, wire, encoded)
			collection := &gentypes.Collection{Items: []*gentypes.Packet{original}, Named: map[string]*gentypes.Packet{"item": original}}
			encoded, err = gentypes.EncodeCollection(collection)
			require.NoError(t, err)
			restored, err := gentypes.DecodeCollection(encoded)
			require.NoError(t, err)
			assert.Equal(t, collection, restored)
			ordinary, err := json.Marshal(collection)
			require.NoError(t, err)
			var decoded gentypes.Collection
			require.NoError(t, json.Unmarshal(ordinary, &decoded))
			assert.Equal(t, collection, &decoded)
		}
		encoded, err := gentypes.EncodeRawBytes(gentypes.RawBytes(raw))
		require.NoError(t, err)
		assert.Equal(t, raw, string(encoded))
		restored, err := gentypes.DecodeRawBytes(encoded)
		require.NoError(t, err)
		assert.Equal(t, raw, string(restored))
	}
}

func TestRawJSONByteInvalidValues(t *testing.T) {
	for _, raw := range []string{"", `{} {}`, `{"x":1,"x":2}`, `"\ud800"`} {
		original := &gentypes.Packet{Choice: gentypes.NewRawChoiceNamed(gentypes.RawBytes(raw))}
		_, err := gentypes.EncodePacket(original)
		assert.Error(t, err, raw)
	}
	for _, wire := range []string{`{"choice":{"type":"named"}}`, `{"choice":{"type":"named","value":{"x":1,"x":2}}}`, `{"choice":{"type":"plain","value":null}}`, `{"choice":{"type":"plain","value":{}}}`} {
		_, err := gentypes.DecodePacket([]byte(wire))
		assert.Error(t, err, wire)
	}
	original, err := gentypes.DecodePacket([]byte(`{"choice":{"type":"plain","value":"e30="}}`))
	require.NoError(t, err)
	plain, ok := original.Choice.AsPlain()
	require.True(t, ok)
	assert.Equal(t, "{}", string(plain))
}
