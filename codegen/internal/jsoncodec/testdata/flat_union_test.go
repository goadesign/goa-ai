// These tests call the generated strict codec through its public typed API.
// Both branches and nested collections retain their exact values and reject
// unknown fields, duplicate members and values that violate the selected type.
package types_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	gentypes "codec.local/gen/types"
)

func TestFlatUnionRoundTrip(t *testing.T) {
	for _, value := range []*gentypes.Outcome{
		{Outcome: gentypes.NewResultChoiceComplete(&gentypes.Complete{Reference: "done"})},
		{Outcome: gentypes.NewResultChoiceInputRequired(&gentypes.Pending{State: "next"})},
	} {
		before := *value
		encoded, err := gentypes.EncodeOutcome(value)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), `"value":`)
		decoded, err := gentypes.DecodeOutcome(encoded)
		require.NoError(t, err)
		require.Equal(t, value, decoded)
		require.Equal(t, before, *value)
		nested := &gentypes.Results{Items: []*gentypes.Outcome{value}, Named: map[string]*gentypes.Outcome{"first": value}}
		data, err := gentypes.EncodeResults(nested)
		require.NoError(t, err)
		result, err := gentypes.DecodeResults(data)
		require.NoError(t, err)
		require.Equal(t, nested, result)
		require.True(t, nested.Items[0] == value)
	}
}

func TestFlatUnionRejectsInvalidJSON(t *testing.T) {
	for _, document := range []string{
		`null`, `[]`, `{}`, `{"outcome":null}`,
		`{"outcome":{"resultType":null}}`, `{"outcome":{"resultType":1}}`,
		`{"outcome":{"resultType":"unknown"}}`, `{"outcome":{"reference":"done"}}`,
		`{"outcome":{"resultType":"complete","reference":""}}`,
		`{"outcome":{"resultType":"complete","reference":null}}`,
		`{"outcome":{"resultType":"complete","reference":"done","state":"next"}}`,
		`{"outcome":{"resultType":"complete","value":{"reference":"done"}}}`,
		`{"outcome":{"resultType":"complete","reference":"done","extra":true}}`,
		`{"outcome":{"resultType":"complete","resultType":"input_required","state":"next"}}`,
		`{"outcome":{"resultType":"complete","reference":"done","reference":"other"}}`,
		`{"outcome":{"resultType":"input_required","state":"next"}} {}`,
	} {
		value, err := gentypes.DecodeOutcome([]byte(document))
		require.Error(t, err, document)
		require.Nil(t, value)
	}
}

func TestFlatUnionRejectsInvalidTypedValue(t *testing.T) {
	for _, value := range []*gentypes.Outcome{
		{}, {Outcome: gentypes.NewResultChoiceComplete(nil)},
		{Outcome: gentypes.NewResultChoiceComplete(&gentypes.Complete{})},
		{Outcome: gentypes.NewResultChoiceInputRequired(&gentypes.Pending{})},
		{Outcome: gentypes.NewResultChoiceComplete(&gentypes.Complete{Reference: string([]byte{0xff})})},
	} {
		before := *value
		data, err := gentypes.EncodeOutcome(value)
		require.Error(t, err)
		require.Nil(t, data)
		require.True(t, reflect.DeepEqual(before, *value))
	}
}
