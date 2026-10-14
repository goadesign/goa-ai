// Package workflowcodec exposes selected values from generated Registry and run
// identity unions to workflow size inspection. These values are private;
// inspecting their exact JSON fields preserves validation and byte accounting.
package workflowcodec

import (
	"reflect"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	genrunterminal "goa.design/goa-ai/registry/gen/runterminal"
)

// generatedUnionJSONValue returns generated Registry and run-executor union shapes.
// It uses the generator's selection checks, so an invalid union fails before
// encoding and arbitrary custom marshalers still receive their usual rejection.
func generatedUnionJSONValue(value reflect.Value) (reflect.Value, bool, error) {
	if value.Kind() == reflect.Pointer {
		element := value.Type().Elem()
		if element != reflect.TypeFor[genregistry.CatalogTarget]() && element != reflect.TypeFor[genregistry.ToolFieldSegment]() && element != reflect.TypeFor[genregistry.ToolUnionSelection]() && element != reflect.TypeFor[genrunidentity.RunExecutor]() && element != reflect.TypeFor[genregistry.ExecutionInvocationKind]() && element != reflect.TypeFor[genrunterminal.PreparationState]() && element != reflect.TypeFor[genrunterminal.ExecutionTerminalTime]() && element != reflect.TypeFor[genrunterminal.UnstartedExecutionOutcome]() && element != reflect.TypeFor[genrunterminal.UnstartedExecutionTerminalDecision]() {
			return reflect.Value{}, false, nil
		}
		if value.IsNil() {
			return reflect.Value{}, true, nil
		}
		value = value.Elem()
	}
	if !value.CanInterface() {
		return reflect.Value{}, false, nil
	}
	var kind string
	var branch any
	var err error
	switch selection := value.Interface().(type) {
	case genregistry.CatalogTarget:
		kind = string(selection.Kind())
		branch, err = selection.Value()
	case genrunterminal.PreparationState:
		kind = string(selection.Kind())
		branch, err = selection.Value()
	case genrunterminal.ExecutionTerminalTime:
		kind = string(selection.Kind())
		branch, err = selection.Value()
	case genrunterminal.UnstartedExecutionOutcome:
		kind = string(selection.Kind())
		branch, err = selection.Value()
	case genrunterminal.UnstartedExecutionTerminalDecision:
		kind = string(selection.Kind())
		branch, err = selection.Value()
	case genregistry.ExecutionInvocationKind:
		kind = string(selection.Kind())
		branch, err = selection.Value()
	case genrunidentity.RunExecutor:
		kind = string(selection.Kind())
		branch, err = selection.Value()
	case genregistry.ToolFieldSegment:
		kind = string(selection.Kind())
		branch, err = selection.Value()
	case genregistry.ToolUnionSelection:
		kind = string(selection.Kind())
		branch, err = selection.Value()
	default:
		return reflect.Value{}, false, nil
	}
	if err != nil {
		return reflect.Value{}, true, err
	}
	return reflect.ValueOf(struct {
		Type  string `json:"type"`
		Value any    `json:"value"`
	}{Type: kind, Value: branch}), true, nil
}
