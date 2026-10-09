// These checks compare complete YAML and JSON values without rounding numbers
// or discarding future fields. Shared aliases and merge sources stay ordinary
// YAML; recursive aliases and values outside JSON's types fail explicitly.
package mcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSkillFrontmatterComparison(t *testing.T) {
	for _, test := range []struct {
		name, yaml, json, want string
	}{
		{"all fields", "name: review\nfuture: [true, null, {exact: 9007199254740993}]\n", `{"name":"review","future":[true,null,{"exact":9007199254740993}]}`, ""},
		{"map ordering", "b: 2\na: 1\n", `{"a":1,"b":2}`, ""},
		{"empty collections", "a: []\nb: {}\n", `{"a":[],"b":{}}`, ""},
		{"integer spelling", "value: 9_007_199_254_740_993\n", `{"value":9007199254740993}`, ""},
		{"hexadecimal", "value: 0x20000000000001\n", `{"value":9007199254740993}`, ""},
		{"large exact integer", "value: !!int 123456789012345678901234567890\n", `{"value":123456789012345678901234567890}`, ""},
		{"numeric equality", "value: 1.25e3\n", `{"value":1250}`, ""},
		{"huge exponent without expansion", "value: !!float 1e999999999999999999999999999999\n", `{"value":10e999999999999999999999999999998}`, ""},
		{"negative zero", "value: -0.0\n", `{"value":0}`, ""},
		{"quoted numeric string", "value: '9007199254740993'\n", `{"value":"9007199254740993"}`, ""},
		{"no numeric coercion", "value: '1'\n", `{"value":1}`, "type differs"},
		{"no rounded equality", "value: 9007199254740992\n", `{"value":9007199254740993}`, "type differs"},
		{"nullable future value", "value: null\n", `{"value":null}`, ""},
		{"aliases", "original: &value {count: 3}\ncopy: *value\n", `{"original":{"count":3},"copy":{"count":3}}`, ""},
		{"aliased string key", "key: &key field\nmap:\n  *key: 2\n", `{"key":"field","map":{"field":2}}`, ""},
		{"merged mapping", "base: &base {a: 1, b: 2}\nitem: {<<: *base, b: 3}\n", `{"base":{"a":1,"b":2},"item":{"a":1,"b":3}}`, ""},
		{"merge precedence", "a: &a {value: 1}\nb: &b {value: 2, other: 3}\nitem: {<<: [*a, *b]}\n", `{"a":{"value":1},"b":{"value":2,"other":3},"item":{"value":1,"other":3}}`, ""},
		{"aliased merge sequence", "sources: &sources [{value: 1}, {value: 2, other: 3}]\nitem: {<<: *sources}\n", `{"sources":[{"value":1},{"value":2,"other":3}],"item":{"value":1,"other":3}}`, ""},
		{"quoted merge name", "'<<': 1\n", `{"<<":1}`, ""},
		{"duplicate fields", "value: 1\nvalue: 1\n", `{"value":1}`, "duplicate"},
		{"duplicate merge keys", "item: {<<: {a: 1}, <<: {b: 2}}\n", `{"item":{"a":1,"b":2}}`, "duplicate"},
		{"malformed merge", "item: {<<: 1}\n", `{"item":{}}`, "mapping"},
		{"alias cycle", "item: &item [*item]\n", `{"item":[]}`, "cycle"},
		{"merge cycle", "item: &item {<<: *item}\n", `{"item":{}}`, "cycle"},
		{"non-string keys", "1: one\n", `{"1":"one"}`, "keys must be strings"},
		{"custom YAML tag", "value: !local text\n", `{"value":"text"}`, "type differs"},
		{"binary tag has no JSON type", "value: !!binary YQ==\n", `{"value":"YQ=="}`, "type differs"},
		{"quoted date", "value: '2026-10-08'\n", `{"value":"2026-10-08"}`, ""},
		{"timestamp has no JSON type", "value: !!timestamp 2026-10-08\n", `{"value":"2026-10-08"}`, "type differs"},
		{"non-finite number", "value: .inf\n", `{"value":1}`, "type differs"},
		{"YAML root array", "[1, 2]\n", `{}`, "must be a mapping"},
		{"extra YAML document", "a: 1\n---\nb: 2\n", `{"a":1}`, "one YAML document"},
		{"extra field", "a: 1\nb: 2\n", `{"a":1}`, "fields differ"},
		{"different array order", "a: [1, 2]\n", `{"a":[2,1]}`, "differs"},
		{"invalid YAML", "a: [\n", `{}`, "invalid Skill YAML"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := compareSkillFrontmatter([]byte(test.yaml), json.RawMessage(test.json))
			if test.want == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, test.want)
		})
	}
}
