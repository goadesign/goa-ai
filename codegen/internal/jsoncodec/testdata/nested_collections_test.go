package types_test

import (
	"reflect"
	"testing"

	gentypes "codec.local/gen/types"
)

func TestNestedCollections(t *testing.T) {
	checkCollection(t, gentypes.DecodeNestedEntries, gentypes.EncodeNestedEntries,
		[]string{`[]`, `[null,[]]`, `[[null,{"Label":""}],null]`},
		[]string{`null`, `[[{}]]`, `[[{"Label":null}]]`, `[[{"Label":"","extra":true}]]`})
	checkCollection(t, gentypes.DecodeRequiredRows, gentypes.EncodeRequiredRows,
		[]string{`[]`, `[[],[null,{"Label":""}]]`},
		[]string{`null`, `[null]`, `[[],null]`, `[[{}]]`})
	if data, err := gentypes.EncodeRequiredRows(gentypes.RequiredRows{nil}); err == nil || data != nil {
		t.Fatalf("required row produced bytes: %s, err %v", data, err)
	}
	checkCollection(t, gentypes.DecodeMatrix, gentypes.EncodeMatrix,
		[]string{`[]`, `[null,[]]`, `[[""],["a","b"]]`},
		[]string{`null`, `[[null]]`, `[[1]]`})
	checkCollection(t, gentypes.DecodeChoices, gentypes.EncodeChoices,
		[]string{`[]`, `[{"type":"text","value":""},{"type":"entry","value":{"Label":""}}]`},
		[]string{`null`, `[null]`, `[{}]`, `[{"type":"entry","value":null}]`,
			`[{"type":"entry","value":{}}]`, `[{"type":"text","value":null}]`,
			`[{"type":"unknown","value":""}]`})
	checkCollection(t, gentypes.DecodeEntryMap, gentypes.EncodeEntryMap,
		[]string{`{}`, `{"nil":null,"value":{"Label":""}}`},
		[]string{`null`, `{"bad":{}}`, `{"bad":{"Label":null}}`, `{"bad":{"Label":"","extra":true}}`})
	checkCollection(t, gentypes.DecodeRowMap, gentypes.EncodeRowMap,
		[]string{`{}`, `{"empty":[],"nil":null,"value":["a"]}`},
		[]string{`null`, `{"bad":[null]}`, `{"bad":[1]}`})
	checkCollection(t, gentypes.DecodeBlobMap, gentypes.EncodeBlobMap,
		[]string{`{}`, `{"empty":"","nil":null,"value":"YQ=="}`},
		[]string{`null`, `{"bad":[]}`, `{"bad":"!"}`})
	checkCollection(t, gentypes.DecodeLabelMap, gentypes.EncodeLabelMap,
		[]string{`{}`, `{"value":""}`},
		[]string{`null`, `{"bad":null}`, `{"bad":1}`})
}

func checkCollection[T any](t *testing.T, decode func([]byte) (T, error), encode func(T) ([]byte, error), valid, invalid []string) {
	t.Helper()
	for _, document := range valid {
		value, err := decode([]byte(document))
		if err != nil {
			t.Fatalf("decode %s: %v", document, err)
		}
		data, err := encode(value)
		if err != nil || string(data) != document {
			t.Fatalf("round trip %s: got %s, err %v", document, data, err)
		}
		again, err := decode(data)
		if err != nil || !reflect.DeepEqual(again, value) {
			t.Fatalf("changed collection after second decode: %s, err %v", data, err)
		}
	}
	for _, document := range invalid {
		var zero T
		value, err := decode([]byte(document))
		if err == nil || !reflect.DeepEqual(value, zero) {
			t.Fatalf("invalid collection %s produced %#v, err %v", document, value, err)
		}
	}
}
