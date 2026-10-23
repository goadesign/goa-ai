// These tests call newly generated framework validators without encoding JSON first.
package types

import (
	"reflect"
	"testing"
)

func TestStandaloneUnionValueValidation(t *testing.T) {
	for _, test := range []struct {
		name    string
		value   func() Choice
		invalid bool
	}{
		{"missing kind", func() Choice {
			return Choice{}
		}, true},
		{"unknown kind", func() Choice {
			return Choice(ChoiceValue{kind: "unknown"})
		}, true},
		{"nil selected object", func() Choice {
			return Choice(NewChoiceValueEmpty(nil))
		}, true},
		{"empty object", func() Choice {
			return Choice(NewChoiceValueEmpty(&Empty{}))
		}, false},
		{"empty array", func() Choice {
			return Choice(NewChoiceValueWords([]string{}))
		}, false},
		{"empty map", func() Choice {
			return Choice(NewChoiceValueCounts(map[string]int{}))
		}, false},
		{"empty text", func() Choice {
			return Choice(NewChoiceValueText(""))
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, before := test.value(), test.value()
			derived := Derived(value)
			holder := &Holder{Selected: value}
			for _, check := range []struct {
				name string
				err  error
			}{
				{"named root", ValidateChoiceValue(&value)},
				{"derived root", ValidateDerivedValue(&derived)},
				{"nested required", ValidateHolderValue(holder)},
			} {
				if (check.err != nil) != test.invalid {
					t.Errorf("%s invalid=%t want=%t: %v", check.name, check.err != nil, test.invalid, check.err)
				}
			}
			if !reflect.DeepEqual(before, value) || !reflect.DeepEqual(Derived(before), derived) || !reflect.DeepEqual(&Holder{Selected: before}, holder) {
				t.Error("validation changed the caller's union")
			}
			_, err := EncodeChoice(&value)
			if (err != nil) != test.invalid {
				t.Errorf("encode invalid=%t want=%t: %v", err != nil, test.invalid, err)
			}
			if !test.invalid {
				encoded, err := EncodeHolder(holder)
				if err != nil {
					t.Fatal(err)
				}
				decoded, err := DecodeHolder(encoded)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(holder, decoded) {
					t.Errorf("round trip changed value: %#v %#v", holder, decoded)
				}
			}
		})
	}
	for _, test := range []struct {
		name    string
		change  func(*Holder)
		invalid bool
	}{
		{"optional absent", func(v *Holder) {
		}, false},
		{"optional unknown kind", func(v *Holder) {
			v.Optional = Choice(ChoiceValue{kind: "unknown"})
		}, true},
		{"optional selected nil", func(v *Holder) {
			v.Optional = Choice(NewChoiceValueEmpty(nil))
		}, true},
		{"derived absent", func(v *Holder) {
		}, false},
		{"derived selected nil", func(v *Holder) {
			v.Derived = Derived(NewChoiceValueEmpty(nil))
		}, true},
		{"collection absent", func(v *Holder) {
			v.Items = nil
		}, false},
		{"collection allocated empty", func(v *Holder) {
			v.Items = []Choice{}
		}, false},
		{"collection selected nil", func(v *Holder) {
			v.Items = []Choice{Choice(NewChoiceValueEmpty(nil))}
		}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := &Holder{Selected: Choice(NewChoiceValueEmpty(&Empty{}))}
			before := &Holder{Selected: Choice(NewChoiceValueEmpty(&Empty{}))}
			test.change(value)
			test.change(before)
			err := ValidateHolderValue(value)
			if (err != nil) != test.invalid {
				t.Errorf("invalid=%t want=%t: %v", err != nil, test.invalid, err)
			}
			if !reflect.DeepEqual(before, value) {
				t.Error("validation changed original field/collection presence")
			}
		})
	}
}
