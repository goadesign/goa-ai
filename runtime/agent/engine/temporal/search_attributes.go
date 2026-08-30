// Package temporal keeps search-attribute typing at the adapter boundary. The
// shared runtime contract stays generic (`map[string]any`), and only this file
// decides how those values map onto Temporal visibility types.
package temporal

import (
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	temporalsdk "go.temporal.io/sdk/temporal"

	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
)

// convertSearchAttributes builds Temporal's typed start options from the same
// normalized values and payloads included in the shared start recipe.
func convertSearchAttributes(attributes []startrecipe.SearchAttribute) temporalsdk.SearchAttributes {
	updates := make([]temporalsdk.SearchAttributeUpdate, 0, len(attributes))
	for _, attribute := range attributes {
		updates = append(updates, convertSearchAttribute(attribute))
	}
	return temporalsdk.NewSearchAttributes(updates...)
}

// convertSearchAttribute selects the typed key that matches the payload type
// already validated by startrecipe.EncodeSearchAttributes.
func convertSearchAttribute(attribute startrecipe.SearchAttribute) temporalsdk.SearchAttributeUpdate {
	switch attribute.ValueType {
	case enumspb.INDEXED_VALUE_TYPE_KEYWORD:
		return temporalsdk.NewSearchAttributeKeyKeyword(attribute.Name).ValueSet(attribute.Value.(string))
	case enumspb.INDEXED_VALUE_TYPE_BOOL:
		return temporalsdk.NewSearchAttributeKeyBool(attribute.Name).ValueSet(attribute.Value.(bool))
	case enumspb.INDEXED_VALUE_TYPE_INT:
		return temporalsdk.NewSearchAttributeKeyInt64(attribute.Name).ValueSet(attribute.Value.(int64))
	case enumspb.INDEXED_VALUE_TYPE_DOUBLE:
		return temporalsdk.NewSearchAttributeKeyFloat64(attribute.Name).ValueSet(attribute.Value.(float64))
	case enumspb.INDEXED_VALUE_TYPE_DATETIME:
		return temporalsdk.NewSearchAttributeKeyTime(attribute.Name).ValueSet(attribute.Value.(time.Time))
	case enumspb.INDEXED_VALUE_TYPE_KEYWORD_LIST:
		return temporalsdk.NewSearchAttributeKeyKeywordList(attribute.Name).ValueSet(attribute.Value.([]string))
	case enumspb.INDEXED_VALUE_TYPE_UNSPECIFIED, enumspb.INDEXED_VALUE_TYPE_TEXT:
		panic("start recipe returned a search attribute type not supported by the engine contract")
	default:
		panic("start recipe returned an unsupported Temporal search attribute type")
	}
}
