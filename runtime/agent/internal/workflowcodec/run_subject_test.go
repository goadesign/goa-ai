// These tests retain generated run subjects through every common workflow
// envelope and prove that malformed saved subjects cannot lose their invalid
// fields during generated union decoding.
package workflowcodec

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

func TestWorkflowSubjectRoundTrip(t *testing.T) {
	codec := NewDataConverter()
	for _, subject := range []genrunidentity.RunSubject{
		{Executor: genrunidentity.NewRunExecutorAgent("agent")},
		{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "catalog.lookup"})},
		{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "secondary", Tool: "catalog.lookup"})},
	} {
		for _, input := range []any{
			api.RunInput{Subject: subject, RunID: "run"},
			api.RunOutput{Subject: subject, RunID: "run"},
			api.ToolInput{Subject: subject, RunID: "run", Invocation: genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindTool(&genregistry.ToolInvocation{RunID: "issuer", ToolCallID: "call"})}},
			api.ToolCall{Subject: subject, RunID: "run"},
			runlog.ActivityInput{Subject: subject, RunID: "run"},
			storage.SeedDeclaration{Subject: subject, RunID: "run", Origin: genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindRoot(&genregistry.RootInvocation{RunID: "run"})}},
			storage.PreparationOperation{Subject: subject, RunID: "run"},
		} {
			t.Run(reflect.TypeOf(input).String()+"/"+string(subject.Executor.Kind()), func(t *testing.T) {
				payload, err := codec.ToPayload(input)
				require.NoError(t, err)
				saved := reflect.New(reflect.TypeOf(input))
				require.NoError(t, codec.FromPayload(payload, saved.Interface()))
				assert.Equal(t, input, saved.Elem().Interface())
				assert.Contains(t, string(payload.Data), `"Subject":{"executor":`)
				var bound Budget
				require.NoError(t, bound.AddEncodedSource(input))
				assert.GreaterOrEqual(t, bound.used, len(payload.Data))
			})
		}
	}
}

func TestWorkflowSubjectRejectsMalformedSavedIdentity(t *testing.T) {
	codec := NewDataConverter()
	for _, subject := range []string{
		`null`, `{}`, `{"executor":null}`, `{"executor":{"type":"agent","value":""}}`,
		`{"executor":{"type":"service_tool","value":null}}`,
		`{"executor":{"type":"service_tool","value":{"registry":"","tool":"catalog.lookup"}}}`,
		`{"executor":{"type":"service_tool","value":{"registry":"primary","tool":"lookup"}}}`,
		`{"executor":{"type":"service_tool","value":{"registry":"primary","tool":"catalog.lookup","unknown":1}}}`,
		`{"executor":{"type":"agent","value":"agent","service_tool":{}}}`,
		`{"executor":{"type":"agent","value":"agent"},"unknown":1}`,
		`{"executor":{"type":"agent","value":"agent","value":"other"}}`,
		`{"executor":{"type":"agent","value":"agent"},"executor":{"type":"agent","value":"other"}}`,
	} {
		for _, field := range []string{"Subject", "subject"} {
			payload := &commonpb.Payload{Metadata: map[string][]byte{converter.MetadataEncoding: []byte(converter.MetadataEncodingJSON)}, Data: []byte(`{"` + field + `":` + subject + `}`)}
			var input api.RunInput
			require.Error(t, codec.FromPayload(payload, &input), string(payload.Data))
		}
	}
	for _, body := range []string{`{}`, `{"AgentID":"agent"}`} {
		payload := &commonpb.Payload{Metadata: map[string][]byte{converter.MetadataEncoding: []byte(converter.MetadataEncodingJSON)}, Data: []byte(body)}
		var input api.RunInput
		require.Error(t, codec.FromPayload(payload, &input))
	}
}

func TestWorkflowSubjectRejectsInvalidSourceAndCountsServiceBytes(t *testing.T) {
	codec := NewDataConverter()
	for _, subject := range []genrunidentity.RunSubject{
		{}, {Executor: genrunidentity.NewRunExecutorAgent("")},
		{Executor: genrunidentity.NewRunExecutorServiceTool(nil)},
		{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Tool: "catalog.lookup"})},
		{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "lookup"})},
	} {
		_, err := codec.ToPayload(api.RunInput{Subject: subject})
		require.Error(t, err)
		var bound Budget
		require.Error(t, bound.AddEncodedSource(subject))
	}
	oversized := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: strings.Repeat("x", engine.MaxPayloadBytes+1), Tool: "catalog.lookup"})}
	_, err := codec.ToPayload(api.RunInput{Subject: oversized})
	require.ErrorContains(t, err, "maximum aggregate size")
	var bound Budget
	require.ErrorContains(t, bound.AddEncodedSource(oversized), "exceeds conservative encoded-size bound")
}

func TestWorkflowSubjectValidationInsideCollections(t *testing.T) {
	type nested struct{ Runs map[string][]api.RunInput }
	codec := NewDataConverter()
	body := `{"Runs":{"one":[{"Subject":{"executor":{"type":"agent","value":"agent","unknown":1}}}]}}`
	payload := &commonpb.Payload{Metadata: map[string][]byte{converter.MetadataEncoding: []byte(converter.MetadataEncodingJSON)}, Data: []byte(body)}
	var output nested
	require.Error(t, codec.FromPayload(payload, &output))
	valid := nested{Runs: map[string][]api.RunInput{"one": {{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("agent")}}}}}
	encoded, err := codec.ToPayload(valid)
	require.NoError(t, err)
	require.NoError(t, codec.FromPayload(encoded, &output))
	require.Equal(t, valid, output)
}

func TestDecodeJSONPreservesStoredValuesBeyondTransportLimit(t *testing.T) {
	type savedRecord struct {
		Subject genrunidentity.RunSubject
		Text    string
	}
	original := savedRecord{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("agent")}, Text: strings.Repeat("x", engine.MaxPayloadBytes+1)}
	data, err := json.Marshal(original)
	require.NoError(t, err)
	var decoded savedRecord
	require.NoError(t, DecodeJSON(data, &decoded))
	require.Equal(t, original, decoded)
	payload := &commonpb.Payload{Metadata: map[string][]byte{converter.MetadataEncoding: []byte(converter.MetadataEncodingJSON)}, Data: data}
	require.Error(t, NewDataConverter().FromPayload(payload, &decoded))
}

func TestDecodeJSONRejectsMissingAndMalformedSubjects(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"Subject":null}`,
		`{"Subject":{"executor":{"type":"agent","value":"agent","extra":true}}}`,
		`{"Subject":{"executor":{"type":"agent","value":"agent","service_tool":{}}}}`,
	} {
		var decoded api.RunInput
		require.Error(t, DecodeJSON([]byte(body), &decoded), body)
	}
}

func TestDecodeJSONValidatesSelectedEmbeddedSubject(t *testing.T) {
	type subjectFields struct{ Subject genrunidentity.RunSubject }
	type textFields struct{ Subject string }
	type deepText struct{ textFields }
	type embeddedRecord struct {
		subjectFields
		deepText
		SUBJECT string
	}
	invalid := []byte(`{"Subject":{"executor":{"type":"agent","value":"agent","unknown":true}}}`)
	var value embeddedRecord
	require.ErrorContains(t, DecodeJSON(invalid, &value), "saved run subject")
	valid := []byte(`{"Subject":{"executor":{"type":"agent","value":"agent"}},"SUBJECT":"text"}`)
	require.NoError(t, DecodeJSON(valid, &value))
	require.Equal(t, "text", value.SUBJECT)
	require.Equal(t, genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("agent")}, value.Subject)
}

func TestSavedRecordSubjectRoundTrip(t *testing.T) {
	for _, subject := range []genrunidentity.RunSubject{
		{Executor: genrunidentity.NewRunExecutorAgent("agent")},
		{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "catalog.lookup"})},
	} {
		for _, original := range []any{
			runlog.Event{Subject: subject, RunID: "run"},
			session.RunStart{Subject: subject, RunID: "run"},
			session.RunMeta{Subject: subject, RunID: "run", Origin: genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindRoot(&genregistry.RootInvocation{RunID: "run"})}},
		} {
			t.Run(reflect.TypeOf(original).String()+"/"+string(subject.Executor.Kind()), func(t *testing.T) {
				data, err := json.Marshal(original)
				require.NoError(t, err)
				decoded := reflect.New(reflect.TypeOf(original))
				require.NoError(t, DecodeJSON(data, decoded.Interface()))
				assert.Equal(t, original, decoded.Elem().Interface())
				invalid := strings.Replace(string(data), `"executor":`, `"unknown":true,"executor":`, 1)
				require.ErrorContains(t, DecodeJSON([]byte(invalid), decoded.Interface()), "saved run subject")
			})
		}
	}
}
