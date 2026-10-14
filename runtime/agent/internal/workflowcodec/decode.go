// Package workflowcodec decodes saved runtime records and workflow payloads.
// Their owners apply storage or transport size limits separately, so decoding a
// saved record does not impose a workflow payload limit on that record.
package workflowcodec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"unicode/utf8"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	genrunterminal "goa.design/goa-ai/registry/gen/runterminal"
)

// DecodeJSON decodes one typed runtime record, retaining exact numbers and
// rejecting unknown fields, trailing data, and invalid generated run subjects.
// Callers remain responsible for their own storage or transport size limits.
func DecodeJSON(data []byte, valuePtr any) error {
	if !utf8.Valid(data) {
		return errors.New("workflow codec: canonical JSON payload contains invalid UTF-8")
	}
	if err := validateSavedSubjects(data, reflect.TypeOf(valuePtr)); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(valuePtr); err != nil {
		return fmt.Errorf("workflow codec: decode canonical JSON payload: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("workflow codec: canonical JSON payload has trailing data")
	}
	return validateDecodedSubjects(reflect.ValueOf(valuePtr), new(workflowJSONContainers))
}

// validateDecodedSubjects also rejects an omitted required subject. Generated
// codecs own subject constraints; this traversal only locates their Go values.
func validateDecodedSubjects(value reflect.Value, active *workflowJSONContainers) error {
	if err := validatePreparationResult(value); err != nil {
		return err
	}
	if !value.IsValid() {
		return nil
	}
	if selected, known, err := generatedUnionJSONValue(value); known {
		if err != nil {
			return err
		}
		return validateDecodedSubjects(selected, active)
	}
	if value.Type() == reflect.TypeFor[genrunterminal.PreparationDecision]() {
		decision := value.Interface().(genrunterminal.PreparationDecision)
		_, err := genrunterminal.EncodePreparationDecision(&decision)
		return err
	}
	if value.Type() == reflect.TypeFor[genrunterminal.ExecutionFailure]() {
		terminal := value.Interface().(genrunterminal.ExecutionFailure)
		_, err := genrunterminal.EncodeExecutionFailure(&terminal)
		return err
	}
	if value.Type() == reflect.TypeFor[genrunterminal.UnstartedExecutionTerminal]() {
		terminal := value.Interface().(genrunterminal.UnstartedExecutionTerminal)
		_, err := genrunterminal.EncodeUnstartedExecutionTerminal(&terminal)
		return err
	}
	if value.Type() == reflect.TypeFor[genrunterminal.UnstartedExecutionTerminalResult]() {
		terminal := value.Interface().(genrunterminal.UnstartedExecutionTerminalResult)
		_, err := genrunterminal.EncodeUnstartedExecutionTerminalResult(&terminal)
		return err
	}
	if value.Type() == reflect.TypeFor[genregistry.ExecutionInvocation]() {
		invocation := value.Interface().(genregistry.ExecutionInvocation)
		_, err := genregistry.EncodeExecutionInvocation(&invocation)
		return err
	}
	if value.Type() == reflect.TypeFor[genrunidentity.RunSubject]() {
		subject := value.Interface().(genrunidentity.RunSubject)
		_, err := genrunidentity.EncodeRunSubject(&subject)
		return err
	}
	if isWorkflowByteSlice(value.Type()) {
		return nil
	}
	container, tracked, err := active.enter(value)
	if err != nil {
		return err
	}
	if tracked {
		defer delete(*active, container)
	}
	//nolint:exhaustive // Only containers can hold nested subjects; scalar values need no traversal.
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if !value.IsNil() {
			return validateDecodedSubjects(value.Elem(), active)
		}
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			field := value.Type().Field(index)
			if field.Tag.Get("json") == "-" || (field.PkgPath != "" && !isEmbeddedWorkflowJSONStruct(field)) {
				continue
			}
			if err := validateDecodedSubjects(value.Field(index), active); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err := validateDecodedSubjects(value.Index(index), active); err != nil {
				return err
			}
		}
	case reflect.Map:
		iter := value.MapRange()
		for iter.Next() {
			if err := validateDecodedSubjects(iter.Value(), active); err != nil {
				return err
			}
		}
	}
	return nil
}
