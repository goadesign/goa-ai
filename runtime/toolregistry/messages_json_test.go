// These tests distinguish fixed wire records from raw tool values and keep
// parsing separate from the existing message and tool-contract validators.
package toolregistry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeToolResultMessageRejectsMalformedRecords(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		body string
	}{
		{name: "empty"},
		{name: "whitespace", body: " \n\t"},
		{name: "null", body: "null"},
		{name: "array", body: "[]"},
		{name: "string", body: `"result"`},
		{name: "number", body: "7"},
		{name: "boolean", body: "true"},
		{name: "malformed", body: `{"tool_use_id":"partial",`},
		{name: "second object", body: `{"tool_use_id":"partial"} {}`},
		{name: "second scalar", body: `{"tool_use_id":"partial"} true`},
		{name: "second null", body: `{"tool_use_id":"partial"} null`},
		{name: "trailing garbage", body: `{"tool_use_id":"partial"} !`},
		{name: "root unknown", body: `{"tool_use_id":"partial","extra":1}`},
		{name: "bounds unknown", body: `{"bounds":{"Returned":1,"extra":1}}`},
		{name: "server item unknown", body: `{"server_data":[{"kind":"record","audience":"timeline","data":{},"extra":1}]}`},
		{name: "error unknown", body: `{"error":{"extra":1}}`},
		{name: "failure unknown", body: `{"error":{"failure":{"extra":1}}}`},
		{name: "recovery unknown", body: `{"error":{"failure":{"recovery":{"extra":1}}}}`},
		{name: "issue unknown", body: `{"error":{"failure":{"recovery":{"issues":[{"extra":1}]}}}}`},
		{name: "cause unknown", body: `{"error":{"failure":{"error":{"cause":{"extra":1}}}}}`},
		{name: "retry unknown", body: `{"retry":{"extra":1}}`},
		{name: "no retry invented field", body: `{"noRetry":true}`},
		{name: "identity type", body: `{"tool_use_id":7}`},
		{name: "bounds type", body: `{"bounds":{"Returned":"one"}}`},
		{name: "server item type", body: `{"server_data":[true]}`},
		{name: "retry integer type", body: `{"retry":{"retry_after_ms":0.5}}`},
		{name: "retry integer overflow", body: `{"retry":{"retry_after_ms":9223372036854775808}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			message, err := DecodeToolResultMessage([]byte(test.body))
			require.Error(t, err)
			assert.Equal(t, ToolResultMessage{}, message, "never return a partially decoded record")
		})
	}
}

func TestDecodeToolResultMessagePreservesRawValues(t *testing.T) {
	t.Parallel()

	const raw = `{ "unregistered": [9007199254740993, {"text":"\u0061"}] }`
	body := []byte(fmt.Sprintf(`{
		"registration_token":%q,
		"tool_use_id":"raw-result",
		"result_json":%s,
		"server_data":[{"kind":"record","audience":"timeline","data":%s}],
		"bounds":{"Returned":1,"Total":2,"Truncated":true,"NextCursor":"next","RefinementHint":"narrow"}
	}`, strings.Repeat("a", 64), raw, raw))
	original := bytes.Clone(body)

	message, err := DecodeToolResultMessage(body)

	require.NoError(t, err)
	require.NoError(t, ValidateToolResultMessage(message))
	assert.Equal(t, []byte(raw), []byte(message.Result)) //nolint:testifylint // Exact JSON bytes, including whitespace, are part of this contract.
	require.Len(t, message.ServerData, 1)
	assert.Equal(t, []byte(raw), []byte(message.ServerData[0].Data)) //nolint:testifylint // Exact JSON bytes, including whitespace, are part of this contract.
	require.NotNil(t, message.Bounds)
	assert.Equal(t, 1, message.Bounds.Returned)
	require.NotNil(t, message.Bounds.Total)
	assert.Equal(t, 2, *message.Bounds.Total)
	assert.True(t, message.Bounds.Truncated)
	require.NotNil(t, message.Bounds.NextCursor)
	assert.Equal(t, "next", *message.Bounds.NextCursor)
	assert.Equal(t, "narrow", message.Bounds.RefinementHint)
	assert.Equal(t, original, body)
	clear(body)
	assert.Equal(t, []byte(raw), []byte(message.Result))             //nolint:testifylint // Exact JSON bytes, including whitespace, are part of this contract.
	assert.Equal(t, []byte(raw), []byte(message.ServerData[0].Data)) //nolint:testifylint // Exact JSON bytes, including whitespace, are part of this contract.
}

func TestDecodeToolResultMessagePreservesRecovery(t *testing.T) {
	t.Parallel()

	const prior = `{ "value":9007199254740993, "dynamic":[{"custom":true}] }`
	const example = `{ "value":9007199254740993 }`
	body := []byte(fmt.Sprintf(`{
		"registration_token":%q,"tool_use_id":"correction",
		"error":{"code":"invalid_arguments","failure":{
			"kind":"invalid_call",
			"error":{"message":"invalid value","cause":{"message":"wrong type"}},
			"recovery":{"action":"correct_call",
				"issues":[{"field":"value","constraint":"invalid_field_type","expected_json_type":"integer","actual_json_type":"string"}],
				"prior_input":%s,"example_json":%s
			}
		}}
	}`, strings.Repeat("a", 64), prior, example))
	message, err := DecodeToolResultMessage(body)
	require.NoError(t, err)
	require.NoError(t, ValidateToolResultMessage(message))
	failure := message.Error.Failure
	assert.Equal(t, "wrong type", failure.Error.Cause.Message)
	assert.Equal(t, []byte(prior), []byte(failure.Recovery.PriorInput)) //nolint:testifylint // Exact JSON bytes, including whitespace, are part of this contract.
	assert.Equal(t, []byte(example), []byte(failure.Recovery.ExampleJSON))
	require.Len(t, failure.Recovery.Issues, 1)
	assert.Equal(t, "integer", failure.Recovery.Issues[0].ExpectedJSONType)
	clear(body)
	assert.Equal(t, []byte(prior), []byte(failure.Recovery.PriorInput)) //nolint:testifylint // Exact JSON bytes, including whitespace, are part of this contract.
	assert.Equal(t, []byte(example), []byte(failure.Recovery.ExampleJSON))
}

func TestDecodeToolResultMessageKeepsStandardParserSemantics(t *testing.T) {
	t.Parallel()

	for _, body := range [][]byte{
		[]byte(`{"tool_use_id":"first","tool_use_id":"last"}`),
		[]byte(`{"TOOL_USE_ID":"case alias","bounds":{"returned":1}}`),
		[]byte(`{"bounds":{"Returned":1},"bounds":{"Truncated":true}}`),
		[]byte(`{"tool_use_id":"\ud800"}`),
		append(append([]byte(`{"tool_use_id":"`), byte(0xff)), []byte(`"}`)...),
		[]byte(`{"error":{"failure":{"recovery":{"prior_input":null,"example_json":null}}}}`),
		[]byte(`{"result_json":null,"server_data":[{"data":null}]}`),
	} {
		var existing ToolResultMessage
		require.NoError(t, json.Unmarshal(body, &existing))
		decoded, err := DecodeToolResultMessage(body)
		require.NoError(t, err)
		assert.Equal(t, existing, decoded)
	}
}

func TestDecodeToolResultMessageLeavesSemanticValidationToOwners(t *testing.T) {
	t.Parallel()

	identity := fmt.Sprintf(`"registration_token":%q,"tool_use_id":"semantic-result"`, strings.Repeat("a", 64))
	for _, test := range []struct {
		name       string
		body       string
		validUnion bool
	}{
		{name: "empty record parses", body: `{}`},
		{name: "nil semantic success", body: `{` + identity + `}`, validUnion: true},
		{name: "retry control", body: `{` + identity + `,"retry":{"reason":"provider_overloaded","retry_after_ms":250}}`, validUnion: true},
		{name: "invalid union parses", body: `{` + identity + `,"result_json":{},"retry":{"reason":"provider_overloaded"}}`},
		{name: "null item awaits server data validator", body: `{` + identity + `,"server_data":[null]}`, validUnion: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			message, err := DecodeToolResultMessage([]byte(test.body))
			require.NoError(t, err)
			err = ValidateToolResultMessage(message)
			if test.validUnion {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}
