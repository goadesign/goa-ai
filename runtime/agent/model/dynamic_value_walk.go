// Package model bounds recursive metadata work shared by response copying and
// fingerprinting. Provider JSON cannot contain reference cycles, but custom
// adapters can construct them in Go values.
package model

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"unicode/utf8"
)

const (
	maxDynamicValueDepth  = 64
	maxDynamicValueVisits = 100_000
	maxDynamicValueBytes  = 16 << 20
)

type (
	dynamicContainer struct {
		kind      reflect.Kind
		valueType reflect.Type
		pointer   uintptr
		length    int
		capacity  int
	}

	dynamicValueWalk struct {
		visits int
		bytes  int
		active map[dynamicContainer]struct{}
	}
)

// visit charges one top-level response value or stream chunk.
func (w *dynamicValueWalk) visit() error {
	w.visits++
	if w.visits > maxDynamicValueVisits {
		return fmt.Errorf(
			"dynamic value exceeds maximum visited values %d",
			maxDynamicValueVisits,
		)
	}
	return nil
}

// enter checks the recursion and work limits, then marks mutable containers on
// the current path so cycles fail before another recursive call.
func (w *dynamicValueWalk) enter(value reflect.Value, depth int) (dynamicContainer, bool, error) {
	if depth > maxDynamicValueDepth {
		return dynamicContainer{}, false, fmt.Errorf(
			"dynamic value exceeds maximum depth %d",
			maxDynamicValueDepth,
		)
	}
	if err := w.visit(); err != nil {
		return dynamicContainer{}, false, err
	}
	if value.Kind() == reflect.String {
		if err := w.addBytes(value.Len()); err != nil {
			return dynamicContainer{}, false, err
		}
	} else if value.Kind() == reflect.Slice && value.Type().Elem().Kind() == reflect.Uint8 {
		if err := w.addBytes(value.Len()); err != nil {
			return dynamicContainer{}, false, err
		}
	}
	container, tracked := dynamicContainerIdentity(value)
	if !tracked {
		return dynamicContainer{}, false, nil
	}
	if w.active == nil {
		w.active = make(map[dynamicContainer]struct{})
	}
	if _, exists := w.active[container]; exists {
		return dynamicContainer{}, false, dynamicCycleError(value)
	}
	w.active[container] = struct{}{}
	return container, true, nil
}

// preflightDynamicValueAt checks one metadata or tool-result value without
// copying it. The shared walk carries the invocation-wide byte and visit budget
// while depth and cycle tracking apply to this nested value.
func preflightDynamicValueAt(
	value reflect.Value,
	depth int,
	walk *dynamicValueWalk,
	contract dynamicCloneContract,
) error {
	if !value.IsValid() {
		_, _, err := walk.enter(value, depth)
		return err
	}
	if value.Kind() == reflect.Interface {
		if _, _, err := walk.enter(value, depth); err != nil {
			return err
		}
		if value.IsNil() {
			return nil
		}
		return preflightDynamicValueAt(value.Elem(), depth, walk, contract)
	}
	container, tracked, err := walk.enter(value, depth)
	if err != nil {
		return err
	}
	defer walk.leave(container, tracked)
	if value.Kind() == reflect.String && contract == dynamicCloneCanonical &&
		!utf8.ValidString(value.String()) {
		return fmt.Errorf("string is not valid UTF-8")
	}
	switch value.Kind() {
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("map key type %s is not a string", value.Type().Key())
		}
		if value.IsNil() {
			return nil
		}
		if err := walk.checkChildren(value.Len()); err != nil {
			return err
		}
		keys := sortedStringMapKeys(value)
		for _, key := range keys {
			if err := walk.addBytes(len(key.String())); err != nil {
				return err
			}
			if contract == dynamicCloneCanonical && !utf8.ValidString(key.String()) {
				return fmt.Errorf("map key is not valid UTF-8")
			}
			if err := preflightDynamicValueAt(value.MapIndex(key), depth+1, walk, contract); err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.Type().Elem().Kind() == reflect.Uint8 {
			return nil
		}
		if err := walk.checkChildren(value.Len()); err != nil {
			return err
		}
		for index := 0; index < value.Len(); index++ {
			if err := preflightDynamicValueAt(value.Index(index), depth+1, walk, contract); err != nil {
				return err
			}
		}
		return nil
	case reflect.Struct:
		if contract == dynamicCloneCanonical {
			return fmt.Errorf("value type %s is not JSON-compatible metadata", value.Type())
		}
		valueType := value.Type()
		descriptorBytes := len(valueType.PkgPath()) + len(valueType.Name())
		for index := 0; index < value.NumField(); index++ {
			field := valueType.Field(index)
			descriptorBytes += len(field.Name) + len(field.Tag)
		}
		if err := walk.addBytes(descriptorBytes); err != nil {
			return err
		}
		if err := walk.checkChildren(value.NumField()); err != nil {
			return err
		}
		fieldIndexes := sortedStructFieldIndexes(valueType)
		for _, index := range fieldIndexes {
			if err := preflightDynamicValueAt(value.Field(index), depth+1, walk, contract); err != nil {
				return err
			}
		}
		return nil
	case reflect.Float32, reflect.Float64:
		if contract == dynamicCloneCanonical {
			return validateCanonicalFloat(value)
		}
		return nil
	case reflect.Bool,
		reflect.Int,
		reflect.Int8,
		reflect.Int16,
		reflect.Int32,
		reflect.Int64,
		reflect.Uint,
		reflect.Uint8,
		reflect.Uint16,
		reflect.Uint32,
		reflect.Uint64,
		reflect.String:
		return nil
	case reflect.Invalid,
		reflect.Interface,
		reflect.Uintptr,
		reflect.Complex64,
		reflect.Complex128,
		reflect.Chan,
		reflect.Func,
		reflect.Pointer,
		reflect.UnsafePointer:
		if contract == dynamicCloneCanonical {
			return fmt.Errorf("value type %s is not JSON-compatible metadata", value.Type())
		}
		return fmt.Errorf("value type %s cannot be copied safely", value.Type())
	}
	panic("unreachable dynamic value kind")
}

// sortedStringMapKeys returns deterministic map traversal order. Callers must
// check value.Len against the remaining visit budget before this allocation.
func sortedStringMapKeys(value reflect.Value) []reflect.Value {
	keys := value.MapKeys()
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].String() < keys[j].String()
	})
	return keys
}

// sortedStructFieldIndexes returns deterministic evidence traversal order.
func sortedStructFieldIndexes(valueType reflect.Type) []int {
	indexes := make([]int, valueType.NumField())
	for index := range indexes {
		indexes[index] = index
	}
	sort.Slice(indexes, func(i, j int) bool {
		return valueType.Field(indexes[i]).Name < valueType.Field(indexes[j]).Name
	})
	return indexes
}

// validateCanonicalFloat rejects numbers that encoding/json cannot preserve.
func validateCanonicalFloat(value reflect.Value) error {
	number := value.Float()
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return fmt.Errorf("value %v is not a finite JSON number", number)
	}
	return nil
}

// addBytes bounds model-controlled string, byte-slice, and map-key data across
// one unary response or complete stream.
func (w *dynamicValueWalk) addBytes(count int) error {
	if count < 0 || count > maxDynamicValueBytes-w.bytes {
		return fmt.Errorf("dynamic value exceeds maximum byte size %d", maxDynamicValueBytes)
	}
	w.bytes += count
	return nil
}

// checkChildren rejects a collection before allocating its copy when its
// children cannot fit within the remaining visit budget.
func (w *dynamicValueWalk) checkChildren(count int) error {
	if count < 0 || count > maxDynamicValueVisits-w.visits {
		return fmt.Errorf(
			"dynamic value exceeds maximum visited values %d",
			maxDynamicValueVisits,
		)
	}
	return nil
}

// leave removes a container after all of its children have been visited.
func (w *dynamicValueWalk) leave(container dynamicContainer, tracked bool) {
	if tracked {
		delete(w.active, container)
	}
}

// dynamicContainerIdentity identifies non-empty mutable containers whose
// children can refer back to them.
func dynamicContainerIdentity(value reflect.Value) (dynamicContainer, bool) {
	switch value.Kind() {
	case reflect.Map:
		if value.IsNil() || value.Len() == 0 {
			return dynamicContainer{}, false
		}
		return dynamicContainer{
			kind:      reflect.Map,
			valueType: value.Type(),
			pointer:   uintptr(value.UnsafePointer()),
		}, true
	case reflect.Slice:
		if value.IsNil() || value.Len() == 0 {
			return dynamicContainer{}, false
		}
		return dynamicContainer{
			kind:      reflect.Slice,
			valueType: value.Type(),
			pointer:   value.Pointer(),
			length:    value.Len(),
			capacity:  value.Cap(),
		}, true
	case reflect.Invalid,
		reflect.Bool,
		reflect.Int,
		reflect.Int8,
		reflect.Int16,
		reflect.Int32,
		reflect.Int64,
		reflect.Uint,
		reflect.Uint8,
		reflect.Uint16,
		reflect.Uint32,
		reflect.Uint64,
		reflect.Uintptr,
		reflect.Float32,
		reflect.Float64,
		reflect.Complex64,
		reflect.Complex128,
		reflect.Array,
		reflect.Chan,
		reflect.Func,
		reflect.Interface,
		reflect.Pointer,
		reflect.String,
		reflect.Struct,
		reflect.UnsafePointer:
		return dynamicContainer{}, false
	}
	panic("unreachable dynamic container kind")
}

// dynamicCycleError describes a provider value that cannot be represented
// as finite JSON-compatible metadata.
func dynamicCycleError(value reflect.Value) error {
	return fmt.Errorf("dynamic value contains a %s reference cycle", value.Kind())
}
