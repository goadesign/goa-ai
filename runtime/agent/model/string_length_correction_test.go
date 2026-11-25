// String length correction shares the advertised contract for unary and
// streaming validation without exposing rejected values or concrete indexes.
package model

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMaxStringLengthCorrection(t *testing.T) {
	const schema = `{
		"type":"object","properties":{
			"events":{"type":"array","items":{"type":"object","properties":{
				"supporting_tasks":{"type":"array","items":{"$ref":"#/$defs/task"}}}}},
			"tasks":{"type":"array","items":{"$ref":"#/$defs/task"}},
			"sentence":{"type":"string","maxLength":240}},
		"$defs":{"task":{"type":"object","properties":{
			"existing_entry":{"type":"string","pattern":"^new$"},
			"instruction":{"type":"object","properties":{"quote":{"type":"string","maxLength":400}}}}}}}`
	const nested = "events.*.supporting_tasks.*.instruction.quote"
	const standalone = "tasks.*.instruction.quote"
	def := arrayCorrectionDefinition(schema, testFieldMetadata(map[string]string{
		nested: "string", standalone: "string", "sentence": "string",
	}, nil))
	nestedPayload := func(q, id string) string {
		return fmt.Sprintf(`{"events":[{"supporting_tasks":[{"existing_entry":%q,"instruction":{"quote":%q}}]}]}`, id, q)
	}
	for _, tc := range []struct{ name, payload, expected string }{
		{"nested", nestedPayload(strings.Repeat("é", 401), "new"), `Field "events.*.supporting_tasks.*.instruction.quote" must contain at most 400 characters.`},
		{"inclusive Unicode boundary", nestedPayload(strings.Repeat("é", 400), "new"), ""},
		{
			"unrelated unsupported rule",
			nestedPayload(strings.Repeat("é", 401), "submitted-invalid-value"),
			`Field "events.*.supporting_tasks.*.instruction.quote" must contain at most 400 characters. Other schema errors are not detailed here.`,
		},
		{
			"standalone",
			fmt.Sprintf(`{"tasks":[{"instruction":{"quote":%q}}]}`, strings.Repeat("x", 401)),
			`Field "tasks.*.instruction.quote" must contain at most 400 characters.`,
		},
		{"sentence", fmt.Sprintf(`{"sentence":%q}`, strings.Repeat("x", 241)), `Field "sentence" must contain at most 240 characters.`},
		{"sentence boundary", fmt.Sprintf(`{"sentence":%q}`, strings.Repeat("x", 240)), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rejected := checkArrayCorrection(t, def, tc.payload, tc.expected)
			assert.Equal(t, tc.expected == "", rejected == nil)
		})
	}
}
