// Result envelopes carry raw values into the already compiled tool contract.
// The tool contract, rather than envelope parsing, checks dynamic value shapes.
package contract

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/goa-ai/runtime/toolserverdata"
)

func TestTerminalEnvelopeUsesCompiledToolCodecs(t *testing.T) {
	t.Parallel()

	spec, err := Compile(testDeclaration())
	require.NoError(t, err)
	body := []byte(fmt.Sprintf(`{
		"registration_token":%q,"tool_use_id":"compiled-result",
		"result_json":{ "value":9007199254740993 },
		"server_data":[{"kind":"record","audience":"timeline","data":{ "value":9007199254740993 }}]
	}`, strings.Repeat("a", 64)))
	message, err := toolregistry.DecodeToolResultMessage(body)
	require.NoError(t, err)
	require.NoError(t, toolregistry.ValidateToolResultMessage(message))
	value, err := spec.Result.Codec.FromJSON(message.Result)
	require.NoError(t, err)
	record, ok := value.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, json.Number("9007199254740993"), record["value"])
	resultJSON, err := spec.Result.Codec.ToJSON(value)
	require.NoError(t, err)
	assert.JSONEq(t, `{"value":9007199254740993}`, string(resultJSON))

	serverJSON, err := toolregistry.EncodeServerData(message.ServerData)
	require.NoError(t, err)
	canonical, err := toolserverdata.Apply(spec.CanonicalizeServerData, rawjson.Message(serverJSON))
	require.NoError(t, err)
	items, err := toolregistry.DecodeServerData(canonical)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "timeline", items[0].Audience)
	value, err = spec.ServerData[0].Type.Codec.FromJSON(items[0].Data)
	require.NoError(t, err)
	record, ok = value.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, json.Number("9007199254740993"), record["value"])
}

func TestTerminalEnvelopeLeavesDynamicValidationWithCompiledOwner(t *testing.T) {
	t.Parallel()

	spec, err := Compile(testDeclaration())
	require.NoError(t, err)
	for _, test := range []struct {
		name       string
		result     string
		serverData string
		badResult  bool
	}{
		{name: "result type", result: `{"value":"wrong"}`, badResult: true},
		{name: "result field", result: `{"value":1,"unknown":true}`, badResult: true},
		{name: "server payload", result: `{"value":1}`, serverData: `[{"kind":"record","audience":"timeline","data":{"value":"wrong"}}]`},
		{name: "unknown kind", result: `{"value":1}`, serverData: `[{"kind":"other","audience":"timeline","data":{"value":1}}]`},
		{name: "wrong audience", result: `{"value":1}`, serverData: `[{"kind":"record","audience":"internal","data":{"value":1}}]`},
		{name: "duplicate kind", result: `{"value":1}`, serverData: `[{"kind":"record","audience":"timeline","data":{"value":1}},{"kind":"record","audience":"timeline","data":{"value":2}}]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := ""
			if test.serverData != "" {
				server = `,"server_data":` + test.serverData
			}
			message, err := toolregistry.DecodeToolResultMessage([]byte(fmt.Sprintf(
				`{"registration_token":%q,"tool_use_id":"compiled-result","result_json":%s%s}`,
				strings.Repeat("a", 64), test.result, server,
			)))
			require.NoError(t, err)
			require.NoError(t, toolregistry.ValidateToolResultMessage(message))
			_, err = spec.Result.Codec.FromJSON(message.Result)
			if test.badResult {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			serverJSON, err := toolregistry.EncodeServerData(message.ServerData)
			require.NoError(t, err)
			_, err = toolserverdata.Apply(spec.CanonicalizeServerData, rawjson.Message(serverJSON))
			require.Error(t, err)
		})
	}
	message, err := toolregistry.DecodeToolResultMessage([]byte(`{"server_data":[{"kind":"record","audience":"timeline","data":{"value":1}}]}`))
	require.NoError(t, err)
	serverJSON, err := toolregistry.EncodeServerData(message.ServerData)
	require.NoError(t, err)
	_, err = toolserverdata.Apply(nil, rawjson.Message(serverJSON))
	require.ErrorContains(t, err, "does not declare server data")
}
