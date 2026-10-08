package workflowcodec

// Planner activities return one exclusive result. This file owns the saved
// provider certificate, retaining provider facts and diagnostic text without
// encoding arbitrary SDK errors. Only the encoding tag selects the old reader.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/run"
)

type (
	planOutputPayloadConverter struct{}

	// planOutputRecord retains the established planner-output fields by
	// reference. Creating this view does not copy messages or event collections.
	//nolint:tagliatelle // Temporal output fields retain their declared Go names.
	planOutputRecord struct {
		PublicationBatchID      string
		Result                  *api.PlanResult
		Transcript              []*model.Message
		HistoryContext          *api.HistoryContext `json:",omitempty"`
		PublishedAssistantText  string              `json:",omitempty"`
		Usage                   model.TokenUsage
		PlannerEvents           []*api.PlannerEventRecord
		RecoveryCatalog         *api.RecoveryCatalog         `json:",omitempty"`
		OutputContractFailure   *api.OutputContractFailure   `json:",omitempty"`
		PlanningFailure         *run.Failure                 `json:",omitempty"`
		ProviderFailure         *providerFailureRecord       `json:",omitempty"`
		ModelInvocationRecovery *api.ModelInvocationRecovery `json:",omitempty"`
	}

	// legacyPlanOutput recognizes an old certificate only to reject it. Its
	// summary cannot reconstruct the provider facts the current contract needs.
	//nolint:tagliatelle // The historical reader must retain its original field names.
	legacyPlanOutput struct {
		PublicationBatchID      string
		Result                  *api.PlanResult
		Transcript              []*model.Message
		HistoryContext          *api.HistoryContext `json:",omitempty"`
		PublishedAssistantText  string              `json:",omitempty"`
		Usage                   model.TokenUsage
		PlannerEvents           []*api.PlannerEventRecord
		RecoveryCatalog         *api.RecoveryCatalog         `json:",omitempty"`
		OutputContractFailure   *api.OutputContractFailure   `json:",omitempty"`
		PlanningFailure         *run.Failure                 `json:",omitempty"`
		ProviderFailure         *run.Failure                 `json:",omitempty"`
		ModelInvocationRecovery *api.ModelInvocationRecovery `json:",omitempty"`
	}

	providerFailureRecord struct {
		Provider     string
		Operation    string
		HTTPStatus   int
		Kind         model.ProviderErrorKind
		Code         string
		Message      string
		RequestID    string
		Retryable    bool
		CausePresent bool
		Cause        string
		Diagnostic   string
	}
)

const planOutputEncoding = "json/goa-ai-plan-output-v2"

func (*planOutputPayloadConverter) Encoding() string { return planOutputEncoding }

func (*planOutputPayloadConverter) ToPayload(value any) (*commonpb.Payload, error) {
	if !isPlanOutputType(reflect.TypeOf(value)) {
		return nil, nil
	}
	source := reflect.ValueOf(value)
	for source.Kind() == reflect.Pointer {
		if source.IsNil() {
			return &commonpb.Payload{
				Metadata: map[string][]byte{converter.MetadataEncoding: []byte(planOutputEncoding)},
				Data:     []byte("null"),
			}, nil
		}
		source = source.Elem()
	}
	output := source.Interface().(api.PlanActivityOutput)
	record := planOutputView(output)
	// Repeat the source check on this exact view before rendering its
	// diagnostic. A provider cause supplies text, never a serializable object.
	if err := preflightValues(record); err != nil {
		return nil, err
	}
	if record.ProviderFailure != nil {
		record.ProviderFailure.Diagnostic = strings.Join(record.ProviderFailure.diagnosticParts(), "")
	}
	data, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("encode planner output: %w", err)
	}
	return &commonpb.Payload{
		Metadata: map[string][]byte{converter.MetadataEncoding: []byte(planOutputEncoding)},
		Data:     data,
	}, nil
}

func (*planOutputPayloadConverter) FromPayload(payload *commonpb.Payload, valuePtr any) error {
	if !isPlanOutputType(reflect.TypeOf(valuePtr)) {
		return fmt.Errorf("planner output encoding requires a PlanActivityOutput destination, got %T", valuePtr)
	}
	var record *planOutputRecord
	if err := new(strictJSONPayloadConverter).FromPayload(payload, &record); err != nil {
		return err
	}
	if err := preflightValues(record); err != nil {
		return err
	}
	var output *api.PlanActivityOutput
	if record != nil {
		output = record.output()
		if failure := record.ProviderFailure; failure != nil {
			if failure.Diagnostic != strings.Join(failure.diagnosticParts(), "") {
				return errors.New("planner provider certificate diagnostic does not match its facts and cause")
			}
			var cause error
			if failure.CausePresent {
				cause = errors.New(failure.Cause)
			}
			output.ProviderFailure = model.NewProviderError(
				failure.Provider, failure.Operation, failure.HTTPStatus, failure.Kind,
				failure.Code, failure.Message, failure.RequestID, failure.Retryable, cause,
			)
		}
	}
	return assignPlanOutput(valuePtr, output)
}

func (*planOutputPayloadConverter) ToString(payload *commonpb.Payload) string {
	return string(payload.Data)
}

// UnmarshalJSON requires every current certificate fact, including explicit
// cause presence. Missing, null, duplicate, or unknown fields cannot silently
// become facts about the original provider failure.
func (failure *providerFailureRecord) UnmarshalJSON(data []byte) error {
	var decoded providerFailureRecord
	fields := map[string]any{
		"Provider": &decoded.Provider, "Operation": &decoded.Operation,
		"HTTPStatus": &decoded.HTTPStatus, "Kind": &decoded.Kind,
		"Code": &decoded.Code, "Message": &decoded.Message,
		"RequestID": &decoded.RequestID, "Retryable": &decoded.Retryable,
		"CausePresent": &decoded.CausePresent, "Cause": &decoded.Cause,
		"Diagnostic": &decoded.Diagnostic,
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil {
		return err
	}
	if opening != json.Delim('{') {
		return errors.New("planner provider certificate must be an object")
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := token.(string)
		if !ok {
			return errors.New("planner provider certificate field name must be text")
		}
		target, ok := fields[name]
		if !ok {
			return errors.New("planner provider certificate contains an unknown or duplicate field")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return err
		}
		if bytes.Equal(raw, []byte("null")) {
			return errors.New("planner provider certificate fields must not be null")
		}
		if err := json.Unmarshal(raw, target); err != nil {
			return fmt.Errorf("decode planner provider certificate field: %w", err)
		}
		delete(fields, name)
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	if len(fields) != 0 {
		return errors.New("planner provider certificate is missing required fields")
	}
	*failure = decoded
	return nil
}

// planOutputView selects references to existing output values and plain cause
// text. The diagnostic is rendered only after aggregate preflight has passed.
func planOutputView(output api.PlanActivityOutput) *planOutputRecord {
	record := &planOutputRecord{
		PublicationBatchID: output.PublicationBatchID, Result: output.Result,
		Transcript: output.Transcript, HistoryContext: output.HistoryContext,
		PublishedAssistantText: output.PublishedAssistantText, Usage: output.Usage,
		PlannerEvents: output.PlannerEvents, RecoveryCatalog: output.RecoveryCatalog,
		OutputContractFailure: output.OutputContractFailure, PlanningFailure: output.PlanningFailure,
		ModelInvocationRecovery: output.ModelInvocationRecovery,
	}
	if failure := output.ProviderFailure; failure != nil {
		record.ProviderFailure = &providerFailureRecord{
			Provider: failure.Provider(), Operation: failure.Operation(), HTTPStatus: failure.HTTPStatus(),
			Kind: failure.Kind(), Code: failure.Code(), Message: failure.Message(),
			RequestID: failure.RequestID(), Retryable: failure.Retryable(),
			CausePresent: failure.Unwrap() != nil,
		}
		if record.ProviderFailure.CausePresent {
			record.ProviderFailure.Cause = failure.Unwrap().Error()
		}
	}
	return record
}

// decodeLegacyPlanOutput accepts only the declared historical shape. Old
// non-null certificates must stay with the worker build that produced them.
func decodeLegacyPlanOutput(payload *commonpb.Payload, valuePtr any) error {
	var old *legacyPlanOutput
	if err := new(strictJSONPayloadConverter).FromPayload(payload, &old); err != nil {
		return err
	}
	if err := preflightValues(old); err != nil {
		return err
	}
	var output *api.PlanActivityOutput
	if old != nil {
		if old.ProviderFailure != nil {
			return errors.New("legacy json/plain planner provider certificate is unsupported by this worker; retain its matching worker build")
		}
		output = &api.PlanActivityOutput{
			PublicationBatchID: old.PublicationBatchID, Result: old.Result,
			Transcript: old.Transcript, HistoryContext: old.HistoryContext,
			PublishedAssistantText: old.PublishedAssistantText, Usage: old.Usage,
			PlannerEvents: old.PlannerEvents, RecoveryCatalog: old.RecoveryCatalog,
			OutputContractFailure: old.OutputContractFailure, PlanningFailure: old.PlanningFailure,
			ModelInvocationRecovery: old.ModelInvocationRecovery,
		}
	}
	return assignPlanOutput(valuePtr, output)
}

func isPlanOutputType(t reflect.Type) bool {
	if t == nil {
		return false
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t == reflect.TypeFor[api.PlanActivityOutput]()
}

// assignPlanOutput preserves JSON null behavior and rejects invalid decode
// destinations without changing the caller's existing value on failure.
func assignPlanOutput(valuePtr any, output *api.PlanActivityOutput) error {
	destination := reflect.ValueOf(valuePtr)
	if destination.Kind() != reflect.Pointer || destination.IsNil() {
		return errors.New("planner output destination must be a nonnil pointer")
	}
	destination = destination.Elem()
	if output == nil {
		if destination.Kind() == reflect.Pointer {
			destination.SetZero()
		}
		return nil
	}
	for destination.Kind() == reflect.Pointer {
		if destination.IsNil() {
			destination.Set(reflect.New(destination.Type().Elem()))
		}
		destination = destination.Elem()
	}
	destination.Set(reflect.ValueOf(*output))
	return nil
}

func (record *planOutputRecord) output() *api.PlanActivityOutput {
	return &api.PlanActivityOutput{
		PublicationBatchID: record.PublicationBatchID, Result: record.Result,
		Transcript: record.Transcript, HistoryContext: record.HistoryContext,
		PublishedAssistantText: record.PublishedAssistantText, Usage: record.Usage,
		PlannerEvents: record.PlannerEvents, RecoveryCatalog: record.RecoveryCatalog,
		OutputContractFailure: record.OutputContractFailure, PlanningFailure: record.PlanningFailure,
		ModelInvocationRecovery: record.ModelInvocationRecovery,
	}
}

// validate checks the exclusive certificate before either engine can accept
// it. Only the activity journal can establish that these facts are certified;
// an arbitrary ProviderError elsewhere is never a certificate.
func (record *planOutputRecord) validate() error {
	failure := record.ProviderFailure
	if failure == nil {
		return nil
	}
	for _, text := range []string{failure.Provider, failure.Operation, string(failure.Kind),
		failure.Code, failure.Message, failure.RequestID, failure.Cause, failure.Diagnostic} {
		if !utf8.ValidString(text) {
			return errors.New("planner provider certificate contains invalid UTF-8")
		}
	}
	if record.Result != nil || record.OutputContractFailure != nil ||
		record.ModelInvocationRecovery != nil || record.PlanningFailure != nil ||
		len(record.Transcript) != 0 || record.HistoryContext != nil ||
		record.PublishedAssistantText != "" || record.RecoveryCatalog != nil {
		return errors.New("provider failure cannot accompany planner output or another result variant")
	}
	if failure.Provider == "" || !failure.Retryable ||
		(failure.Kind != model.ProviderErrorKindRateLimited && failure.Kind != model.ProviderErrorKindUnavailable) {
		return errors.New("provider recovery requires a typed temporary provider failure")
	}
	if failure.HTTPStatus != 0 && (failure.HTTPStatus < 100 || failure.HTTPStatus > 599) {
		return errors.New("planner provider certificate HTTP status is outside 100-599")
	}
	if !failure.CausePresent && failure.Cause != "" {
		return errors.New("planner provider certificate has cause text without a cause")
	}
	return nil
}

// diagnosticParts mirrors ProviderError.Error using references to the saved
// text. Preflight counts these pieces before joining them into one string.
func (failure *providerFailureRecord) diagnosticParts() []string {
	operation := failure.Operation
	if operation == "" {
		operation = "request"
	}
	status := ""
	if failure.HTTPStatus > 0 {
		status = strconv.Itoa(failure.HTTPStatus) + " "
	}
	codeSeparator := ""
	if failure.Code != "" {
		codeSeparator = ": "
	}
	message := failure.Message
	if message == "" && failure.CausePresent {
		message = failure.Cause
	}
	if message == "" {
		message = "provider error"
	}
	return []string{failure.Provider, " ", string(failure.Kind), " ", status, "(",
		operation, "): ", failure.Code, codeSeparator, message}
}
