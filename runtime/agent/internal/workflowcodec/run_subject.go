// Package workflowcodec preserves workflow Go field names, while each run
// subject uses its authored JSON contract. This reader finds subjects by their
// Go type and validates their original bytes before a union reader can discard
// unknown fields. Subject field names and constraints remain generated.
package workflowcodec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	genrunterminal "goa.design/goa-ai/registry/gen/runterminal"
)

type (
	savedJSONField struct {
		typ    reflect.Type
		name   string
		depth  int
		tagged bool
		order  int
	}
)

// validateSavedSubjects follows typed fields and collections to each saved subject.
// It passes that complete original value to the generated strict decoder.
func validateSavedSubjects(data []byte, typ reflect.Type) error {
	if typ == nil {
		return nil
	}
	for typ.Kind() == reflect.Pointer {
		if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
			return nil
		}
		typ = typ.Elem()
	}
	if known, err := validateSavedTerminalUnion(data, typ); known {
		return err
	}
	if typ == reflect.TypeFor[genrunterminal.PreparationDecision]() {
		_, err := genrunterminal.DecodePreparationDecision(data)
		return err
	}
	if typ == reflect.TypeFor[genrunterminal.PreparationProceed]() {
		_, err := genrunterminal.DecodePreparationProceed(data)
		return err
	}
	if typ == reflect.TypeFor[genrunterminal.StartedExecution]() {
		_, err := genrunterminal.DecodeStartedExecution(data)
		return err
	}
	if typ == reflect.TypeFor[genrunterminal.ExecutionFailure]() {
		_, err := genrunterminal.DecodeExecutionFailure(data)
		return err
	}
	if typ == reflect.TypeFor[genrunterminal.UnstartedExecutionTerminal]() {
		_, err := genrunterminal.DecodeUnstartedExecutionTerminal(data)
		return err
	}
	if typ == reflect.TypeFor[genrunterminal.UnstartedExecutionTerminalResult]() {
		_, err := genrunterminal.DecodeUnstartedExecutionTerminalResult(data)
		return err
	}
	if typ == reflect.TypeFor[genregistry.ExecutionInvocation]() {
		if _, err := genregistry.DecodeExecutionInvocation(data); err != nil {
			return fmt.Errorf("workflow codec: saved execution invocation: %w", err)
		}
		return nil
	}
	if typ == reflect.TypeFor[genrunidentity.RunSubject]() {
		if _, err := genrunidentity.DecodeRunSubject(data); err != nil {
			return fmt.Errorf("workflow codec: saved run subject: %w", err)
		}
		return nil
	}
	//nolint:exhaustive // Only JSON objects and arrays can contain a nested subject.
	switch typ.Kind() {
	case reflect.Struct, reflect.Map, reflect.Slice, reflect.Array:
	default:
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return nil
	}
	//nolint:exhaustive // The earlier type check leaves only objects and arrays here.
	switch typ.Kind() {
	case reflect.Struct, reflect.Map:
		if token != json.Delim('{') {
			return nil
		}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				return err
			}
			var fieldType reflect.Type
			if typ.Kind() == reflect.Map {
				fieldType = typ.Elem()
			} else {
				name, ok := key.(string)
				if !ok {
					return fmt.Errorf("workflow codec: object field name must be text")
				}
				fieldType = savedJSONFieldType(typ, name)
			}
			if fieldType != nil {
				if err := validateSavedSubjects(raw, fieldType); err != nil {
					return err
				}
			}
		}
	case reflect.Slice, reflect.Array:
		if token != json.Delim('[') {
			return nil
		}
		for decoder.More() {
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				return err
			}
			if err := validateSavedSubjects(raw, typ.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

// savedJSONFieldType follows JSON field selection: shallower fields hide deeper
// ones, an explicit tag wins a tie, and exact names win before folded names.
func savedJSONFieldType(typ reflect.Type, name string) reflect.Type {
	var fields []savedJSONField
	collectSavedJSONFields(typ, nil, &fields)
	selected := make(map[string]savedJSONField)
	ambiguous := make(map[string]bool)
	for _, field := range fields {
		previous, found := selected[field.name]
		if !found || field.depth < previous.depth || (field.depth == previous.depth && field.tagged && !previous.tagged) {
			selected[field.name] = field
			ambiguous[field.name] = false
		} else if field.depth == previous.depth && field.tagged == previous.tagged {
			ambiguous[field.name] = true
		}
	}
	if field, found := selected[name]; found && !ambiguous[name] {
		return field.typ
	}
	var folded *savedJSONField
	for fieldName, field := range selected {
		if !ambiguous[fieldName] && strings.EqualFold(fieldName, name) && (folded == nil || field.order < folded.order) {
			copy := field
			folded = &copy
		}
	}
	if folded == nil {
		return nil
	}
	return folded.typ
}

// collectSavedJSONFields preserves declaration order while examining promoted
// embedded fields. Repeated types stop only their own path, as encoding/json does.
func collectSavedJSONFields(typ reflect.Type, ancestors []reflect.Type, fields *[]savedJSONField) {
	if slices.Contains(ancestors, typ) {
		return
	}
	ancestors = append(ancestors, typ)
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		if field.Tag.Get("json") == "-" || (field.PkgPath != "" && !isEmbeddedWorkflowJSONStruct(field)) {
			continue
		}
		tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		embedded := field.Type
		if embedded.Kind() == reflect.Pointer {
			embedded = embedded.Elem()
		}
		if field.Anonymous && tag == "" && embedded.Kind() == reflect.Struct {
			collectSavedJSONFields(embedded, ancestors, fields)
			continue
		}
		jsonName, _ := jsonFieldEncoding(field)
		*fields = append(*fields, savedJSONField{typ: field.Type, name: jsonName, depth: len(ancestors), tagged: tag != "", order: len(*fields)})
	}
}
