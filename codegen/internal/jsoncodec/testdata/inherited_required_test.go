package types_test

import (
	"testing"

	gentypes "codec.local/gen/types"
)

func TestInheritedRequiredValues(t *testing.T) {
	for _, note := range []string{"ready", ""} {
		value := &gentypes.Derived{Label: "valid", Note: &note}
		data, err := gentypes.EncodeDerived(value)
		if err != nil {
			t.Fatal(err)
		}
		got, err := gentypes.DecodeDerived(data)
		if err != nil || got.Label != value.Label || got.Note == nil || *got.Note != note {
			t.Fatalf("required pointer round trip: got %#v, err %v", got, err)
		}
		if value.Note != &note || *value.Note != note {
			t.Fatal("encoder changed the caller's required value")
		}
	}
	note := ""
	for _, invalid := range []*gentypes.Derived{
		{Label: "valid"},
		{Note: &note},
		{Label: string([]byte{0xff}), Note: &note},
	} {
		if data, err := gentypes.EncodeDerived(invalid); err == nil || data != nil {
			t.Fatalf("invalid original value produced bytes: %s, err %v", data, err)
		}
	}
	for _, document := range []string{
		`{"Label":"valid"}`,
		`{"Label":"valid","Note":null}`,
		`{"Label":"","Note":""}`,
	} {
		if got, err := gentypes.DecodeDerived([]byte(document)); err == nil || got != nil {
			t.Fatalf("invalid JSON produced value: %#v, err %v", got, err)
		}
	}
}
