// These runtime checks compare complete original values with the narrower tool
// inputs and completion results emitted by the same ordinary generation command.
package sample_test

import (
	"testing"

	sample "codec.local/gen/sample"
	completions "codec.local/gen/sample/completions"
	checks "codec.local/gen/sample/toolsets/checks"
	types "codec.local/gen/types"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/runtime"
)

func TestOriginalAndInjectedToolContracts(t *testing.T) {
	original := &sample.ToolInput{
		Text: "ready", RecordKey: "record-a", SessionID: "server-session",
	}
	data, err := sample.EncodeToolInput(original)
	require.NoError(t, err)
	require.JSONEq(t, `{"text":"ready","recordKey":"record-a","session_id":"server-session"}`, string(data))
	decoded, err := sample.DecodeToolInput(data)
	require.NoError(t, err)
	require.Equal(t, original, decoded)
	value, err := sample.DecodeToolInput([]byte(`{"text":"ready","recordKey":"record-a"}`))
	require.Error(t, err)
	require.Nil(t, value)

	modelJSON := []byte(`{"text":"ready","record_key":"record-a"}`)
	payload, err := checks.CheckPayloadCodec().FromJSON(modelJSON)
	require.NoError(t, err)
	require.Empty(t, payload.SessionID)
	require.Equal(t, "record-a", payload.RecordKey)
	injected, err := checks.DecodeCheck(modelJSON, runtime.ToolCallMeta{SessionID: "server-session"}, nil)
	require.NoError(t, err)
	require.Equal(t, "server-session", injected.SessionID)
	encoded, err := checks.CheckPayloadCodec().ToJSON(injected)
	require.NoError(t, err)
	require.JSONEq(t, string(modelJSON), string(encoded))
	for _, invalid := range []string{
		`{"text":"ready","recordKey":"record-a"}`,
		`{"text":"ready","record_key":"record-a","session_id":"model-authored"}`,
	} {
		value, err := checks.CheckPayloadCodec().FromJSON([]byte(invalid))
		require.Error(t, err)
		require.Nil(t, value)
	}
	spec := checks.SpecCheck()
	for _, schema := range []string{string(spec.Payload.Schema), string(spec.ExecutionPayloadSchema)} {
		require.Contains(t, schema, `"record_key"`)
		require.NotContains(t, schema, `"recordKey"`)
		require.NotContains(t, schema, `"session_id"`)
	}
	result, err := checks.CheckResultCodec().FromJSON([]byte(`"ready"`))
	require.NoError(t, err)
	require.Equal(t, checks.CheckResult("ready"), result)
}

func TestCompletionCodecsAndLocatedOriginal(t *testing.T) {
	items := completions.SpecItems()
	value, err := items.Codec.FromJSON([]byte(`[{"text":"ready"}]`))
	require.NoError(t, err)
	require.Equal(t, completions.ItemsResult{{Text: "ready"}}, value)
	_, err = items.Codec.FromJSON([]byte(`[{"text":"ready","unknown":true}]`))
	require.Error(t, err)
	require.Contains(t, string(items.Schema), `"type":"array"`)

	lines := completions.SpecLines()
	entries, err := lines.Codec.FromJSON([]byte(`["ready","next"]`))
	require.NoError(t, err)
	require.Equal(t, completions.LinesResult{"ready", "next"}, entries)
	data, err := lines.Codec.ToJSON(entries)
	require.NoError(t, err)
	require.JSONEq(t, `["ready","next"]`, string(data))
	_, err = lines.Codec.FromJSON([]byte(`["ready",7]`))
	require.Error(t, err)

	headline := completions.SpecHeadline()
	text, err := headline.Codec.FromJSON([]byte(`"ready"`))
	require.NoError(t, err)
	require.Equal(t, completions.HeadlineResult("ready"), text)
	data, err = headline.Codec.ToJSON(text)
	require.NoError(t, err)
	require.JSONEq(t, `"ready"`, string(data))
	_, err = headline.Codec.FromJSON([]byte(`{}`))
	require.Error(t, err)

	original := &types.Item{Text: "ready"}
	data, err = types.EncodeItem(original)
	require.NoError(t, err)
	decoded, err := types.DecodeItem(data)
	require.NoError(t, err)
	require.Equal(t, original, decoded)
}
