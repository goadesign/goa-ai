// These runtime checks call only the generated public codecs. Nil anonymous
// objects keep their existing meaning, and rejected text leaves input unchanged.
package types_test

import (
	"bytes"
	"strings"
	"testing"

	gentypes "codec.local/gen/types"
)

func TestOmittedOptionalInlineObject(t *testing.T) {
	input := &gentypes.InlineRoot{}
	first, err := gentypes.EncodeInlineRoot(input)
	if err != nil || string(first) != `{}` {
		t.Fatalf("encode omitted inline object: %s, %v", first, err)
	}
	if input.Details != nil || input.Items != nil || input.Lookup != nil {
		t.Fatalf("encoding changed omitted fields: %#v", input)
	}
	decoded, err := gentypes.DecodeInlineRoot(first)
	if err != nil || decoded == nil {
		t.Fatalf("decode empty object: %#v, %v", decoded, err)
	}
	if decoded.Details != nil || decoded.Items != nil || decoded.Lookup != nil {
		t.Fatalf("decoding populated omitted fields: %#v", decoded)
	}
	second, err := gentypes.EncodeInlineRoot(decoded)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("encode decoded empty object: %s, %v", second, err)
	}
}

func TestRequiredNilInlineObject(t *testing.T) {
	input := &gentypes.RequiredInlineRoot{}
	if data, err := gentypes.EncodeRequiredInlineRoot(input); err == nil || data != nil {
		t.Fatalf("required nil inline object produced bytes: %s, %v", data, err)
	}
	if input.Details != nil {
		t.Fatalf("encoding populated the required field: %#v", input)
	}
	for _, document := range []string{`{}`, `{"details":null}`} {
		if value, err := gentypes.DecodeRequiredInlineRoot([]byte(document)); err == nil || value != nil {
			t.Fatalf("missing required inline object %s produced %#v, %v", document, value, err)
		}
	}
	const present = `{"details":{}}`
	value, err := gentypes.DecodeRequiredInlineRoot([]byte(present))
	if err != nil || value == nil || value.Details == nil {
		t.Fatalf("decode present required inline object: %#v, %v", value, err)
	}
	if data, err := gentypes.EncodeRequiredInlineRoot(value); err != nil || string(data) != present {
		t.Fatalf("encode present required inline object: %s, %v", data, err)
	}
}

func TestNullableInlineObjectsRoundTrip(t *testing.T) {
	for _, document := range []string{
		`{"details":{}}`,
		`{"items":[null]}`,
		`{"lookup":{"nil":null}}`,
		`{"details":{"text":"detail"},"items":[null,{},{"text":"array"},null],"lookup":{"empty":{},"nil":null,"value":{"text":"map"}}}`,
	} {
		t.Run(document, func(t *testing.T) {
			value, err := gentypes.DecodeInlineRoot([]byte(document))
			if err != nil || value == nil {
				t.Fatalf("decode inline objects: %#v, %v", value, err)
			}
			first, err := gentypes.EncodeInlineRoot(value)
			if err != nil || string(first) != document {
				t.Fatalf("encode inline objects: %s, %v", first, err)
			}
			again, err := gentypes.DecodeInlineRoot(first)
			if err != nil || again == nil {
				t.Fatalf("decode encoded inline objects: %#v, %v", again, err)
			}
			second, err := gentypes.EncodeInlineRoot(again)
			if err != nil || !bytes.Equal(first, second) {
				t.Fatalf("changed inline objects after round trip: %s, %v", second, err)
			}
		})
	}
}

func TestPresentInlineObjectInvalidUTF8(t *testing.T) {
	const document = `{"details":{"text":"detail"},"items":[null,{"text":"array"}],"lookup":{"nil":null,"value":{"text":"map"}}}`
	for _, test := range []struct {
		name string
		text func(*gentypes.InlineRoot) *string
	}{
		{"field", func(v *gentypes.InlineRoot) *string { return v.Details.Text }},
		{"array member", func(v *gentypes.InlineRoot) *string { return v.Items[1].Text }},
		{"map member", func(v *gentypes.InlineRoot) *string { return v.Lookup["value"].Text }},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, err := gentypes.DecodeInlineRoot([]byte(document))
			if err != nil {
				t.Fatal(err)
			}
			invalid := string([]byte{0xff})
			*test.text(value) = invalid
			details, items, lookup := value.Details, value.Items, value.Lookup
			item, mapped := items[1], lookup["value"]
			detailText, itemText, mappedText := details.Text, item.Text, mapped.Text
			detailValue, itemValue, mappedValue := *detailText, *itemText, *mappedText
			if data, err := gentypes.EncodeInlineRoot(value); err == nil || data != nil || !strings.Contains(err.Error(), "invalid UTF-8") {
				t.Fatalf("invalid inline text produced bytes or wrong error: %s, %v", data, err)
			}
			nilMember, hasNilMember := value.Lookup["nil"]
			if value.Details != details || len(value.Items) != 2 || len(value.Lookup) != 2 ||
				&value.Items[0] != &items[0] || value.Items[0] != nil || value.Items[1] != item ||
				!hasNilMember || nilMember != nil || value.Lookup["value"] != mapped ||
				details.Text != detailText || item.Text != itemText || mapped.Text != mappedText ||
				*detailText != detailValue || *itemText != itemValue || *mappedText != mappedValue {
				t.Fatalf("encoding invalid text changed the caller's fields: %#v", value)
			}
			*test.text(value) = test.name
			if _, err := gentypes.EncodeInlineRoot(value); err != nil {
				t.Fatalf("valid inline text remained unusable: %v", err)
			}
		})
	}
}
