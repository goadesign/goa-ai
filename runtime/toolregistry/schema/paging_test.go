package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPagingCompositionRequiresDisjointClosedInputs(t *testing.T) {
	for _, test := range []struct {
		name, schema      string
		composed, invalid bool
	}{
		{"flat self", `{"type":"object"}`, false, false},
		{"valid", `{"oneOf":[{"type":"object","additionalProperties":false},{"type":"object","additionalProperties":false,"properties":{"cursor":{"type":"string"}},"required":["cursor"]}]}`, true, false},
		{"open first", `{"oneOf":[{"type":"object"},{"type":"object","additionalProperties":false,"properties":{"cursor":{"type":"string"}},"required":["cursor"]}]}`, false, true},
		{"optional cursor", `{"oneOf":[{"type":"object","additionalProperties":false},{"type":"object","additionalProperties":false,"properties":{"cursor":{"type":"string"}}}]}`, false, true},
		{"first accepts cursor", `{"oneOf":[{"type":"object","additionalProperties":false,"properties":{"cursor":{"type":"string"}}},{"type":"object","additionalProperties":false,"properties":{"cursor":{"type":"string"}},"required":["cursor"]}]}`, false, true},
		{"wrong cursor type", `{"oneOf":[{"type":"object","additionalProperties":false},{"type":"object","additionalProperties":false,"properties":{"cursor":{"type":"number"}},"required":["cursor"]}]}`, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			composed, err := PagingComposition([]byte(test.schema), "cursor")
			assert.Equal(t, test.composed, composed)
			assert.Equal(t, test.invalid, err != nil)
		})
	}
}
