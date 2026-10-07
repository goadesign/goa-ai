package workflowcodec

// These tests exercise the private planner certificate through the shared
// engine converter, including source budgets and the declared legacy reader.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
)

func TestPlanOutputCertificateRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message string
		cause   error
	}{
		{name: "nil cause", message: "réessayez 世界"},
		{name: "empty cause", cause: errors.New("")},
		{name: "cause only", cause: errors.New("réseau indisponible 世界")},
		{name: "message and cause", message: "request failed", cause: errors.New("connection closed")},
		{name: "long valid diagnostic", cause: errors.New(strings.Repeat("é", 5000))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failure := model.NewProviderError("synthetic", "complete", 503, model.ProviderErrorKindUnavailable,
				"capacity", tc.message, "request-1", true, tc.cause)
			source := &api.PlanActivityOutput{PublicationBatchID: "publication-1", ProviderFailure: failure}
			codec := NewDataConverter()
			payload, err := codec.ToPayload(source)
			require.NoError(t, err)
			assert.Equal(t, planOutputEncoding, string(payload.Metadata[converter.MetadataEncoding]))
			var decoded *api.PlanActivityOutput
			require.NoError(t, codec.FromPayload(payload, &decoded))
			actual := decoded.ProviderFailure
			require.NotNil(t, actual)
			assert.NotSame(t, failure, actual)
			assert.Equal(t, failure.Provider(), actual.Provider())
			assert.Equal(t, failure.Operation(), actual.Operation())
			assert.Equal(t, failure.HTTPStatus(), actual.HTTPStatus())
			assert.Equal(t, failure.Kind(), actual.Kind())
			assert.Equal(t, failure.Code(), actual.Code())
			assert.Equal(t, failure.Message(), actual.Message())
			assert.Equal(t, failure.RequestID(), actual.RequestID())
			assert.Equal(t, failure.Retryable(), actual.Retryable())
			assert.Equal(t, failure.Error(), actual.Error())
			if tc.cause == nil {
				assert.NoError(t, actual.Unwrap())
			} else {
				require.Error(t, actual.Unwrap())
				assert.Equal(t, tc.cause.Error(), actual.Unwrap().Error())
				assert.NotSame(t, tc.cause, actual.Unwrap())
			}
		})
	}
}

func TestPlanOutputCertificateRejectsInvalidSourceAndSavedFacts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*planOutputRecord)
		want   string
	}{
		{name: "missing provider", change: func(r *planOutputRecord) { r.ProviderFailure.Provider = "" }, want: "typed temporary"},
		{name: "nonretryable", change: func(r *planOutputRecord) { r.ProviderFailure.Retryable = false }, want: "typed temporary"},
		{name: "unknown kind", change: func(r *planOutputRecord) { r.ProviderFailure.Kind = "other" }, want: "typed temporary"},
		{name: "status below", change: func(r *planOutputRecord) { r.ProviderFailure.HTTPStatus = 99 }, want: "HTTP status"},
		{name: "status above", change: func(r *planOutputRecord) { r.ProviderFailure.HTTPStatus = 600 }, want: "HTTP status"},
		{name: "cause without presence", change: func(r *planOutputRecord) { r.ProviderFailure.CausePresent = false }, want: "without a cause"},
		{name: "accepted result", change: func(r *planOutputRecord) { r.Result = &api.PlanResult{} }, want: "another result variant"},
		{name: "selected history", change: func(r *planOutputRecord) { r.HistoryContext = &api.HistoryContext{} }, want: "another result variant"},
		{name: "published text", change: func(r *planOutputRecord) { r.PublishedAssistantText = "visible" }, want: "another result variant"},
		{name: "planning failure", change: func(r *planOutputRecord) { r.PlanningFailure = &run.Failure{} }, want: "another result variant"},
		{name: "transcript", change: func(r *planOutputRecord) { r.Transcript = []*model.Message{{}} }, want: "another result variant"},
		{name: "output rejection", change: func(r *planOutputRecord) { r.OutputContractFailure = &api.OutputContractFailure{} }, want: "another result variant"},
		{name: "model recovery", change: func(r *planOutputRecord) { r.ModelInvocationRecovery = &api.ModelInvocationRecovery{} }, want: "another result variant"},
		{name: "catalog", change: func(r *planOutputRecord) { r.RecoveryCatalog = &api.RecoveryCatalog{} }, want: "another result variant"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := planOutputView(certificateOutput())
			tc.change(record)
			require.ErrorContains(t, preflightValues(record), tc.want)
			data, err := json.Marshal(record)
			require.NoError(t, err)
			var decoded *api.PlanActivityOutput
			err = NewDataConverter().FromPayload(&commonpb.Payload{
				Metadata: map[string][]byte{converter.MetadataEncoding: []byte(planOutputEncoding)}, Data: data,
			}, &decoded)
			require.ErrorContains(t, err, tc.want)
			assert.Nil(t, decoded)
		})
	}
	for _, status := range []int{0, 100, 599} {
		output := certificateOutput()
		output.ProviderFailure = model.NewProviderError("synthetic", "", status,
			model.ProviderErrorKindUnavailable, "", "", "", true, nil)
		_, err := Copy(NewDataConverter(), output)
		require.NoError(t, err)
	}
}

func TestPlanOutputCertificateRejectsInvalidTextAndDiagnostic(t *testing.T) {
	for _, field := range []string{"Provider", "Operation", "Code", "Message", "RequestID", "Cause", "Diagnostic"} {
		t.Run(field, func(t *testing.T) {
			record := planOutputView(certificateOutput())
			failure := record.ProviderFailure
			text := map[string]*string{
				"Provider": &failure.Provider, "Operation": &failure.Operation,
				"Code": &failure.Code, "Message": &failure.Message, "RequestID": &failure.RequestID,
				"Cause": &failure.Cause, "Diagnostic": &failure.Diagnostic,
			}
			*text[field] = "\xff"
			require.ErrorContains(t, preflightValues(record), "UTF-8")
		})
	}
	codec := NewDataConverter()
	payload, err := codec.ToPayload(certificateOutput())
	require.NoError(t, err)
	for _, tc := range []struct {
		name       string
		diagnostic string
		unknown    bool
		want       string
	}{
		{name: "invented diagnostic", diagnostic: "invented", want: "diagnostic does not match"},
		{name: "empty diagnostic", want: "diagnostic does not match"},
		{name: "unknown field", unknown: true, want: "unknown or duplicate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(payload.Data, &fields))
			var certificate map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(fields["ProviderFailure"], &certificate))
			if tc.unknown {
				certificate["Unexpected"] = []byte("true")
			} else {
				certificate["Diagnostic"], err = json.Marshal(tc.diagnostic)
				require.NoError(t, err)
			}
			fields["ProviderFailure"], err = json.Marshal(certificate)
			require.NoError(t, err)
			data, err := json.Marshal(fields)
			require.NoError(t, err)
			var decoded api.PlanActivityOutput
			require.ErrorContains(t, codec.FromPayload(&commonpb.Payload{Metadata: payload.Metadata, Data: data}, &decoded), tc.want)
		})
	}
	payload.Data = append(payload.Data, []byte(" {}")...)
	var decoded api.PlanActivityOutput
	require.ErrorContains(t, codec.FromPayload(payload, &decoded), "trailing")
}

func TestPlanOutputCertificateRequiresEverySavedFact(t *testing.T) {
	codec := NewDataConverter()
	payload, err := codec.ToPayload(certificateOutput())
	require.NoError(t, err)
	var output map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(payload.Data, &output))
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(output["ProviderFailure"], &fields))
	for name, original := range fields {
		for _, null := range []bool{false, true} {
			if null {
				fields[name] = []byte("null")
			} else {
				delete(fields, name)
			}
			output["ProviderFailure"], err = json.Marshal(fields)
			require.NoError(t, err)
			payload.Data, err = json.Marshal(output)
			require.NoError(t, err)
			var decoded api.PlanActivityOutput
			require.Error(t, codec.FromPayload(payload, &decoded), "field %s null %v", name, null)
			fields[name] = original
		}
	}
	certificate, err := json.Marshal(fields)
	require.NoError(t, err)
	output["ProviderFailure"] = append([]byte(`{"Retryable":false,`), certificate[1:]...)
	payload.Data, err = json.Marshal(output)
	require.NoError(t, err)
	var decoded api.PlanActivityOutput
	require.ErrorContains(t, codec.FromPayload(payload, &decoded), "duplicate")
}

func TestPlanOutputCertificateSharesAggregatePreflight(t *testing.T) {
	output := certificateOutput()
	failure := output.ProviderFailure
	want := len(output.PublicationBatchID) + len(failure.Provider()) + len(failure.Operation()) +
		len(failure.Kind()) + len(failure.Code()) + len(failure.Message()) +
		len(failure.RequestID()) + len(failure.Unwrap().Error()) + len(failure.Error())
	var budget Budget
	require.NoError(t, budget.AddSource(output))
	assert.Equal(t, want, budget.used)
	for _, extra := range []int{-1, 0, 1} {
		var combined Budget
		err := combined.AddSource(output, strings.Repeat("x", engine.MaxPayloadBytes-want+extra))
		if extra <= 0 {
			require.NoError(t, err)
		} else {
			require.ErrorContains(t, err, "maximum aggregate size")
		}
	}
	large := strings.Repeat("x", engine.MaxPayloadBytes/3)
	output.ProviderFailure = model.NewProviderError("synthetic", "", 0, model.ProviderErrorKindUnavailable,
		"", large, "", true, errors.New(large))
	_, err := NewDataConverter().ToPayload(output)
	require.ErrorContains(t, err, "maximum aggregate size")

	output = certificateOutput()
	output.PublicationBatchID = strings.Repeat("x", engine.MaxPayloadBytes/2)
	_, err = NewDataConverter().ToPayloads(output, strings.Repeat("x", engine.MaxPayloadBytes/2))
	require.ErrorContains(t, err, "maximum aggregate size")
	var visits Budget
	require.NoError(t, visits.AddSource(certificateOutput()))
	visits.sourceVisits = maxWorkflowJSONVisits - visits.sourceVisits + 1
	require.ErrorContains(t, visits.AddSource(certificateOutput()), "maximum visited")

	var nested any = "leaf"
	for range maxWorkflowJSONDepth {
		nested = []any{nested}
	}
	output = certificateOutput()
	output.PlannerEvents = []*api.PlannerEventRecord{{}}
	_, err = NewDataConverter().ToPayloads(output, nested)
	require.ErrorContains(t, err, "maximum depth")
}

func TestPlanOutputCertificatePayloadMetadataSharesByteLimit(t *testing.T) {
	codec := NewDataConverter()
	payload, err := codec.ToPayload(certificateOutput())
	require.NoError(t, err)
	size := len(payload.Data) + len(converter.MetadataEncoding) + len(planOutputEncoding) + len("padding")
	payload.Metadata["padding"] = make([]byte, engine.MaxPayloadBytes-size)
	var decoded api.PlanActivityOutput
	require.NoError(t, codec.FromPayload(payload, &decoded))
	payload.Metadata["padding"] = append(payload.Metadata["padding"], 0)
	require.ErrorContains(t, codec.FromPayload(payload, &decoded), "maximum aggregate size")
}

func TestPlanOutputCertificateBoundsEscapingBeforeRendering(t *testing.T) {
	output := certificateOutput()
	output.ProviderFailure = model.NewProviderError("synthetic", "", 0,
		model.ProviderErrorKindUnavailable, "", "", "", true,
		errors.New(strings.Repeat("\x00", engine.MaxPayloadBytes/12+1)))
	record := planOutputView(output)
	require.ErrorContains(t, preflightValues(record), "conservative encoded-size")
	assert.Empty(t, record.ProviderFailure.Diagnostic)
	_, err := NewDataConverter().ToPayload(output)
	require.ErrorContains(t, err, "conservative encoded-size")

	output = certificateOutput()
	var budget Budget
	require.NoError(t, budget.addEncodedPayload(output))
	remaining := engine.MaxPayloadBytes - budget.used -
		len(converter.MetadataEncoding) - len(converter.MetadataEncodingJSON) - 2
	for _, extra := range []int{-1, 0, 1} {
		err := preflightValues(output, strings.Repeat("x", remaining+extra))
		if extra <= 0 {
			require.NoError(t, err)
		} else {
			require.ErrorContains(t, err, "conservative encoded-size")
		}
	}
}

func TestPlanOutputVersionSelectsExactLegacyReader(t *testing.T) {
	codec := NewDataConverter()
	for _, text := range []string{
		`null`,
		`{"PublicationBatchID":"old","Result":{"FinalResponse":{"Message":{"role":"assistant","parts":[{"kind":"text","text":"done"}]}}}}`,
		`{"PublicationBatchID":"old","ProviderFailure":null}`,
	} {
		payload := &commonpb.Payload{Metadata: map[string][]byte{converter.MetadataEncoding: []byte(converter.MetadataEncodingJSON)}, Data: []byte(text)}
		var expected, actual *api.PlanActivityOutput
		require.NoError(t, json.Unmarshal(payload.Data, &expected))
		require.NoError(t, codec.FromPayload(payload, &actual))
		assert.Equal(t, expected, actual)
	}
	for _, text := range []string{
		`{"ProviderFailure":{"kind":"unavailable","provider":"synthetic","retryable":true,"debug_message":"old summary"}}`,
		`{"ProviderFailure":{}}`,
	} {
		var output api.PlanActivityOutput
		err := codec.FromPayload(&commonpb.Payload{Metadata: map[string][]byte{converter.MetadataEncoding: []byte(converter.MetadataEncodingJSON)}, Data: []byte(text)}, &output)
		require.ErrorContains(t, err, "legacy json/plain planner provider certificate is unsupported")
	}
	newPayload, err := codec.ToPayload(certificateOutput())
	require.NoError(t, err)
	newPayload.Metadata[converter.MetadataEncoding] = []byte(converter.MetadataEncodingJSON)
	var output api.PlanActivityOutput
	require.ErrorContains(t, codec.FromPayload(newPayload, &output), "unknown field")
	newPayload.Metadata[converter.MetadataEncoding] = []byte("json/goa-ai-plan-output-v999")
	require.Error(t, codec.FromPayload(newPayload, &output))
}

func TestPlanOutputNormalVariantsRoundTrip(t *testing.T) {
	for _, output := range []api.PlanActivityOutput{
		{Result: &api.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: model.ConversationRoleAssistant}}}},
		{PlanningFailure: &run.Failure{Message: "failed"}, PublishedAssistantText: "visible"},
		{OutputContractFailure: &api.OutputContractFailure{Reason: "rejected"}},
		{ModelInvocationRecovery: &api.ModelInvocationRecovery{}},
	} {
		output.PublicationBatchID = "publication"
		actual, err := Copy(NewDataConverter(), output)
		require.NoError(t, err)
		assert.Equal(t, output, actual)
	}
}

func TestProviderCertificateCannotUseOrdinaryJSONEncoding(t *testing.T) {
	output := certificateOutput()
	for _, value := range []any{output.ProviderFailure, struct{ Output api.PlanActivityOutput }{output}} {
		_, err := NewDataConverter().ToPayload(value)
		require.ErrorContains(t, err, "owning")
	}
}

func certificateOutput() api.PlanActivityOutput {
	return api.PlanActivityOutput{
		PublicationBatchID: "publication-1",
		ProviderFailure: model.NewProviderError("synthetic", "complete", 503, model.ProviderErrorKindUnavailable,
			"capacity", "", "request-1", true, errors.New("connection closed")),
	}
}
