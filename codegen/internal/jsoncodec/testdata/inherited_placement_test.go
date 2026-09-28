// These generated-package checks use public original-value codecs only.
package alpha_test

import (
	"reflect"
	"testing"

	alpha "codec.local/gen/alpha"
	beta "codec.local/gen/beta"
	shared "codec.local/gen/shared/types"
)

func TestInheritedOriginalValueCodecs(t *testing.T) {
	for _, choice := range []string{
		`{"type":"text","value":""}`,
		`{"type":"child","value":{"value":"child","next":{"value":"next"}}}`,
		`{"type":"words","value":["word"]}`,
		`{"type":"table","value":{"key":["word"]}}`,
	} {
		input := []byte(`{"child":{"value":"child","next":{"value":"next"}},"children":[{"value":"array"}],"by_name":{"key":{"value":"map"}},"entries":[{"choice":` + choice + `}]}`)
		first, err := alpha.DecodeLocal(input)
		if err != nil {
			t.Fatal(err)
		}
		second, err := beta.DecodeLocal(input)
		if err != nil {
			t.Fatal(err)
		}
		var left, right *shared.Child = first.Child, second.Child
		if !reflect.DeepEqual(left, right) {
			t.Fatal("services did not retain the same shared child value")
		}
		encoded, err := alpha.EncodeLocal(first)
		if err != nil {
			t.Fatal(err)
		}
		again, err := alpha.DecodeLocal(encoded)
		if err != nil || !reflect.DeepEqual(first, again) {
			t.Fatalf("original graph changed: %#v %v", again, err)
		}
	}
	if _, err := alpha.DecodeLocal([]byte(`{"child":{"value":"x"},"entries":[],"extra":true}`)); err == nil {
		t.Fatal("codec accepted an unknown field")
	}
}
