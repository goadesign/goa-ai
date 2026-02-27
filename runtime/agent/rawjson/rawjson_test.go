package rawjson

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRawJSONMarshalJSONEmptyAndWhitespaceAsNull(t *testing.T) {
	testCases := []struct {
		name  string
		value RawJSON
	}{
		{
			name:  "nil",
			value: nil,
		},
		{
			name:  "empty bytes",
			value: RawJSON([]byte{}),
		},
		{
			name:  "whitespace bytes",
			value: RawJSON([]byte("  \n\t  ")),
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			out, err := testCase.value.MarshalJSON()
			require.NoError(t, err)
			require.Equal(t, "null", string(out))
		})
	}
}

func TestRawJSONMarshalJSONRejectsInvalidJSON(t *testing.T) {
	testCases := []struct {
		name  string
		value RawJSON
	}{
		{
			name:  "truncated object",
			value: RawJSON([]byte(`{"a"`)),
		},
		{
			name:  "invalid token",
			value: RawJSON([]byte(`{x:1}`)),
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := testCase.value.MarshalJSON()
			require.Error(t, err)
			require.ErrorContains(t, err, "rawjson: invalid JSON")
		})
	}
}

func TestRawJSONUnmarshalJSONNormalizesAndValidates(t *testing.T) {
	testCases := []struct {
		name      string
		input     []byte
		wantNil   bool
		wantBytes string
		wantErr   string
	}{
		{
			name:    "empty bytes become nil",
			input:   []byte("   \n\t "),
			wantNil: true,
		},
		{
			name:    "null becomes nil",
			input:   []byte("null"),
			wantNil: true,
		},
		{
			name:      "valid JSON is preserved",
			input:     []byte(` {"a":[1,2]} `),
			wantBytes: `{"a":[1,2]}`,
		},
		{
			name:    "invalid JSON fails",
			input:   []byte(`{"a"`),
			wantErr: "rawjson: invalid JSON",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var value RawJSON
			err := value.UnmarshalJSON(testCase.input)
			if testCase.wantErr != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, testCase.wantErr)
				return
			}
			require.NoError(t, err)
			if testCase.wantNil {
				require.Nil(t, value)
				return
			}
			require.Equal(t, testCase.wantBytes, string(value))
		})
	}
}

func TestRawJSONRoundTripWithEncodingJSON(t *testing.T) {
	type envelope struct {
		Payload RawJSON `json:"payload"`
	}

	input := envelope{
		Payload: RawJSON([]byte(`{"a":[1,2,3],"ok":true}`)),
	}
	wire, err := json.Marshal(input)
	require.NoError(t, err)

	var decoded envelope
	err = json.Unmarshal(wire, &decoded)
	require.NoError(t, err)
	require.JSONEq(t, string(input.Payload), string(decoded.Payload))
}

func TestRawJSONRawMessageReturnsUnderlyingBytes(t *testing.T) {
	value := RawJSON([]byte(`{"x":1}`))
	require.JSONEq(t, `{"x":1}`, string(value.RawMessage()))
}
