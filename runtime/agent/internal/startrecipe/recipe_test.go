// These tests prove workflow input snapshots and start digests preserve exact values.
package startrecipe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/proto"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

const otherRecipeValue = "other"

type (
	recipeMemoAlias string

	recipeMemoValue struct {
		Name string
	}
)

const preparedRequestV1Golden = `{"version":"goa-ai-prepared-run-v1","agent_id":"agent","id":"run-1","workflow":"agent.workflow","task_queue":"custom.queue","input":{"metadata":{"encoding":"anNvbi9wbGFpbg=="},"data":"eyJBZ2VudElEIjoiYWdlbnQiLCJSdW5JRCI6InJ1bi0xIiwiU2Vzc2lvbklEIjoic2Vzc2lvbi0xIiwiVHVybklEIjoiIiwiUGFyZW50VG9vbENhbGxJRCI6IiIsIlBhcmVudFJ1bklEIjoiIiwiUGFyZW50QWdlbnRJRCI6IiIsIlRvb2wiOiIiLCJUb29sQXJncyI6bnVsbCwiTWVzc2FnZXMiOm51bGwsIlJlbmRlcmVkUHJvbXB0cyI6bnVsbCwiTGFiZWxzIjpudWxsLCJNZXRhZGF0YSI6bnVsbCwiUG9saWN5IjpudWxsLCJDb250aW51YXRpb24iOm51bGx9"},"run_timeout":0,"retry_policy":{"MaxAttempts":4,"UnlimitedAttempts":false,"InitialInterval":2000000000,"BackoffCoefficient":1.5},"memo":[{"name":"note","payload":{"metadata":{"encoding":"anNvbi9wbGFpbg=="},"data":"Im1lbW8gdmFsdWUi"}}],"search_attributes":[{"name":"Attempt","payload":{"metadata":{"encoding":"anNvbi9wbGFpbg==","type":"SW50"},"data":"Mw=="}}],"task_queue_override":"custom.queue"}`

func TestPreparedRequestV1GoldenBytes(t *testing.T) {
	memo, err := EncodeMemo(map[string]any{"note": "memo value"})
	require.NoError(t, err)
	created, err := NewPreparedRequest("agent", engine.WorkflowStartRequest{
		ID:        "run-1",
		Workflow:  "agent.workflow",
		TaskQueue: "custom.queue",
		Input: &api.RunInput{
			AgentID:   "agent",
			RunID:     "run-1",
			SessionID: "session-1",
		},
		Memo:             memo,
		SearchAttributes: map[string]any{"Attempt": int64(3)},
		RetryPolicy: engine.RetryPolicy{
			MaxAttempts:        4,
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 1.5,
		},
	}, "custom.queue")
	require.NoError(t, err)
	createdData, err := created.MarshalBinary()
	require.NoError(t, err)
	//nolint:testifylint // The durable contract requires identical bytes, not equivalent JSON.
	require.Equal(t, preparedRequestV1Golden, string(createdData))

	parsed, err := ParsePreparedRequest([]byte(preparedRequestV1Golden))
	require.NoError(t, err)
	parsedData, err := parsed.MarshalBinary()
	require.NoError(t, err)
	//nolint:testifylint // The durable contract requires identical bytes, not equivalent JSON.
	require.Equal(t, preparedRequestV1Golden, string(parsedData))
	parsedData[0] = 'X'
	again, err := parsed.MarshalBinary()
	require.NoError(t, err)
	//nolint:testifylint // A parsed request must retain the exact accepted bytes.
	require.Equal(t, preparedRequestV1Golden, string(again))
}

func TestNewPreparedRequestDefersDurableEncoding(t *testing.T) {
	prepared, err := NewPreparedRequest("agent", engine.WorkflowStartRequest{
		ID: "run-1", Workflow: "agent.workflow", TaskQueue: "agent.queue",
		Input: &api.RunInput{AgentID: "agent", RunID: "run-1"},
	}, "")
	require.NoError(t, err)
	require.Nil(t, prepared.data)

	data, err := prepared.MarshalBinary()
	require.NoError(t, err)
	require.NotEmpty(t, data)
	require.Nil(t, prepared.data)
}

func TestPreparedRequestMarshalBinaryUsesAcceptedSnapshot(t *testing.T) {
	prepared, err := NewPreparedRequest("agent", engine.WorkflowStartRequest{
		ID: "run-1", Workflow: "agent.workflow", TaskQueue: "agent.queue",
		Input: &api.RunInput{AgentID: "agent", RunID: "run-1"},
	}, "")
	require.NoError(t, err)
	prepared.Request = engine.WorkflowStartRequest{}

	data, err := prepared.MarshalBinary()
	require.NoError(t, err)
	parsed, err := ParsePreparedRequest(data)
	require.NoError(t, err)
	require.Equal(t, "run-1", parsed.Request.ID)
	require.Equal(t, prepared.Digest, parsed.Digest)
}

func TestPreparedRequestMarshalBinaryOwnsStoredRecordLimit(t *testing.T) {
	const memoCount = 120_000
	memo := make(map[string]engine.EncodedValue, memoCount)
	for index := range memoCount {
		memo[fmt.Sprintf("%06x", index)] = engine.EncodedValue{}
	}
	workflow := strings.Repeat("\x01", 100_000)
	taskQueue := strings.Repeat("\x01", 140_000)
	prepared, err := NewPreparedRequest("agent", engine.WorkflowStartRequest{
		ID: "run-1", Workflow: workflow, TaskQueue: taskQueue,
		Input: &api.RunInput{AgentID: "agent", RunID: "run-1"},
		Memo:  memo,
	}, taskQueue)
	require.NoError(t, err)
	require.Nil(t, prepared.data)
	_, err = prepared.MarshalBinary()
	require.ErrorContains(t, err, "prepared run exceeds maximum stored size")
}

func TestPreparedRequestRoundTripPreservesEveryEngineField(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 34, 56, 789, time.UTC)
	memoValues := map[string]any{
		"binary": []byte("memo bytes"),
		"nil":    nil,
		"number": 42,
		"object": map[string]any{"name": "value"},
		"string": "memo value",
	}
	memo, err := EncodeMemo(memoValues)
	require.NoError(t, err)
	searchAttributes := map[string]any{
		"bool":         true,
		"datetime":     now,
		"double":       1.5,
		"int":          int64(42),
		"keyword":      "site-1",
		"keyword_list": []string{"one", "two"},
	}
	request := engine.WorkflowStartRequest{
		ID:        "run-1",
		Workflow:  "agent.workflow",
		TaskQueue: "agent.queue",
		Input: &api.RunInput{
			AgentID: "agent", RunID: "run-1", SessionID: "session-1", TurnID: "turn-1",
			Metadata: map[string]any{"count": 7, "nested": map[string]any{"enabled": true}},
		},
		RunTimeout:       3 * time.Minute,
		Memo:             memo,
		SearchAttributes: searchAttributes,
		RetryPolicy: engine.RetryPolicy{
			MaxAttempts: 4, InitialInterval: 2 * time.Second, BackoffCoefficient: 1.5,
		},
	}

	created, err := NewPreparedRequest("agent", request, "agent.queue")
	require.NoError(t, err)
	data, err := created.MarshalBinary()
	require.NoError(t, err)
	prepared, err := ParsePreparedRequest(data)
	require.NoError(t, err)
	require.Equal(t, created.Digest, prepared.Digest)
	require.Equal(t, "agent", prepared.AgentID)
	require.Equal(t, request.ID, prepared.Request.ID)
	require.Equal(t, request.Workflow, prepared.Request.Workflow)
	require.Equal(t, request.TaskQueue, prepared.Request.TaskQueue)
	require.Equal(t, "agent.queue", prepared.TaskQueueOverride)
	require.Equal(t, request.RunTimeout, prepared.Request.RunTimeout)
	require.Equal(t, request.RetryPolicy, prepared.Request.RetryPolicy)
	require.Equal(t, request.Input.AgentID, prepared.Request.Input.AgentID)
	require.Equal(t, request.Input.RunID, prepared.Request.Input.RunID)
	require.Equal(t, request.Input.SessionID, prepared.Request.Input.SessionID)
	require.Equal(t, request.Input.TurnID, prepared.Request.Input.TurnID)
	for name, value := range memoValues {
		expected, err := workflowcodec.NewDataConverter().ToPayload(value)
		require.NoError(t, err)
		require.True(t, proto.Equal(expected, MemoPayload(prepared.Request.Memo[name])), "memo %q payload changed", name)
	}
	numericPayload := MemoPayload(prepared.Request.Memo["number"])
	require.Equal(t, "json/plain", string(numericPayload.Metadata["encoding"]))
	require.Equal(t, []byte("42"), numericPayload.Data)
	require.Equal(t, true, prepared.Request.SearchAttributes["bool"])
	require.Equal(t, now, prepared.Request.SearchAttributes["datetime"])
	require.InDelta(t, 1.5, prepared.Request.SearchAttributes["double"], 0)
	require.Equal(t, int64(42), prepared.Request.SearchAttributes["int"])
	require.Equal(t, "site-1", prepared.Request.SearchAttributes["keyword"])
	require.Equal(t, []string{"one", "two"}, prepared.Request.SearchAttributes["keyword_list"])
}

func TestPreparedRequestRejectsInvalidEnvelope(t *testing.T) {
	created, err := NewPreparedRequest("agent", engine.WorkflowStartRequest{
		ID: "run-1", Workflow: "agent.workflow", TaskQueue: "agent.queue",
		Input: &api.RunInput{AgentID: "agent", RunID: "run-1", SessionID: "session-1"},
	}, "")
	require.NoError(t, err)
	valid, err := created.MarshalBinary()
	require.NoError(t, err)

	var envelope map[string]any
	require.NoError(t, json.Unmarshal(valid, &envelope))
	wrongVersion := mapsClone(envelope)
	wrongVersion["version"] = "goa-ai-prepared-run-v2"
	wrongVersionData, err := json.Marshal(wrongVersion)
	require.NoError(t, err)
	unknownField := mapsClone(envelope)
	unknownField["unexpected"] = true
	unknownFieldData, err := json.Marshal(unknownField)
	require.NoError(t, err)
	duplicateIDData := append([]byte(`{"id":"other",`), valid[1:]...)
	spacedData := append([]byte(" "), valid...)
	var mismatchedInput preparedRequestWire
	require.NoError(t, json.Unmarshal(valid, &mismatchedInput))
	var input *api.RunInput
	require.NoError(t, workflowcodec.NewDataConverter().FromPayload(preparedPayloadView(mismatchedInput.Input), &input))
	input.AgentID = otherRecipeValue
	payload, err := workflowcodec.NewDataConverter().ToPayload(input)
	require.NoError(t, err)
	mismatchedInput.Input = marshalPreparedPayload(payload)
	mismatchedInputData, err := json.Marshal(mismatchedInput)
	require.NoError(t, err)
	var nonCanonicalInput preparedRequestWire
	require.NoError(t, json.Unmarshal(valid, &nonCanonicalInput))
	nonCanonicalInput.Input.Metadata["unused"] = []byte("value")
	nonCanonicalInputData, err := json.Marshal(nonCanonicalInput)
	require.NoError(t, err)
	var mismatchedQueueOverride preparedRequestWire
	require.NoError(t, json.Unmarshal(valid, &mismatchedQueueOverride))
	mismatchedQueueOverride.TaskQueueOverride = "other.queue"
	mismatchedQueueOverrideData, err := json.Marshal(mismatchedQueueOverride)
	require.NoError(t, err)
	nullMemoData := bytes.Replace(valid, []byte(`"memo":[]`), []byte(`"memo":null`), 1)
	objectMemoData := bytes.Replace(valid, []byte(`"memo":[]`), []byte(`"memo":{}`), 1)
	omittedMemoData := bytes.Replace(valid, []byte(`,"memo":[]`), nil, 1)
	nullSearchData := bytes.Replace(valid, []byte(`"search_attributes":[]`), []byte(`"search_attributes":null`), 1)
	nullMetadataData := bytes.Replace(
		valid,
		[]byte(`"metadata":{"encoding":"anNvbi9wbGFpbg=="}`),
		[]byte(`"metadata":null`),
		1,
	)
	withSearch, err := NewPreparedRequest("agent", engine.WorkflowStartRequest{
		ID: "run-1", Workflow: "agent.workflow", TaskQueue: "agent.queue",
		Input:            &api.RunInput{AgentID: "agent", RunID: "run-1", SessionID: "session-1"},
		SearchAttributes: map[string]any{"site": "site-1"},
	}, "")
	require.NoError(t, err)
	withSearchData, err := withSearch.MarshalBinary()
	require.NoError(t, err)
	redundantSearchTypeData := bytes.Replace(
		withSearchData,
		[]byte(`"name":"site","payload"`),
		[]byte(`"name":"site","value_type":1,"payload"`),
		1,
	)

	tests := []struct {
		name      string
		data      []byte
		wantError string
	}{
		{name: "malformed", data: []byte(`{"version":`), wantError: "decode prepared run"},
		{name: "version", data: wrongVersionData, wantError: "unsupported version"},
		{name: "unknown field", data: unknownFieldData, wantError: "unknown field"},
		{name: "duplicate field", data: duplicateIDData, wantError: "not the canonical"},
		{name: "non-canonical whitespace", data: spacedData, wantError: "not the canonical"},
		{name: "input identity", data: mismatchedInputData, wantError: "input agent id does not match"},
		{name: "input payload", data: nonCanonicalInputData, wantError: "not the canonical"},
		{name: "queue override", data: mismatchedQueueOverrideData, wantError: "task queue override does not match"},
		{name: "null memo", data: nullMemoData, wantError: "not the canonical"},
		{name: "object memo", data: objectMemoData, wantError: "cannot unmarshal object"},
		{name: "omitted memo", data: omittedMemoData, wantError: "not the canonical"},
		{name: "null search attributes", data: nullSearchData, wantError: "not the canonical"},
		{name: "null payload metadata", data: nullMetadataData, wantError: "decode prepared run input"},
		{name: "redundant search type", data: redundantSearchTypeData, wantError: "unknown field"},
		{name: "trailing bytes", data: append(append([]byte(nil), valid...), []byte(" true")...), wantError: "trailing data"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParsePreparedRequest(test.data)
			require.ErrorContains(t, err, test.wantError)
		})
	}
}

func TestNewPreparedRequestRejectsInvalidLaunchValues(t *testing.T) {
	base := engine.WorkflowStartRequest{
		ID: "run-1", Workflow: "agent.workflow", TaskQueue: "agent.queue",
		Input: &api.RunInput{AgentID: "agent", RunID: "run-1", SessionID: "session-1"},
	}

	tests := []struct {
		name              string
		request           engine.WorkflowStartRequest
		taskQueueOverride string
		wantError         string
	}{
		{
			name: "queue override", request: base, taskQueueOverride: "other.queue",
			wantError: "task queue override does not match",
		},
		{
			name: "empty memo name",
			request: func() engine.WorkflowStartRequest {
				request := base
				request.Memo = map[string]engine.EncodedValue{"": {Data: []byte("value")}}
				return request
			}(),
			wantError: "workflow memo name is required",
		},
		{
			name: "empty search attribute name",
			request: func() engine.WorkflowStartRequest {
				request := base
				request.SearchAttributes = map[string]any{"": "value"}
				return request
			}(),
			wantError: "workflow search attribute name is required",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewPreparedRequest("agent", test.request, test.taskQueueOverride)
			require.ErrorContains(t, err, test.wantError)
		})
	}
}

func TestNewPreparedRequestRejectsOversizedPayloads(t *testing.T) {
	base := engine.WorkflowStartRequest{
		ID: "run-1", Workflow: "agent.workflow", TaskQueue: "agent.queue",
		Input: &api.RunInput{AgentID: "agent", RunID: "run-1", SessionID: "session-1"},
	}

	t.Run("input", func(t *testing.T) {
		request := base
		input := *base.Input
		input.Metadata = map[string]any{"value": strings.Repeat("x", engine.MaxPayloadBytes)}
		request.Input = &input

		_, err := NewPreparedRequest("agent", request, "")
		require.ErrorContains(t, err, "maximum aggregate size")
	})

	t.Run("memo", func(t *testing.T) {
		request := base
		request.Memo = map[string]engine.EncodedValue{
			"value": {Data: []byte(strings.Repeat("x", engine.MaxPayloadBytes))},
		}

		_, err := NewPreparedRequest("agent", request, "")
		require.ErrorContains(t, err, "maximum aggregate size")
	})

	t.Run("workflow name", func(t *testing.T) {
		request := base
		request.Workflow = strings.Repeat("w", engine.MaxPayloadBytes)

		_, err := NewPreparedRequest("agent", request, "")
		require.ErrorContains(t, err, "maximum aggregate size")
	})
}

func TestParsePreparedRequestValidatesCompleteEngineRequest(t *testing.T) {
	created, err := NewPreparedRequest("agent", engine.WorkflowStartRequest{
		ID: "run-1", Workflow: "agent.workflow", TaskQueue: "agent.queue",
		Input: &api.RunInput{AgentID: "agent", RunID: "run-1", SessionID: "session-1"},
	}, "")
	require.NoError(t, err)
	var wire preparedRequestWire
	createdData, err := created.MarshalBinary()
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(createdData, &wire))
	wire.SearchAttributes = []preparedSearchAttribute{{
		Name: "site",
		Payload: preparedPayload{
			Metadata: map[string][]byte{
				"encoding":                  []byte("json/plain"),
				searchAttributeTypeMetadata: []byte("INDEXED_VALUE_TYPE_KEYWORD"),
			},
			Data: []byte(`"` + strings.Repeat("x", engine.MaxPayloadBytes) + `"`),
		},
	}}
	data, err := json.Marshal(wire)
	require.NoError(t, err)

	_, err = ParsePreparedRequest(data)
	require.ErrorContains(t, err, "maximum aggregate size")
}

func TestParsePreparedRequestRejectsTimingBeforeDynamicValues(t *testing.T) {
	created, err := NewPreparedRequest("agent", engine.WorkflowStartRequest{
		ID: "run-1", Workflow: "agent.workflow", TaskQueue: "agent.queue",
		Input: &api.RunInput{AgentID: "agent", RunID: "run-1", SessionID: "session-1"},
	}, "")
	require.NoError(t, err)
	createdData, err := created.MarshalBinary()
	require.NoError(t, err)

	tests := []struct {
		name    string
		change  func(*preparedRequestWire)
		wantErr string
	}{
		{
			name: "negative timeout",
			change: func(wire *preparedRequestWire) {
				wire.RunTimeout = -time.Second
			},
			wantErr: "decode prepared run: workflow run timeout must not be negative",
		},
		{
			name: "partial retry policy",
			change: func(wire *preparedRequestWire) {
				wire.RetryPolicy.InitialInterval = time.Second
			},
			wantErr: "decode prepared run: workflow retry timing requires max attempts or unlimited attempts",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var wire preparedRequestWire
			require.NoError(t, json.Unmarshal(createdData, &wire))
			test.change(&wire)
			wire.Input.Data = []byte(`{"invalid"`)
			data, err := json.Marshal(wire)
			require.NoError(t, err)

			_, err = ParsePreparedRequest(data)
			require.EqualError(t, err, test.wantErr)
		})
	}
}

func TestParsePreparedRequestRejectsOversizedSearchListBeforeDecoding(t *testing.T) {
	created, err := NewPreparedRequest("agent", engine.WorkflowStartRequest{
		ID: "run-1", Workflow: "agent.workflow", TaskQueue: "agent.queue",
		Input: &api.RunInput{AgentID: "agent", RunID: "run-1", SessionID: "session-1"},
	}, "")
	require.NoError(t, err)
	var wire preparedRequestWire
	createdData, err := created.MarshalBinary()
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(createdData, &wire))
	wire.SearchAttributes = []preparedSearchAttribute{{
		Name: "sites",
		Payload: preparedPayload{
			Metadata: map[string][]byte{
				"encoding":                  []byte("json/plain"),
				searchAttributeTypeMetadata: []byte("INDEXED_VALUE_TYPE_KEYWORD_LIST"),
			},
			Data: []byte(`["` + strings.Repeat("x", engine.MaxPayloadBytes) + `"]`),
		},
	}}
	data, err := json.Marshal(wire)
	require.NoError(t, err)
	require.Less(t, len(data), maxPreparedRequestBytes)

	_, err = ParsePreparedRequest(data)
	require.ErrorContains(t, err, "decode prepared run search attribute \"sites\"")
	require.ErrorContains(t, err, "maximum aggregate size")
}

func TestParsePreparedRequestRejectsOversizedEnvelopeBeforeJSON(t *testing.T) {
	_, err := ParsePreparedRequest(make([]byte, maxPreparedRequestBytes+1))
	require.EqualError(t, err, fmt.Sprintf(
		"decode prepared run: stored value exceeds maximum size %d bytes",
		maxPreparedRequestBytes,
	))
}

func TestPreparedRequestRoundTripsUnlimitedRetryPolicy(t *testing.T) {
	request := engine.WorkflowStartRequest{
		ID: "run-1", Workflow: "agent.workflow", TaskQueue: "agent.queue",
		Input:       &api.RunInput{AgentID: "agent", RunID: "run-1", SessionID: "session-1"},
		RetryPolicy: engine.RetryPolicy{UnlimitedAttempts: true},
	}

	created, err := NewPreparedRequest("agent", request, "")
	require.NoError(t, err)
	createdData, err := created.MarshalBinary()
	require.NoError(t, err)
	prepared, err := ParsePreparedRequest(createdData)
	require.NoError(t, err)
	require.Equal(t, request.RetryPolicy, prepared.Request.RetryPolicy)
}

func TestPreparedRequestRoundTripsEncodedMemoValues(t *testing.T) {
	values := []any{
		nil,
		[]byte("bytes"),
		"string",
		true,
		int(1), int8(2), int16(3), int32(4), int64(5),
		uint(6), uint8(7), uint16(8), uint32(9), uint64(10),
		float32(1.25), float64(2.5),
		json.Number("9007199254740993"),
		map[string]any{"count": 11, "enabled": true},
		[]any{"one", 12},
		recipeMemoAlias("alias"),
		recipeMemoValue{Name: "value"},
		&commonpb.WorkflowExecution{WorkflowId: "workflow-1", RunId: "run-1"},
	}
	for _, value := range values {
		name := fmt.Sprintf("%T", value)
		t.Run(name, func(t *testing.T) {
			memoValues := map[string]any{"value": value}
			memo, err := EncodeMemo(memoValues)
			require.NoError(t, err)
			request := engine.WorkflowStartRequest{
				ID: "run-1", Workflow: "agent.workflow", TaskQueue: "agent.queue",
				Input: &api.RunInput{AgentID: "agent", RunID: "run-1", SessionID: "session-1"},
				Memo:  memo,
			}

			created, err := NewPreparedRequest("agent", request, "")
			require.NoError(t, err)
			createdData, err := created.MarshalBinary()
			require.NoError(t, err)
			prepared, err := ParsePreparedRequest(createdData)
			require.NoError(t, err)
			expected, err := workflowcodec.NewDataConverter().ToPayload(value)
			require.NoError(t, err)
			require.True(t, proto.Equal(expected, MemoPayload(prepared.Request.Memo["value"])))
		})
	}
}

func TestEncodeMemoOwnsPayloadBytes(t *testing.T) {
	source := []byte("value")
	encoded, err := EncodeMemo(map[string]any{"value": source})
	require.NoError(t, err)
	source[0] = 'X'

	first := MemoPayload(encoded["value"])
	first.Metadata["encoding"][0] = 'X'
	first.Data[0] = 'X'
	second := MemoPayload(encoded["value"])

	require.Equal(t, []byte("binary/plain"), second.Metadata["encoding"])
	require.Equal(t, []byte("value"), second.Data)
}

func TestEncodeMemoRejectsEmptyName(t *testing.T) {
	_, err := EncodeMemo(map[string]any{"": "value"})
	require.EqualError(t, err, "workflow memo name is required")
}

func TestEncodeMemoRejectsNilRawPayload(t *testing.T) {
	_, err := EncodeMemo(map[string]any{
		"value": converter.NewRawValue(nil),
	})
	require.EqualError(t, err, "validate workflow memo \"value\": workflow codec: raw payload is nil")
}

func TestEncodeSearchAttributesRejectsEmptyName(t *testing.T) {
	_, err := EncodeSearchAttributes(map[string]any{"": "value"})
	require.EqualError(t, err, "workflow search attribute name is required")
}

func TestSnapshotRequestPreservesNilMemo(t *testing.T) {
	snapshot, err := SnapshotRequest(engine.WorkflowStartRequest{
		ID:        "run-1",
		Workflow:  "agent.workflow",
		TaskQueue: "agent.queue",
		Input:     &api.RunInput{RunID: "run-1"},
	})
	require.NoError(t, err)
	require.Nil(t, snapshot.Request.Memo)
}

func TestSnapshotsValidateRequestsBeforeEncoding(t *testing.T) {
	oversized := strings.Repeat("x", engine.MaxPayloadBytes+1)
	_, err := SnapshotRequest(engine.WorkflowStartRequest{
		Workflow:  "agent.workflow",
		TaskQueue: "agent.queue",
		Input: &api.RunInput{
			Metadata: map[string]any{"payload": oversized},
		},
	})
	require.EqualError(t, err, "validate workflow start request: workflow id is required")

	_, err = SnapshotChildRequest(engine.ChildWorkflowRequest{
		ID:       "run-1",
		Workflow: "agent.workflow",
		Input: &api.RunInput{
			RunID:    "run-1",
			Metadata: map[string]any{"payload": oversized},
		},
	})
	require.EqualError(t, err, "validate child workflow request: child workflow task queue is required")
}

func TestSnapshotRequestIsStableAfterNormalization(t *testing.T) {
	memo, err := EncodeMemo(map[string]any{
		"structured": map[string]any{"count": int32(2), "enabled": true},
	})
	require.NoError(t, err)
	first, err := SnapshotRequest(engine.WorkflowStartRequest{
		ID: "run-1", Workflow: "agent.workflow", TaskQueue: "agent.queue",
		Input: &api.RunInput{
			RunID:    "run-1",
			ToolArgs: rawjson.Message(`{"site":"one","count":9007199254740993}`),
			Metadata: map[string]any{
				"number": int64(9007199254740993),
				"nested": map[string]any{"enabled": true, "values": []any{"one", int32(2)}},
			},
		},
		Memo:             memo,
		SearchAttributes: map[string]any{"attempt": int32(2), "site": "one"},
	})
	require.NoError(t, err)

	second, err := SnapshotRequest(first.Request)
	require.NoError(t, err)
	require.True(t, proto.Equal(first.InputPayload, second.InputPayload))
	require.Equal(t, first.Digest, second.Digest)
}

func TestStartRecipeRejectsInvalidUTF8AtEveryTextBoundary(t *testing.T) {
	invalid := string([]byte{0xff})
	_, err := EncodeMemo(map[string]any{invalid: "value"})
	require.ErrorContains(t, err, "invalid UTF-8")
	_, err = EncodeMemo(map[string]any{"value": invalid})
	require.ErrorContains(t, err, "invalid UTF-8")
	_, err = EncodeSearchAttributes(map[string]any{invalid: "value"})
	require.ErrorContains(t, err, "invalid UTF-8")
	_, err = EncodeSearchAttributes(map[string]any{"value": invalid})
	require.ErrorContains(t, err, "invalid UTF-8")
	_, err = SnapshotRequest(engine.WorkflowStartRequest{
		ID: invalid, Workflow: "agent.workflow", TaskQueue: "agent.queue",
		Input: &api.RunInput{RunID: invalid},
	})
	require.ErrorContains(t, err, "invalid UTF-8")
	_, err = SnapshotChildRequest(engine.ChildWorkflowRequest{
		ID: "run-1", Workflow: invalid, TaskQueue: "agent.queue",
		Input: &api.RunInput{RunID: "run-1"},
	})
	require.ErrorContains(t, err, "invalid UTF-8")
	_, err = SnapshotRequest(engine.WorkflowStartRequest{
		ID: "run-1", Workflow: "agent.workflow", TaskQueue: "agent.queue",
		Input: &api.RunInput{RunID: "run-1"},
		Memo: map[string]engine.EncodedValue{"value": {
			Metadata: map[string][]byte{invalid: []byte("value")},
		}},
	})
	require.ErrorContains(t, err, "invalid UTF-8")
}

func TestEncodedCollectionsBoundRepeatedSharedValues(t *testing.T) {
	shared := strings.Repeat("x", engine.MaxPayloadBytes/2+1)
	_, err := EncodeMemo(map[string]any{"first": shared, "second": shared})
	require.ErrorContains(t, err, "maximum aggregate size")

	_, err = EncodeSearchAttributes(map[string]any{"first": shared, "second": shared})
	require.ErrorContains(t, err, "maximum aggregate size")
}

func TestSnapshotsEnforceRootAndChildPayloadLimits(t *testing.T) {
	dataConverter := workflowcodec.NewDataConverter()
	baseInput := &api.RunInput{RunID: "run-1", Metadata: map[string]any{"payload": ""}}
	basePayload, err := dataConverter.ToPayload(baseInput)
	require.NoError(t, err)
	id, workflowName, taskQueue := "run-1", "agent.workflow", "agent.queue"
	fixedText := len(id) + len(workflowName) + len(taskQueue)
	rootReserve := rootRecipeMemoByteSize(t)

	rootPadding := engine.MaxPayloadBytes - fixedText - rootReserve - payloadByteSize(basePayload)
	require.Positive(t, rootPadding)
	rootInput := &api.RunInput{
		RunID:    "run-1",
		Metadata: map[string]any{"payload": strings.Repeat("x", rootPadding)},
	}
	rootPayload, err := dataConverter.ToPayload(rootInput)
	require.NoError(t, err)
	require.Equal(t, engine.MaxPayloadBytes,
		fixedText+rootReserve+payloadByteSize(rootPayload))
	_, err = SnapshotRequest(engine.WorkflowStartRequest{
		ID: id, Workflow: workflowName, TaskQueue: taskQueue, Input: rootInput,
	})
	require.NoError(t, err)
	rootInput.Metadata["payload"] = strings.Repeat("x", rootPadding+1)
	_, err = SnapshotRequest(engine.WorkflowStartRequest{
		ID: id, Workflow: workflowName, TaskQueue: taskQueue, Input: rootInput,
	})
	require.ErrorContains(t, err, "maximum aggregate size")

	childPadding := engine.MaxPayloadBytes - fixedText - payloadByteSize(basePayload)
	require.Greater(t, childPadding, rootPadding)
	childInput := &api.RunInput{
		RunID:    "run-1",
		Metadata: map[string]any{"payload": strings.Repeat("x", childPadding)},
	}
	childPayload, err := dataConverter.ToPayload(childInput)
	require.NoError(t, err)
	require.Equal(t, engine.MaxPayloadBytes, fixedText+payloadByteSize(childPayload))
	child, err := SnapshotChildRequest(engine.ChildWorkflowRequest{
		ID: id, Workflow: workflowName, TaskQueue: taskQueue, Input: childInput,
	})
	require.NoError(t, err)
	require.True(t, proto.Equal(childPayload, child.InputPayload))
	_, err = SnapshotRequest(engine.WorkflowStartRequest{
		ID: id, Workflow: workflowName, TaskQueue: taskQueue, Input: childInput,
	})
	require.ErrorContains(t, err, "maximum aggregate size")
	childInput.Metadata["payload"] = strings.Repeat("x", childPadding+1)
	_, err = SnapshotChildRequest(engine.ChildWorkflowRequest{
		ID: id, Workflow: workflowName, TaskQueue: taskQueue, Input: childInput,
	})
	require.ErrorContains(t, err, "maximum aggregate size")
}

func TestSnapshotRequestEnforcesAggregatePayloadLimit(t *testing.T) {
	dataConverter := workflowcodec.NewDataConverter()

	t.Run("input", func(t *testing.T) {
		baseInput := &api.RunInput{RunID: "run-1", Metadata: map[string]any{"payload": ""}}
		basePayload, err := dataConverter.ToPayload(baseInput)
		require.NoError(t, err)
		paddingSize := engine.MaxPayloadBytes - rootRecipeMemoByteSize(t) -
			payloadByteSize(basePayload) - len("run-1agent.workflowagent.queue")
		require.Positive(t, paddingSize)

		exactInput := &api.RunInput{
			RunID:    "run-1",
			Metadata: map[string]any{"payload": strings.Repeat("x", paddingSize)},
		}
		exactPayload, err := dataConverter.ToPayload(exactInput)
		require.NoError(t, err)
		require.Equal(t, engine.MaxPayloadBytes,
			rootRecipeMemoByteSize(t)+payloadByteSize(exactPayload)+len("run-1agent.workflowagent.queue"))
		_, err = SnapshotRequest(engine.WorkflowStartRequest{
			ID: "run-1", Workflow: "agent.workflow", TaskQueue: "agent.queue", Input: exactInput,
		})
		require.NoError(t, err)

		oversizedInput := &api.RunInput{
			RunID:    "run-1",
			Metadata: map[string]any{"payload": strings.Repeat("x", paddingSize+1)},
		}
		_, err = SnapshotRequest(engine.WorkflowStartRequest{
			ID: "run-1", Workflow: "agent.workflow", TaskQueue: "agent.queue", Input: oversizedInput,
		})
		require.ErrorContains(t, err, "payloads exceed maximum aggregate size")
	})

	t.Run("memo with input", func(t *testing.T) {
		input := &api.RunInput{RunID: "run-1"}
		inputPayload, err := dataConverter.ToPayload(input)
		require.NoError(t, err)
		memoSize := engine.MaxPayloadBytes - rootRecipeMemoByteSize(t) -
			payloadByteSize(inputPayload) - len("run-1agent.workflowagent.queuememo")
		require.Positive(t, memoSize)

		exactMemo := engine.EncodedValue{Data: make([]byte, memoSize)}
		_, err = SnapshotRequest(engine.WorkflowStartRequest{
			ID: "run-1", Workflow: "agent.workflow", TaskQueue: "agent.queue", Input: input,
			Memo: map[string]engine.EncodedValue{"memo": exactMemo},
		})
		require.NoError(t, err)

		exactMemo.Data = append(exactMemo.Data, 0)
		_, err = SnapshotRequest(engine.WorkflowStartRequest{
			ID: "run-1", Workflow: "agent.workflow", TaskQueue: "agent.queue", Input: input,
			Memo: map[string]engine.EncodedValue{"memo": exactMemo},
		})
		require.ErrorContains(t, err, "payloads exceed maximum aggregate size")
	})
}

// mapsClone gives each malformed-envelope case its own top-level map.
func mapsClone(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source))
	for name, value := range source {
		cloned[name] = value
	}
	return cloned
}

func TestDigestFramesMapEntriesAndIgnoresMapOrder(t *testing.T) {
	dataConverter := workflowcodec.NewDataConverter()
	inputSnapshot, err := snapshotRunInput(dataConverter, &api.RunInput{RunID: "run-1"})
	require.NoError(t, err)
	base := digestInput{
		Workflow: "agent.workflow", TaskQueue: "agent.queue", InputPayload: inputSnapshot.Payload,
	}

	first := base
	first.Memo = encodeMemoForTest(t, map[string]any{"a": "bc", "order": "stable"})
	firstDigest, err := digest(dataConverter, first)
	require.NoError(t, err)
	same := base
	same.Memo = encodeMemoForTest(t, map[string]any{"order": "stable", "a": "bc"})
	sameDigest, err := digest(dataConverter, same)
	require.NoError(t, err)
	require.Equal(t, firstDigest, sameDigest)

	ambiguousWithoutFraming := base
	ambiguousWithoutFraming.Memo = encodeMemoForTest(t, map[string]any{"ab": "c", "order": "stable"})
	ambiguousDigest, err := digest(dataConverter, ambiguousWithoutFraming)
	require.NoError(t, err)
	require.NotEqual(t, firstDigest, ambiguousDigest)
}

func TestDigestPreservesEncodedPayloadType(t *testing.T) {
	dataConverter := workflowcodec.NewDataConverter()
	inputSnapshot, err := snapshotRunInput(dataConverter, &api.RunInput{RunID: "run-1"})
	require.NoError(t, err)
	base := digestInput{
		Workflow: "agent.workflow", TaskQueue: "agent.queue", InputPayload: inputSnapshot.Payload,
	}
	binaryMemo := base
	binaryMemo.Memo = encodeMemoForTest(t, map[string]any{"value": []byte("same bytes")})
	binaryDigest, err := digest(dataConverter, binaryMemo)
	require.NoError(t, err)
	stringMemo := base
	stringMemo.Memo = encodeMemoForTest(t, map[string]any{"value": "same bytes"})
	stringDigest, err := digest(dataConverter, stringMemo)
	require.NoError(t, err)
	require.NotEqual(t, binaryDigest, stringDigest)
}

func TestDigestBindsEachSubmittedStartComponent(t *testing.T) {
	dataConverter := workflowcodec.NewDataConverter()
	inputSnapshot, err := snapshotRunInput(dataConverter, &api.RunInput{RunID: "run-1"})
	require.NoError(t, err)
	searchAttributes, err := EncodeSearchAttributes(map[string]any{"site": "one"})
	require.NoError(t, err)
	base := digestInput{
		Workflow:         "workflow",
		TaskQueue:        "queue",
		InputPayload:     inputSnapshot.Payload,
		RunTimeout:       time.Minute,
		RetryPolicy:      engine.RetryPolicy{MaxAttempts: 2, InitialInterval: time.Second, BackoffCoefficient: 2},
		Memo:             encodeMemoForTest(t, map[string]any{"memo": "one"}),
		SearchAttributes: searchAttributes,
	}
	baseDigest, err := digest(dataConverter, base)
	require.NoError(t, err)

	changedInput, err := snapshotRunInput(dataConverter, &api.RunInput{RunID: "run-2"})
	require.NoError(t, err)
	changedSearch, err := EncodeSearchAttributes(map[string]any{"site": "two"})
	require.NoError(t, err)
	variants := []digestInput{
		func() digestInput { value := base; value.Workflow = otherRecipeValue; return value }(),
		func() digestInput { value := base; value.TaskQueue = otherRecipeValue; return value }(),
		func() digestInput { value := base; value.InputPayload = changedInput.Payload; return value }(),
		func() digestInput { value := base; value.RunTimeout = 2 * time.Minute; return value }(),
		func() digestInput {
			value := base
			value.RetryPolicy = engine.RetryPolicy{MaxAttempts: 3, InitialInterval: time.Second, BackoffCoefficient: 2}
			return value
		}(),
		func() digestInput {
			value := base
			value.Memo = encodeMemoForTest(t, map[string]any{"memo": "two"})
			return value
		}(),
		func() digestInput { value := base; value.SearchAttributes = changedSearch; return value }(),
	}
	for index, variant := range variants {
		digestValue, err := digest(dataConverter, variant)
		require.NoError(t, err)
		require.NotEqual(t, baseDigest, digestValue, "variant %d", index)
	}
}

// encodeMemoForTest fails the current test when Temporal cannot encode the
// fixture as values stored with a workflow execution.
func encodeMemoForTest(t *testing.T, values map[string]any) map[string]engine.EncodedValue {
	t.Helper()
	encoded, err := EncodeMemo(values)
	require.NoError(t, err)
	return encoded
}

// payloadByteSize counts the exact bytes enforced by the workflow codec.
func payloadByteSize(payload *commonpb.Payload) int {
	total := len(payload.Data)
	for name, value := range payload.Metadata {
		total += len(name) + len(value)
	}
	return total
}

// rootRecipeMemoByteSize counts the exact memo name and digest payload that a
// root workflow adapter adds after the recipe digest is computed.
func rootRecipeMemoByteSize(t *testing.T) int {
	t.Helper()
	payload, err := workflowcodec.NewDataConverter().ToPayload(make([]byte, 32))
	require.NoError(t, err)
	return len(MemoKey) + payloadByteSize(payload)
}
