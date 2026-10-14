// Public questions preserve exact extension and form-schema bytes while the
// existing MCP parser owns valid modes, fields, and response validation.
package mcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genhostinput "goa.design/goa-ai/registry/gen/hostinput"
)

func TestPublicInputPreservesExtensionsAndOriginalParameters(t *testing.T) {
	for _, test := range []struct {
		name, params string
		form         bool
	}{
		{"omitted form mode", `{"message":"Select a value","requestedSchema":{"type":"object","properties":{"number":{"type":"integer","minimum":9007199254740993}}},"extension":{"nested":[9007199254740993,{"exact":1e100}]},"MESSAGE":"case-sensitive"}`, true},
		{"URL", `{"mode":"url","message":"Authorize","url":"https://example.test/consent","extension":{"nested":[9007199254740993,{"exact":1e100}]},"MESSAGE":"case-sensitive"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := InputRequest{Method: "elicitation/create", Params: json.RawMessage(test.params)}
			public, err := request.PublicInput()
			require.NoError(t, err)
			assert.Equal(t, test.params, string(request.Params))
			require.Len(t, public.Extensions, 2)
			assert.Equal(t, []byte(`{"nested":[9007199254740993,{"exact":1e100}]}`), public.Extensions["extension"]) //nolint:testifylint // Host question extensions must preserve exact integer and exponent text.
			assert.Equal(t, `"case-sensitive"`, string(public.Extensions["MESSAGE"]))
			if test.form {
				form, ok := public.Question.AsForm()
				require.True(t, ok)
				assert.Contains(t, string(form.RequestedSchema), "9007199254740993")
			} else {
				url, ok := public.Question.AsURL()
				require.True(t, ok)
				assert.Equal(t, "https://example.test/consent", url.URL)
			}
			encoded, err := genhostinput.EncodeHostInputRequest(public)
			require.NoError(t, err)
			restored, err := genhostinput.DecodeHostInputRequest(encoded)
			require.NoError(t, err)
			assert.Equal(t, public, restored)
			public.Extensions["extension"][0] = 'x'
			assert.Equal(t, test.params, string(request.Params))
		})
	}
}

func TestPublicInputKeepsOwningParserRejections(t *testing.T) {
	for _, params := range []string{
		`{"mode":"unsupported","message":"Prompt"}`,
		`{"mode":null,"message":"Prompt","requestedSchema":{"type":"object","properties":{}}}`,
		`{"message":"First","message":"Second","requestedSchema":{"type":"object","properties":{}}}`,
		`{"mode":"url","message":"Prompt","url":"relative"}`,
	} {
		_, err := (InputRequest{Method: "elicitation/create", Params: json.RawMessage(params)}).PublicInput()
		assert.Error(t, err)
	}
}
