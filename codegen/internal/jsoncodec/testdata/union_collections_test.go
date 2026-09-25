// These checks call generated codecs with value unions and pointer objects.
package types_test

import (
	"reflect"
	"strings"
	"testing"

	genordinary "codec.local/gen/ordinary"
	gentypes "codec.local/gen/types"
)

func TestUnionCollectionsRoundTrip(t *testing.T) {
	for _, codec := range []struct {
		name   string
		encode func(*gentypes.UnionCollections) ([]byte, error)
		decode func([]byte) (*gentypes.UnionCollections, error)
	}{
		{"ordinary", genordinary.EncodeUnionCollections, genordinary.DecodeUnionCollections},
		{"original", gentypes.EncodeUnionCollections, gentypes.DecodeUnionCollections},
	} {
		t.Run(codec.name, func(t *testing.T) {
			value := validUnionCollections()
			want := validUnionCollections()
			entry := value.Objects[1]
			note := entry.Note
			data, err := codec.encode(value)
			if err != nil {
				t.Fatal(err)
			}
			arrayEntry, arraySelected := value.Choices[1].AsEntry()
			mapEntry, mapSelected := value.ByName["entry"].AsEntry()
			if !reflect.DeepEqual(value, want) || value.Objects[1] != entry ||
				value.ObjectsByName["entry"] != entry || entry.Note != note ||
				!arraySelected || arrayEntry != entry || !mapSelected || mapEntry != entry {
				t.Fatal("encoding changed the caller's values or pointers")
			}
			decoded, err := codec.decode(data)
			if err != nil || !reflect.DeepEqual(decoded, want) {
				t.Fatalf("round trip changed union collections: got %#v, err %v", decoded, err)
			}
			again, err := codec.encode(decoded)
			if err != nil || string(again) != string(data) {
				t.Fatalf("second encoding changed JSON: %s, err %v", again, err)
			}
		})
	}
}

func TestUnionCollectionsRejectInvalidValues(t *testing.T) {
	for _, codec := range []struct {
		name   string
		encode func(*gentypes.UnionCollections) ([]byte, error)
	}{
		{"ordinary", genordinary.EncodeUnionCollections},
		{"original", gentypes.EncodeUnionCollections},
	} {
		t.Run(codec.name, func(t *testing.T) {
			for _, test := range []struct {
				name   string
				change func(*gentypes.UnionCollections)
			}{
				{"array unset", func(v *gentypes.UnionCollections) { v.Choices[0] = gentypes.ArrayChoice{} }},
				{"map unset", func(v *gentypes.UnionCollections) { v.ByName["empty"] = gentypes.MapChoice{} }},
				{"array nil branch", func(v *gentypes.UnionCollections) { v.Choices[0] = gentypes.NewArrayChoiceEntry(nil) }},
				{"map nil branch", func(v *gentypes.UnionCollections) { v.ByName["empty"] = gentypes.NewMapChoiceEntry(nil) }},
				{"array text", func(v *gentypes.UnionCollections) { v.Choices[0] = gentypes.NewArrayChoiceText("!") }},
				{"map text", func(v *gentypes.UnionCollections) { v.ByName["empty"] = gentypes.NewMapChoiceText("!") }},
				{"array object", func(v *gentypes.UnionCollections) {
					v.Choices[0] = gentypes.NewArrayChoiceEntry(&gentypes.CollectionEntry{})
				}},
				{"map object", func(v *gentypes.UnionCollections) {
					v.ByName["empty"] = gentypes.NewMapChoiceEntry(&gentypes.CollectionEntry{})
				}},
				{"object array", func(v *gentypes.UnionCollections) { v.Objects[1] = &gentypes.CollectionEntry{} }},
				{"object map", func(v *gentypes.UnionCollections) { v.ObjectsByName["entry"] = &gentypes.CollectionEntry{} }},
			} {
				t.Run(test.name, func(t *testing.T) {
					value, want := validUnionCollections(), validUnionCollections()
					test.change(value)
					test.change(want)
					if data, err := codec.encode(value); err == nil || data != nil {
						t.Fatalf("invalid typed value produced bytes: %s, err %v", data, err)
					}
					if !reflect.DeepEqual(value, want) {
						t.Fatal("rejected encoding changed the caller's value")
					}
				})
			}
		})
	}
}

func TestUnionCollectionsRejectInvalidJSON(t *testing.T) {
	for _, codec := range []struct {
		name   string
		decode func([]byte) (*gentypes.UnionCollections, error)
	}{
		{"ordinary", genordinary.DecodeUnionCollections},
		{"original", gentypes.DecodeUnionCollections},
	} {
		t.Run(codec.name, func(t *testing.T) {
			for _, document := range []string{
				`{"choices":[null],"byName":{},"objects":[],"objectsByName":{}}`,
				`{"choices":[],"byName":{"one":null},"objects":[],"objectsByName":{}}`,
				`{"choices":[{}],"byName":{},"objects":[],"objectsByName":{}}`,
				`{"choices":[],"byName":{"one":{}},"objects":[],"objectsByName":{}}`,
				`{"choices":[{"type":"entry","value":null}],"byName":{},"objects":[],"objectsByName":{}}`,
				`{"choices":[],"byName":{"one":{"type":"entry","value":null}},"objects":[],"objectsByName":{}}`,
				`{"choices":[{"type":"unknown","value":""}],"byName":{},"objects":[],"objectsByName":{}}`,
				`{"choices":[],"byName":{"one":{"type":"unknown","value":""}},"objects":[],"objectsByName":{}}`,
				`{"choices":[{"type":"text","value":"!"}],"byName":{},"objects":[],"objectsByName":{}}`,
				`{"choices":[],"byName":{"one":{"type":"text","value":"!"}},"objects":[],"objectsByName":{}}`,
			} {
				if value, err := codec.decode([]byte(document)); err == nil || value != nil {
					t.Fatalf("invalid JSON produced a value: %s, got %#v, err %v", document, value, err)
				}
			}
		})
	}
}

func TestUnionCollectionsSelectedChecksText(t *testing.T) {
	invalid := string([]byte{0xff})
	for _, test := range []struct {
		name   string
		change func(*gentypes.UnionCollections)
	}{
		{"array branch", func(v *gentypes.UnionCollections) {
			v.Choices[0] = gentypes.NewArrayChoiceText(gentypes.ArrayChoiceBranchText(invalid))
		}},
		{"map branch", func(v *gentypes.UnionCollections) {
			v.ByName["empty"] = gentypes.NewMapChoiceText(gentypes.MapChoiceBranchText(invalid))
		}},
		{"array object branch", func(v *gentypes.UnionCollections) {
			v.Choices[0] = gentypes.NewArrayChoiceEntry(&gentypes.CollectionEntry{Label: invalid})
		}},
		{"map object branch", func(v *gentypes.UnionCollections) {
			v.ByName["empty"] = gentypes.NewMapChoiceEntry(&gentypes.CollectionEntry{Label: "ok", Note: &invalid})
		}},
		{"object array", func(v *gentypes.UnionCollections) {
			v.Objects[1] = &gentypes.CollectionEntry{Label: invalid}
		}},
		{"object map", func(v *gentypes.UnionCollections) {
			v.ObjectsByName["entry"] = &gentypes.CollectionEntry{Label: "ok", Note: &invalid}
		}},
		{"map key", func(v *gentypes.UnionCollections) {
			v.ByName[invalid] = gentypes.NewMapChoiceText("")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, want := validUnionCollections(), validUnionCollections()
			test.change(value)
			test.change(want)
			data, err := gentypes.EncodeUnionCollections(value)
			if err == nil || data != nil || !strings.Contains(err.Error(), "UTF-8") {
				t.Fatalf("invalid text was not rejected before conversion: %s, err %v", data, err)
			}
			if !reflect.DeepEqual(value, want) {
				t.Fatal("text validation changed the caller's value")
			}
		})
	}
}

// validUnionCollections includes value unions, a shared object, nil optional
// pointers, and nullable object-array and object-map elements.
func validUnionCollections() *gentypes.UnionCollections {
	note := "note"
	entry := &gentypes.CollectionEntry{Label: "ok", Note: &note}
	return &gentypes.UnionCollections{
		Choices: []gentypes.ArrayChoice{
			gentypes.NewArrayChoiceText(""),
			gentypes.NewArrayChoiceEntry(entry),
			gentypes.NewArrayChoiceEntry(&gentypes.CollectionEntry{Label: "plain"}),
		},
		ByName: map[string]gentypes.MapChoice{
			"empty": gentypes.NewMapChoiceText(""),
			"entry": gentypes.NewMapChoiceEntry(entry),
			"plain": gentypes.NewMapChoiceEntry(&gentypes.CollectionEntry{Label: "plain"}),
		},
		Objects:       []*gentypes.CollectionEntry{nil, entry},
		ObjectsByName: map[string]*gentypes.CollectionEntry{"entry": entry, "nil": nil},
	}
}
