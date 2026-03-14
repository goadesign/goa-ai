// Package temporal keeps search-attribute typing at the adapter boundary. The
// shared runtime contract stays generic (`map[string]any`), and only this file
// decides how those values map onto Temporal visibility types.
package temporal

import (
	"fmt"
	"time"

	temporalsdk "go.temporal.io/sdk/temporal"
)

// convertSearchAttributes maps the engine's generic visibility metadata
// contract into Temporal's typed search-attribute API at the adapter boundary.
func convertSearchAttributes(attributes map[string]any) (temporalsdk.SearchAttributes, error) {
	updates := make([]temporalsdk.SearchAttributeUpdate, 0, len(attributes))
	for name, value := range attributes {
		update, err := convertSearchAttribute(name, value)
		if err != nil {
			return temporalsdk.SearchAttributes{}, err
		}
		updates = append(updates, update)
	}
	return temporalsdk.NewSearchAttributes(updates...), nil
}

// convertSearchAttribute infers the Temporal search-attribute key from the Go
// value type. The engine contract does not distinguish text from keyword
// strings, so strings are emitted as keyword attributes because runtime callers
// use them as exact-match visibility identifiers.
func convertSearchAttribute(name string, value any) (temporalsdk.SearchAttributeUpdate, error) {
	switch typed := value.(type) {
	case nil:
		return nil, fmt.Errorf("temporal engine: search attribute %q has nil value", name)
	case string:
		return temporalsdk.NewSearchAttributeKeyKeyword(name).ValueSet(typed), nil
	case bool:
		return temporalsdk.NewSearchAttributeKeyBool(name).ValueSet(typed), nil
	case int:
		return temporalsdk.NewSearchAttributeKeyInt64(name).ValueSet(int64(typed)), nil
	case int8:
		return temporalsdk.NewSearchAttributeKeyInt64(name).ValueSet(int64(typed)), nil
	case int16:
		return temporalsdk.NewSearchAttributeKeyInt64(name).ValueSet(int64(typed)), nil
	case int32:
		return temporalsdk.NewSearchAttributeKeyInt64(name).ValueSet(int64(typed)), nil
	case int64:
		return temporalsdk.NewSearchAttributeKeyInt64(name).ValueSet(typed), nil
	case float32:
		return temporalsdk.NewSearchAttributeKeyFloat64(name).ValueSet(float64(typed)), nil
	case float64:
		return temporalsdk.NewSearchAttributeKeyFloat64(name).ValueSet(typed), nil
	case time.Time:
		return temporalsdk.NewSearchAttributeKeyTime(name).ValueSet(typed), nil
	case []string:
		return temporalsdk.NewSearchAttributeKeyKeywordList(name).ValueSet(typed), nil
	default:
		return nil, fmt.Errorf("temporal engine: search attribute %q has unsupported type %T", name, value)
	}
}
