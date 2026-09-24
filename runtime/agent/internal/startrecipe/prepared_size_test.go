package startrecipe

// The stored-byte ceiling must cover valid tiny-value requests as well as
// large payloads. Golden framing values make codec changes reopen this proof.
import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/engine"
)

func TestPreparedRequestByteBoundIncludesEntryFraming(t *testing.T) {
	empty, err := json.Marshal(preparedValue{Payload: preparedPayload{Metadata: map[string][]byte{"": nil}}})
	require.NoError(t, err)
	require.Len(t, empty, 56)
	require.Equal(t, 391+75*engine.MaxPayloadBytes, PreparedRequestByteLimit())
	for _, payload := range []preparedPayload{
		{Metadata: map[string][]byte{"": nil, "\x01": nil}},
		{Metadata: map[string][]byte{"": {1}}},
	} {
		value, err := json.Marshal(preparedValue{Name: "x", Payload: payload})
		require.NoError(t, err)
		require.LessOrEqual(t, len(value), len(empty)+6+16)
	}
}
