package types_test

import (
	"bytes"
	"testing"

	gentypes "codec.local/gen/types"
)

const rulesDocument = `{"Enabled":false,"Count":0,"Text":"","Token":"b9efb34b-0ac9-47ed-b5e0-5714dca660b7","Entry":{"Label":"雪"},"Other":{"Value":"A"},"Mode":"fast","Level":1,"Inclusive":0,"Items":[null,{"Label":"ok"}],"RequiredItems":[],"Labels":{}}`

func ruleValue(t *testing.T) *gentypes.Record {
	t.Helper()
	value, err := gentypes.DecodeRecord([]byte(rulesDocument))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestOriginalRules(t *testing.T) {
	for name, mutate := range map[string]func(*gentypes.Record){
		"alias minimum":              func(v *gentypes.Record) { v.Count = -1 },
		"format":                     func(v *gentypes.Record) { v.Token = "bad" },
		"renamed field minimum":      func(v *gentypes.Record) { v.Entry.Display = "" },
		"renamed field maximum":      func(v *gentypes.Record) { v.Entry.Display = "four" },
		"other owner pattern":        func(v *gentypes.Record) { v.Other.Value = "bad" },
		"enum":                       func(v *gentypes.Record) { v.Mode = "other" },
		"exclusive minimum":          func(v *gentypes.Record) { v.Level = 0 },
		"exclusive maximum":          func(v *gentypes.Record) { v.Level = 10 },
		"minimum":                    func(v *gentypes.Record) { v.Inclusive = -1 },
		"maximum":                    func(v *gentypes.Record) { v.Inclusive = 3 },
		"array length":               func(v *gentypes.Record) { v.Items = append(v.Items, v.Entry) },
		"array element":              func(v *gentypes.Record) { v.Items[1].Display = "" },
		"required array element":     func(v *gentypes.Record) { v.RequiredItems = append(v.RequiredItems, nil) },
		"map length":                 func(v *gentypes.Record) { v.Labels = map[string]string{"a": "", "b": "", "c": ""} },
		"required object":            func(v *gentypes.Record) { v.Entry = nil },
		"required other object":      func(v *gentypes.Record) { v.Other = nil },
		"required array":             func(v *gentypes.Record) { v.Items = nil },
		"required nonnullable array": func(v *gentypes.Record) { v.RequiredItems = nil },
		"required map":               func(v *gentypes.Record) { v.Labels = nil },
	} {
		t.Run(name, func(t *testing.T) {
			value := ruleValue(t)
			mutate(value)
			if data, err := gentypes.EncodeRecord(value); err == nil || data != nil {
				t.Fatalf("usable invalid encoding: %s %v", data, err)
			}
		})
	}
}

func TestExplicitZeroFalseEmptyAndNullable(t *testing.T) {
	value := ruleValue(t)
	value.Items = value.Items[:0]
	data, err := gentypes.EncodeRecord(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, exact := range []string{`"Enabled":false`, `"Count":0`, `"Text":""`, `"Items":[]`, `"RequiredItems":[]`, `"Labels":{}`} {
		if !bytes.Contains(data, []byte(exact)) {
			t.Fatalf("missing %s: %s", exact, data)
		}
	}
	if value.Enabled || value.Count != 0 || value.Text != "" {
		t.Fatalf("mutated input: %#v", value)
	}
	again, err := gentypes.DecodeRecord(data)
	if err != nil {
		t.Fatal(err)
	}
	next, err := gentypes.EncodeRecord(again)
	if err != nil || !bytes.Equal(data, next) {
		t.Fatalf("unstable: %s %s %v", data, next, err)
	}
	value = ruleValue(t)
	data, err = gentypes.EncodeRecord(value)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"Items":[null,{"Label":"ok"}]`)) {
		t.Fatalf("lost nullable element: %s", data)
	}
}
