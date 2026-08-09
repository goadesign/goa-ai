package toolserverdata

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestCanonicalizeDelegatesPayloadAndEncodesEnvelope(t *testing.T) {
	t.Parallel()

	got, err := Canonicalize(
		rawjson.Message(`[{"kind":"chart.points","audience":"timeline","data": { "count": 2 }}]`),
		func(kind, audience string, data rawjson.Message) (string, rawjson.Message, error) {
			assert.Equal(t, "chart.points", kind)
			assert.Equal(t, "timeline", audience)
			assert.JSONEq(t, `{"count":2}`, string(data))
			return "timeline", rawjson.Message(`{"count":2}`), nil
		},
	)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"kind":"chart.points","audience":"timeline","data":{"count":2}}]`, string(got))
}

func TestCanonicalizeRejectsInvalidEnvelope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
		want string
	}{
		{
			name: "null envelope",
			data: `null`,
			want: "expected array",
		},
		{
			name: "null item",
			data: `[null]`,
			want: "item 0 is null",
		},
		{
			name: "unknown envelope field",
			data: `[{"kind":"chart.points","audience":"timeline","data":{},"legacy":true}]`,
			want: `unknown field "legacy"`,
		},
		{
			name: "trailing value",
			data: `[] {}`,
			want: "trailing JSON value",
		},
		{
			name: "duplicate kind",
			data: `[{"kind":"chart.points","audience":"timeline","data":{}},{"kind":"chart.points","audience":"timeline","data":{}}]`,
			want: "appears more than once",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Canonicalize(rawjson.Message(test.data), canonicalizeTestItem)
			require.Error(t, err)
			assert.ErrorContains(t, err, test.want)
		})
	}
}

func TestApplyRejectsDataWithoutGeneratedContract(t *testing.T) {
	t.Parallel()

	_, err := Apply(nil, rawjson.Message(`[]`))
	require.Error(t, err)
	assert.ErrorContains(t, err, "tool does not declare server data")
}

func canonicalizeTestItem(kind, audience string, data rawjson.Message) (string, rawjson.Message, error) {
	if kind != "chart.points" {
		return "", nil, fmt.Errorf("unexpected kind %q", kind)
	}
	return audience, data, nil
}
