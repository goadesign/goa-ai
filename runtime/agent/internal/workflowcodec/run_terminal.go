// Package workflowcodec checks terminal activity fields before decoding their
// selected records. It checks the original envelope for unknown fields before
// generated union decoding, then uses each record's generated strict codec.
package workflowcodec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"

	genrunterminal "goa.design/goa-ai/registry/gen/runterminal"
)

func validateSavedTerminalUnion(data []byte, typ reflect.Type) (bool, error) {
	if typ != reflect.TypeFor[genrunterminal.PreparationState]() &&
		typ != reflect.TypeFor[genrunterminal.ExecutionTerminalTime]() &&
		typ != reflect.TypeFor[genrunterminal.UnstartedExecutionOutcome]() &&
		typ != reflect.TypeFor[genrunterminal.UnstartedExecutionTerminalDecision]() {
		return false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return true, err
	}
	if token != json.Delim('{') {
		return true, fmt.Errorf("terminal union must be an object")
	}
	seen := make(map[string]bool, 2)
	var branch json.RawMessage
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return true, err
		}
		name, ok := key.(string)
		if !ok || (name != "type" && name != "value") || seen[name] {
			return true, fmt.Errorf("terminal union contains an unknown or duplicate field")
		}
		seen[name] = true
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return true, err
		}
		if name == "value" {
			branch = raw
		}
	}
	if _, err := decoder.Token(); err != nil {
		return true, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return true, fmt.Errorf("terminal union has trailing JSON")
	}
	value := reflect.New(typ)
	if err := json.Unmarshal(data, value.Interface()); err != nil {
		return true, err
	}
	selected, _, err := generatedUnionJSONValue(value)
	if err != nil {
		return true, err
	}
	selectedBranch := selected.FieldByName("Value").Interface()
	return true, validateSavedSubjects(branch, reflect.TypeOf(selectedBranch))
}
