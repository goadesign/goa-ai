package workflowcodec

import (
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

// This literal is the saved pre-v2 schema, not JSON produced from today's type.
const legacyOutputFixture = `{"AgentID":"agent","RunID":"child","Final":null,"FinalToolResult":{"Name":"report.finish","Result":{"ok":true},"Telemetry":{"TokensUsed":2,"DurationMs":3,"Model":"terminal"}},"ToolEvents":[{"Name":"records.page","Result":{"rows":[1]},"Telemetry":{"TokensUsed":4,"DurationMs":5,"Model":"first"}},{"Name":"report.finish","Result":{"ok":true},"Telemetry":{"TokensUsed":2,"DurationMs":3,"Model":"terminal"}}],"Notes":null,"Usage":null,"Suspension":null}`

// This literal is the saved v2 schema before common run subjects.
const v2OutputFixture = `{"AgentID":"agent","RunID":"child","Final":null,"FinalToolResult":{"Name":"report.finish","Result":{"ok":true},"Telemetry":{"TokensUsed":2,"DurationMs":3,"Model":"terminal"}},"ToolCount":2,"ToolTelemetry":{"TokensUsed":6,"DurationMs":8,"Model":""},"Notes":null,"Usage":null,"Suspension":null}`

const newOutputFixture = `{"Subject":{"executor":{"type":"agent","value":"agent"}},"RunID":"child","Final":null,"FinalToolResult":{"Name":"report.finish","Result":{"ok":true},"Telemetry":{"TokensUsed":2,"DurationMs":3,"Model":"terminal"}},"ToolCount":2,"ToolTelemetry":{"TokensUsed":6,"DurationMs":8,"Model":""},"Notes":null,"Usage":null,"Suspension":null}`

func TestRunOutputSavedFormats(t *testing.T) {
	dc := NewDataConverter()
	payload := &commonpb.Payload{Metadata: map[string][]byte{converter.MetadataEncoding: []byte(runOutputEncoding)}, Data: []byte(newOutputFixture)}
	var value api.RunOutput
	require.NoError(t, dc.FromPayload(payload, &value))
	assert.Equal(t, 2, value.ToolCount)
	assert.Equal(t, &telemetry.ToolTelemetry{TokensUsed: 6, DurationMs: 8}, value.ToolTelemetry)
	require.NotNil(t, value.FinalToolResult)
	assert.Equal(t, "terminal", value.FinalToolResult.Telemetry.Model)
	assert.JSONEq(t, `{"ok":true}`, string(value.FinalToolResult.Result))
	var pointer *api.RunOutput
	require.NoError(t, dc.FromPayload(payload, &pointer))
	assert.Equal(t, &value, pointer)
	var deep **api.RunOutput
	require.NoError(t, dc.FromPayload(payload, &deep))
	assert.Equal(t, &value, *deep)
	for _, input := range []any{value, &value, &pointer} {
		encoded, err := dc.ToPayload(input)
		require.NoError(t, err)
		assert.Equal(t, runOutputEncoding, string(encoded.Metadata[converter.MetadataEncoding]))
		assert.NotContains(t, string(encoded.Data), "ToolEvents")
	}
}

func TestRunOutputSavedFormatsRejectWrongContracts(t *testing.T) {
	dc := NewDataConverter()
	for _, test := range []struct{ name, encoding, body string }{
		{"new under old tag", converter.MetadataEncodingJSON, newOutputFixture},
		{"old unversioned", converter.MetadataEncodingJSON, legacyOutputFixture},
		{"old v2", "json/goa-ai-run-output-v2", v2OutputFixture},
		{"old under new tag", runOutputEncoding, legacyOutputFixture},
		{"unknown encoding", "json/goa-ai-run-output-v999", newOutputFixture},
		{"unknown new field", runOutputEncoding, `{"Unknown":1}`},
		{"new trailing", runOutputEncoding, newOutputFixture + ` {}`},
		{"new malformed", runOutputEncoding, `{`},
		{"new negative count", runOutputEncoding, `{"Subject":{"executor":{"type":"agent","value":"agent"}},"ToolCount":-1}`},
		{"new negative tokens", runOutputEncoding, `{"Subject":{"executor":{"type":"agent","value":"agent"}},"ToolCount":1,"ToolTelemetry":{"TokensUsed":-1}}`},
		{"new negative duration", runOutputEncoding, `{"Subject":{"executor":{"type":"agent","value":"agent"}},"ToolCount":1,"ToolTelemetry":{"DurationMs":-1}}`},
		{"oversized old", converter.MetadataEncodingJSON, `{"RunID":"` + strings.Repeat("x", engine.MaxPayloadBytes) + `"}`},
		{"oversized new", runOutputEncoding, `{"RunID":"` + strings.Repeat("x", engine.MaxPayloadBytes) + `"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := &commonpb.Payload{Metadata: map[string][]byte{converter.MetadataEncoding: []byte(test.encoding)}, Data: []byte(test.body)}
			var out *api.RunOutput
			require.Error(t, dc.FromPayload(payload, &out))
		})
	}
	for _, out := range []api.RunOutput{
		{ToolCount: -1},
		{ToolTelemetry: &telemetry.ToolTelemetry{TokensUsed: -1}},
		{ToolTelemetry: &telemetry.ToolTelemetry{DurationMs: -1}},
	} {
		out.Subject = genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("agent")}
		_, err := dc.ToPayload(out)
		require.Error(t, err)
	}
	payload := &commonpb.Payload{Metadata: map[string][]byte{converter.MetadataEncoding: []byte(runOutputEncoding)}, Data: []byte(newOutputFixture)}
	var unrelated map[string]any
	require.ErrorContains(t, dc.FromPayload(payload, &unrelated), "RunOutput destination")
}

func TestRunOutputNilAndOtherEncodingsRemainUnchanged(t *testing.T) {
	dc := NewDataConverter()
	payload := &commonpb.Payload{Metadata: map[string][]byte{converter.MetadataEncoding: []byte(runOutputEncoding)}, Data: []byte("null")}
	value := api.RunOutput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("agent")}, RunID: "retained"}
	require.NoError(t, dc.FromPayload(payload, &value))
	assert.Equal(t, "retained", value.RunID)
	pointer := &value
	require.NoError(t, dc.FromPayload(payload, &pointer))
	assert.Nil(t, pointer)
	for _, value := range []any{nil, (*api.RunOutput)(nil)} {
		payload, err := dc.ToPayload(value)
		require.NoError(t, err)
		assert.Equal(t, converter.MetadataEncodingNil, string(payload.Metadata[converter.MetadataEncoding]))
		var pointer *api.RunOutput
		require.NoError(t, dc.FromPayload(payload, &pointer))
		assert.Nil(t, pointer)
	}
	for _, value := range []any{api.RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("agent")}, RunID: "input"}, map[string]any{"n": int64(math.MaxInt64)}, []string{"a"}} {
		payload, err := dc.ToPayload(value)
		require.NoError(t, err)
		assert.Equal(t, converter.MetadataEncodingJSON, string(payload.Metadata[converter.MetadataEncoding]))
	}
	bytes, err := dc.ToPayload([]byte{1, 2})
	require.NoError(t, err)
	assert.Equal(t, converter.MetadataEncodingBinary, string(bytes.Metadata[converter.MetadataEncoding]))
}
