package types_test

import (
	"reflect"
	"testing"

	genleft "codec.local/gen/left/types"
	genordinary "codec.local/gen/ordinary"
	genright "codec.local/gen/right/types"
	gentypes "codec.local/gen/types"
)

func TestNamedUnionCollections(t *testing.T) {
	for _, codec := range []struct {
		name   string
		encode func(*gentypes.NamedCollections) ([]byte, error)
		decode func([]byte) (*gentypes.NamedCollections, error)
	}{
		{"ordinary", genordinary.EncodeNamedCollections, genordinary.DecodeNamedCollections},
		{"original", gentypes.EncodeNamedCollections, gentypes.DecodeNamedCollections},
	} {
		t.Run(codec.name, func(t *testing.T) {
			text := genright.Derived(genleft.NewChoiceText(""))
			number := genright.Derived(genleft.NewChoiceNumber(1))
			value := &gentypes.NamedCollections{
				Single:  text,
				Choices: []genright.Derived{text, number},
				ByName:  map[string]genright.Derived{"text": text, "number": number},
			}
			data, err := codec.encode(value)
			if err != nil {
				t.Fatal(err)
			}
			if value.Single != text || value.Choices[0] != text || value.Choices[1] != number ||
				value.ByName["text"] != text || value.ByName["number"] != number {
				t.Fatal("encoding changed a named union")
			}
			decoded, err := codec.decode(data)
			if err != nil || !reflect.DeepEqual(value, decoded) {
				t.Fatalf("round trip = %#v, error %v", decoded, err)
			}
			invalid := genright.Derived(genleft.NewChoiceNumber(0))
			for _, where := range []string{"field", "array", "map"} {
				bad := &gentypes.NamedCollections{
					Single:  text,
					Choices: []genright.Derived{number},
					ByName:  map[string]genright.Derived{"number": number},
				}
				switch where {
				case "field":
					bad.Single = invalid
				case "array":
					bad.Choices[0] = invalid
				case "map":
					bad.ByName["number"] = invalid
				}
				if data, err := codec.encode(bad); err == nil || data != nil {
					t.Fatalf("accepted invalid %s: %s, error %v", where, data, err)
				}
			}
			for _, document := range []string{
				`{"single":{"type":"number","value":0},"choices":[],"byName":{}}`,
				`{"single":{"type":"text","value":""},"choices":[{"type":"number","value":0}],"byName":{}}`,
				`{"single":{"type":"text","value":""},"choices":[],"byName":{"bad":{"type":"number","value":0}}}`,
			} {
				if value, err := codec.decode([]byte(document)); err == nil || value != nil {
					t.Fatalf("accepted invalid JSON: %s, value %#v, error %v", document, value, err)
				}
			}
		})
	}
}

func TestNamedUnionCollectionsRejectInvalidText(t *testing.T) {
	value := &gentypes.NamedCollections{
		Single:  genright.Derived(genleft.NewChoiceText(genleft.ChoiceBranchText(string([]byte{0xff})))),
		Choices: []genright.Derived{},
		ByName:  map[string]genright.Derived{},
	}
	if data, err := gentypes.EncodeNamedCollections(value); err == nil || data != nil {
		t.Fatalf("accepted invalid text: %s, error %v", data, err)
	}
}

func TestNamedUnionCollectionsRoots(t *testing.T) {
	for _, codec := range []struct {
		name          string
		encodeBase    func(*genleft.Base) ([]byte, error)
		decodeBase    func([]byte) (*genleft.Base, error)
		encodeDerived func(*genright.Derived) ([]byte, error)
		decodeDerived func([]byte) (*genright.Derived, error)
	}{
		{"ordinary", genordinary.EncodeBase, genordinary.DecodeBase, genordinary.EncodeDerived, genordinary.DecodeDerived},
		{"original", genleft.EncodeBase, genleft.DecodeBase, genright.EncodeDerived, genright.DecodeDerived},
	} {
		t.Run(codec.name, func(t *testing.T) {
			for _, test := range []struct {
				value genleft.Choice
				json  string
			}{
				{genleft.NewChoiceText(""), `{"type":"text","value":""}`},
				{genleft.NewChoiceNumber(1), `{"type":"number","value":1}`},
			} {
				selected := test.value
				base, derived := genleft.Base(selected), genright.Derived(selected)
				data, err := codec.encodeBase(&base)
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != test.json {
					t.Fatalf("base JSON = %s, want %s", data, test.json)
				}
				decodedBase, err := codec.decodeBase(data)
				if err != nil || !reflect.DeepEqual(&base, decodedBase) {
					t.Fatalf("base round trip = %#v, error %v", decodedBase, err)
				}
				data, err = codec.encodeDerived(&derived)
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != test.json {
					t.Fatalf("derived JSON = %s, want %s", data, test.json)
				}
				decodedDerived, err := codec.decodeDerived(data)
				if err != nil || !reflect.DeepEqual(&derived, decodedDerived) {
					t.Fatalf("derived round trip = %#v, error %v", decodedDerived, err)
				}
				if genleft.Choice(base) != selected || genleft.Choice(derived) != selected {
					t.Fatal("encoding changed a named root")
				}
			}
			badBase := genleft.Base(genleft.NewChoiceNumber(0))
			if data, err := codec.encodeBase(&badBase); err == nil || data != nil {
				t.Fatalf("accepted invalid base: %s, error %v", data, err)
			}
			badDerived := genright.Derived(badBase)
			if data, err := codec.encodeDerived(&badDerived); err == nil || data != nil {
				t.Fatalf("accepted invalid derived: %s, error %v", data, err)
			}
		})
	}
}
