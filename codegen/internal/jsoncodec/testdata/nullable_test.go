package types_test

import (
	"bytes"
	"strings"
	"testing"

	gentypes "codec.local/gen/types"
)

var (
	_ func(gentypes.Entries) ([]byte, error)         = gentypes.EncodeEntries
	_ func([]byte) (gentypes.Entries, error)         = gentypes.DecodeEntries
	_ func(gentypes.RequiredEntries) ([]byte, error) = gentypes.EncodeRequiredEntries
	_ func([]byte) (gentypes.RequiredEntries, error) = gentypes.DecodeRequiredEntries
)

func TestArrayRootOccurrences(t *testing.T) {
	for _, document := range []string{`[]`, `[null]`, `[null,{"Label":""},null]`} {
		got, err := gentypes.DecodeEntries([]byte(document))
		if err != nil {
			t.Fatalf("%s: %v", document, err)
		}
		first, err := gentypes.EncodeEntries(got)
		if err != nil || string(first) != document {
			t.Fatalf("changed array: %s %v", first, err)
		}
		again, err := gentypes.DecodeEntries(first)
		if err != nil {
			t.Fatal(err)
		}
		second, err := gentypes.EncodeEntries(again)
		if err != nil || !bytes.Equal(first, second) {
			t.Fatalf("changed encoding: %s %s %v", first, second, err)
		}
	}
	for _, document := range []string{`null`, `[{}]`, `[{"Label":null}]`, `[{"label":""}]`, `[{"Label":"","extra":true}]`} {
		got, err := gentypes.DecodeEntries([]byte(document))
		if err == nil || got != nil {
			t.Fatalf("usable invalid array %s: %#v %v", document, got, err)
		}
	}
	if data, err := gentypes.EncodeEntries(nil); err == nil || data != nil {
		t.Fatalf("usable nil root: %s %v", data, err)
	}
	for _, document := range []string{`null`, `[null]`, `[{"Label":""},null]`} {
		got, err := gentypes.DecodeRequiredEntries([]byte(document))
		if err == nil || got != nil {
			t.Fatalf("usable nonnullable array %s: %#v %v", document, got, err)
		}
	}
	if data, err := gentypes.EncodeRequiredEntries(gentypes.RequiredEntries{nil}); err == nil || data != nil {
		t.Fatalf("encoded null required element: %s %v", data, err)
	}
	for _, document := range []string{`[]`, `[{"Label":""}]`} {
		got, err := gentypes.DecodeRequiredEntries([]byte(document))
		if err != nil {
			t.Fatal(err)
		}
		data, err := gentypes.EncodeRequiredEntries(got)
		if err != nil || string(data) != document {
			t.Fatalf("changed required elements: %s %v", data, err)
		}
	}
}

func TestNilableAndValueElements(t *testing.T) {
	if got, err := gentypes.DecodeWords([]byte(`[null]`)); err == nil || got != nil {
		t.Fatalf("null became a string zero value: %#v %v", got, err)
	}
	got, err := gentypes.DecodeBlobs([]byte(`[null,""]`))
	if err != nil || len(got) != 2 || got[0] != nil || got[1] == nil {
		t.Fatalf("changed bytes: %#v %v", got, err)
	}
	data, err := gentypes.EncodeBlobs(got)
	if err != nil || string(data) != `[null,""]` {
		t.Fatalf("changed byte elements: %s %v", data, err)
	}
	if got, err := gentypes.DecodeRequiredWords([]byte(`[null]`)); err == nil || got != nil {
		t.Fatalf("null became a required string zero value: %#v %v", got, err)
	}
	words, err := gentypes.DecodeRequiredWords([]byte(`[""]`))
	if err != nil {
		t.Fatal(err)
	}
	data, err = gentypes.EncodeRequiredWords(words)
	if err != nil || string(data) != `[""]` {
		t.Fatalf("lost required empty string: %s %v", data, err)
	}
	if got, err := gentypes.DecodeRequiredBlobs([]byte(`[null]`)); err == nil || got != nil {
		t.Fatalf("accepted required null byte element: %#v %v", got, err)
	}
	if data, err := gentypes.EncodeRequiredBlobs(gentypes.RequiredBlobs{nil}); err == nil || data != nil {
		t.Fatalf("encoded required null byte element: %s %v", data, err)
	}
}

const containerDocument = `{"Single":{"Label":""},"Loose":[null,{"Label":""}],"Tight":[{"Label":""}],"Choice":{"type":"entry","value":{"Label":""}}}`

// The same named Entry is shared by every array, object and union occurrence.
// Array element permission must not change that Entry's own required shape.
func TestNullablePermissionBelongsToArrayOccurrence(t *testing.T) {
	for name, choice := range map[string]string{
		"entry": `{"type":"entry","value":{"Label":""}}`,
		"text":  `{"type":"text","value":""}`,
	} {
		t.Run(name, func(t *testing.T) {
			document := strings.Replace(containerDocument, `{"type":"entry","value":{"Label":""}}`, choice, 1)
			value, err := gentypes.DecodeContainer([]byte(document))
			if err != nil {
				t.Fatal(err)
			}
			if value.Single == nil || value.Single.Label != "" ||
				len(value.Loose) != 2 || value.Loose[0] != nil || value.Loose[1] == nil ||
				len(value.Tight) != 1 || value.Tight[0] == nil {
				t.Fatalf("changed shared Entry occurrences: %#v", value)
			}
			first, err := gentypes.EncodeContainer(value)
			if err != nil {
				t.Fatal(err)
			}
			again, err := gentypes.DecodeContainer(first)
			if err != nil {
				t.Fatal(err)
			}
			second, err := gentypes.EncodeContainer(again)
			if err != nil || !bytes.Equal(first, second) {
				t.Fatalf("unstable occurrence encoding: %s %s %v", first, second, err)
			}
		})
	}
}

func TestSharedEntryStrictNullOccurrences(t *testing.T) {
	const loose = `"Loose":[null,{"Label":""}]`
	const choice = `{"type":"entry","value":{"Label":""}}`
	for name, document := range map[string]string{
		"root":                      `null`,
		"missing required array":    strings.Replace(containerDocument, loose+`,`, "", 1),
		"null required loose array": strings.Replace(containerDocument, loose, `"Loose":null`, 1),
		"null required tight array": strings.Replace(containerDocument, `"Tight":[{"Label":""}]`, `"Tight":null`, 1),
		"required single":           strings.Replace(containerDocument, `"Single":{"Label":""}`, `"Single":null`, 1),
		"required element":          strings.Replace(containerDocument, `"Tight":[{"Label":""}]`, `"Tight":[null]`, 1),
		"union entry":               strings.Replace(containerDocument, choice, `{"type":"entry","value":null}`, 1),
		"union field":               strings.Replace(containerDocument, choice, `null`, 1),
		"union missing child":       strings.Replace(containerDocument, choice, `{"type":"entry","value":{}}`, 1),
		"union text":                strings.Replace(containerDocument, choice, `{"type":"text","value":null}`, 1),
		"missing child":             strings.Replace(containerDocument, loose, `"Loose":[null,{}]`, 1),
		"null child field":          strings.Replace(containerDocument, loose, `"Loose":[null,{"Label":null}]`, 1),
		"unknown child":             strings.Replace(containerDocument, loose, `"Loose":[null,{"Label":"","Extra":0}]`, 1),
		"case child":                strings.Replace(containerDocument, loose, `"Loose":[null,{"label":""}]`, 1),
		"duplicate child":           strings.Replace(containerDocument, loose, `"Loose":[null,{"Label":"","\u004cabel":"second"}]`, 1),
		"surrogate child":           strings.Replace(containerDocument, loose, `"Loose":[null,{"Label":"\ud800"}]`, 1),
		"UTF8 child":                strings.Replace(containerDocument, loose, `"Loose":[null,{"Label":"`+string([]byte{0xff})+`"}]`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			value, err := gentypes.DecodeContainer([]byte(document))
			if err == nil || value != nil {
				t.Fatalf("usable invalid occurrence: %#v %v; %s", value, err, document)
			}
		})
	}
	for name, mutate := range map[string]func(*gentypes.Container){
		"required loose array": func(v *gentypes.Container) { v.Loose = nil },
		"required tight array": func(v *gentypes.Container) { v.Tight = nil },
		"single":               func(v *gentypes.Container) { v.Single = nil },
		"required element":     func(v *gentypes.Container) { v.Tight[0] = nil },
		"union entry":          func(v *gentypes.Container) { v.Choice.SetEntry(nil) },
	} {
		t.Run("encode/"+name, func(t *testing.T) {
			value, err := gentypes.DecodeContainer([]byte(containerDocument))
			if err != nil {
				t.Fatal(err)
			}
			mutate(value)
			if data, err := gentypes.EncodeContainer(value); err == nil || data != nil {
				t.Fatalf("usable invalid occurrence encoding: %s %v", data, err)
			}
		})
	}
}
