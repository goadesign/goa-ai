// Package schema checks flat first-query and page inputs before the runtime
// uses cursor presence to identify a page after complete input validation.
package schema

import (
	"fmt"
	"slices"
)

// PagingComposition checks the execution schema of a self-continuing operation.
// A schema without a root oneOf retains ordinary, non-composed behavior. A root
// oneOf must contain a closed first object that forbids cursorField and a closed
// page object that requires a string cursorField. Accepted inputs can therefore
// be classified by cursor presence after validation against the complete schema.
func PagingComposition(schemaBytes []byte, cursorField string) (bool, error) {
	compiled, err := defaultValidator.compiledSchema(schemaBytes)
	if err != nil {
		return false, err
	}
	if len(compiled.OneOf) == 0 {
		return false, nil
	}
	if len(compiled.OneOf) != 2 {
		return false, fmt.Errorf("paging execution requires exactly two input branches")
	}
	first, page := compiled.OneOf[0], compiled.OneOf[1]
	for _, branch := range compiled.OneOf {
		if branch.Types == nil || !slices.Equal(branch.Types.ToStrings(), []string{"object"}) || branch.AdditionalProperties != false || len(branch.PatternProperties) != 0 {
			return false, fmt.Errorf("paging execution branches must be closed objects")
		}
	}
	if _, exists := first.Properties[cursorField]; exists || slices.Contains(first.Required, cursorField) {
		return false, fmt.Errorf("paging first input must forbid cursor field %q", cursorField)
	}
	cursor, exists := page.Properties[cursorField]
	if !exists || !slices.Contains(page.Required, cursorField) || cursor.Types == nil || !slices.Equal(cursor.Types.ToStrings(), []string{"string"}) {
		return false, fmt.Errorf("paging page input must require string cursor field %q", cursorField)
	}
	return true, nil
}
