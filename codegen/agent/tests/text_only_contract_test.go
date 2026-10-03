// These tests exercise regenerated synthetic contracts through their real JSON
// decoders and portable registry declarations before any executor runs.
package tests

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genrecords "goa.design/goa-ai/internal/testpresentation/gen/records/toolsets/records"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry/contract"
)

func TestGeneratedTextOnlyResultReminders(t *testing.T) {
	const domain = "Report the count with its selected scope."
	const ui = "The user sees an interactive record card."
	ordinary := genrecords.SpecRead()
	assert.Equal(t, domain+"\n"+ui, ordinary.ResultReminder)
	assert.Equal(t, domain, ordinary.ForTextOnly().ResultReminder)
	for _, declaration := range genrecords.ToolSchemas() {
		if declaration.Name != ordinary.Name.String() {
			continue
		}
		remote, err := contract.Compile(declaration)
		require.NoError(t, err)
		assert.Equal(t, ordinary.ResultReminder, remote.ResultReminder)
		assert.Equal(t, domain, remote.ForTextOnly().ResultReminder)
		return
	}
	t.Fatal("generated registry declaration for records.read is missing")
}

func TestGeneratedTextOnlyControlsDecodeFalse(t *testing.T) {
	ordinary := genrecords.SpecRead()
	text := ordinary.ForTextOnly()
	for _, test := range []struct {
		name  string
		codec tools.JSONCodec[any]
		input string
	}{
		{"model omission", text.Payload.Codec, `{"query":"active"}`},
		{"execution omission", text.ExecutionPayloadCodec, `{"query":"active"}`},
		{"execution explicit false", text.ExecutionPayloadCodec, `{"query":"active","render_ui":false,"render_summary":false}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			decoded, err := test.codec.FromJSON([]byte(test.input))
			require.NoError(t, err)
			payload, ok := decoded.(*genrecords.ReadPayload)
			require.True(t, ok)
			require.NotNil(t, payload.RenderUI)
			assert.False(t, *payload.RenderUI)
			assert.False(t, payload.RenderSummary)
			encoded, err := ordinary.ExecutionPayloadCodec.ToJSON(decoded)
			require.NoError(t, err)
			assert.JSONEq(t, `{"query":"active","render_ui":false,"render_summary":false}`, string(encoded))
		})
	}
}

func TestGeneratedTextOnlyControlsRejectSuppliedModelValues(t *testing.T) {
	ordinary := genrecords.SpecRead()
	text := ordinary.ForTextOnly()
	for _, field := range []string{"render_ui", "render_summary"} {
		for _, value := range []string{"false", "true", "null"} {
			_, err := text.Payload.Codec.FromJSON([]byte(`{"query":"active","` + field + `":` + value + `}`))
			require.Error(t, err, "model field %s=%s", field, value)
		}
		_, err := text.ExecutionPayloadCodec.FromJSON([]byte(`{"query":"active","` + field + `":true}`))
		require.Error(t, err, "execution field %s=true", field)
	}
	decoded, err := ordinary.ExecutionPayloadCodec.FromJSON([]byte(`{"query":"active","render_ui":true,"render_summary":true}`))
	require.NoError(t, err)
	payload, ok := decoded.(*genrecords.ReadPayload)
	require.True(t, ok)
	require.NotNil(t, payload.RenderUI)
	assert.True(t, *payload.RenderUI)
	assert.True(t, payload.RenderSummary)
}
