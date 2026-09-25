package types_test

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	gentypes "codec.local/gen/types"
)

var (
	_ func(*gentypes.Settings) ([]byte, error) = gentypes.EncodeSettings
	_ func([]byte) (*gentypes.Settings, error) = gentypes.DecodeSettings
)

const valid = `{"Enabled":false,"Count":0,"Title":"hello","Node":{"Label":"","Count":0},"Choice":{"type":"number","value":0}}`

func value(t *testing.T) *gentypes.Settings {
	t.Helper()
	got, err := gentypes.DecodeSettings([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestRequiredZeroAndIntegerWidth(t *testing.T) {
	for _, number := range []string{"0", "2147483647", "4294967596", "9007199254740993", "9223372036854775807"} {
		document := strings.Replace(valid, `"Count":0`, `"Count":`+number, 1)
		got, err := gentypes.DecodeSettings([]byte(document))
		want, widthErr := strconv.ParseInt(number, 10, strconv.IntSize)
		if widthErr != nil {
			if err == nil || got != nil {
				t.Fatalf("accepted out-of-width int %s: %#v %v", number, got, err)
			}
			continue
		}
		if err != nil || got == nil || int64(got.Count) != want || got.Enabled {
			t.Fatalf("lost integer/false %s: %#v %v", number, got, err)
		}
		encoded, err := gentypes.EncodeSettings(got)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(encoded, []byte(`"Enabled":false`)) || !bytes.Contains(encoded, []byte(`"Count":`+number)) {
			t.Fatalf("lost presence or integer: %s", encoded)
		}
		again, err := gentypes.DecodeSettings(encoded)
		if err != nil || again.Count != got.Count {
			t.Fatalf("round trip: %#v %v", again, err)
		}
	}
}

func TestStrictDecode(t *testing.T) {
	cases := map[string]string{
		"missing false default":            strings.Replace(valid, `"Enabled":false,`, "", 1),
		"missing zero":                     strings.Replace(valid, `"Count":0,`, "", 1),
		"missing nested":                   strings.Replace(valid, `"Label":"",`, "", 1),
		"null root":                        "null",
		"null required":                    strings.Replace(valid, `"Enabled":false`, `"Enabled":null`, 1),
		"null required object":             strings.Replace(valid, `{"Label":"","Count":0}`, `null`, 1),
		"null optional":                    strings.Replace(valid, `"Title":"hello"`, `"Title":"hello","Labels":null`, 1),
		"unknown":                          strings.Replace(valid, `"Title":"hello"`, `"Title":"hello","extra":0`, 1),
		"case":                             strings.Replace(valid, `"Enabled"`, `"enabled"`, 1),
		"nested case":                      strings.Replace(valid, `"Label"`, `"label"`, 1),
		"duplicate":                        strings.Replace(valid, `"Enabled":false`, `"Enabled":true,"Enabled":false`, 1),
		"escaped duplicate":                strings.Replace(valid, `"Enabled":false`, `"Enabled":true,"\u0045nabled":false`, 1),
		"nested duplicate":                 strings.Replace(valid, `"Label":""`, `"Label":"x","Label":""`, 1),
		"map duplicate":                    strings.Replace(valid, `"Title":"hello"`, `"Title":"hello","Labels":{"x":"a","\u0078":"b"}`, 1),
		"high surrogate":                   strings.Replace(valid, `"hello"`, `"\ud800"`, 1),
		"low surrogate":                    strings.Replace(valid, `"hello"`, `"\udfff"`, 1),
		"wrong pair":                       strings.Replace(valid, `"hello"`, `"\ud800\u0041"`, 1),
		"bad UTF8":                         strings.Replace(valid, "hello", string([]byte{0xff}), 1),
		"bad UTF8 key":                     strings.Replace(valid, `"Title":"hello"`, `"Title":"hello","Labels":{"`+string([]byte{0xff})+`":"a"}`, 1),
		"range":                            strings.Replace(valid, `"Count":0`, `"Count":9223372036854775808`, 1),
		"negative":                         strings.Replace(valid, `"Count":0`, `"Count":-1`, 1),
		"fraction":                         strings.Replace(valid, `"Count":0`, `"Count":1.5`, 1),
		"exponent":                         strings.Replace(valid, `"Count":0`, `"Count":1e2`, 1),
		"empty title":                      strings.Replace(valid, `"hello"`, `""`, 1),
		"extra document":                   valid + " {}",
		"trailing invalid":                 valid + " x",
		"unknown branch":                   strings.Replace(valid, `"number"`, `"unknown"`, 1),
		"missing branch":                   strings.Replace(valid, `"type":"number",`, "", 1),
		"missing value":                    strings.Replace(valid, `,"value":0`, "", 1),
		"null value":                       strings.Replace(valid, `"value":0`, `"value":null`, 1),
		"union case":                       strings.Replace(valid, `"type":"number"`, `"Type":"number"`, 1),
		"union unknown":                    strings.Replace(valid, `"value":0`, `"value":0,"extra":1`, 1),
		"branch unknown":                   strings.Replace(valid, `{"type":"number","value":0}`, `{"type":"text","value":{"value":"x","extra":0}}`, 1),
		"branch missing":                   strings.Replace(valid, `{"type":"number","value":0}`, `{"type":"text","value":{}}`, 1),
		"branch case":                      strings.Replace(valid, `{"type":"number","value":0}`, `{"type":"text","value":{"Value":"x"}}`, 1),
		"required array element":           strings.Replace(valid, `"Title":"hello"`, `"Title":"hello","RequiredNodes":[null]`, 1),
		"nested required array element":    strings.Replace(valid, `"Label":""`, `"Label":"","RequiredEntries":[null]`, 1),
		"null object union branch":         strings.Replace(valid, `{"type":"number","value":0}`, `{"type":"node","value":null}`, 1),
		"invalid nonnull nullable element": strings.Replace(valid, `"Title":"hello"`, `"Title":"hello","Nodes":[null,{}]`, 1),
	}
	for name, document := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := gentypes.DecodeSettings([]byte(document))
			if err == nil || got != nil {
				t.Fatalf("usable invalid value: %#v; error %v; input %q", got, err, document)
			}
		})
	}
}

func TestUnicodeAndDeterministicOutput(t *testing.T) {
	for _, text := range []string{`"雪😀"`, `"\u96ea\ud83d\ude00"`, `"\\ud800"`, `"\ufffd"`} {
		got, err := gentypes.DecodeSettings([]byte(strings.Replace(valid, `"hello"`, text, 1)))
		if err != nil {
			t.Fatal(err)
		}
		first, err := gentypes.EncodeSettings(got)
		if err != nil {
			t.Fatal(err)
		}
		again, err := gentypes.DecodeSettings(first)
		if err != nil || again.Title != got.Title {
			t.Fatalf("text changed: %#v %v", again, err)
		}
		second, err := gentypes.EncodeSettings(again)
		if err != nil || !bytes.Equal(first, second) {
			t.Fatalf("unstable output: %s %s %v", first, second, err)
		}
	}
}

func TestTypedEncodeRejectsBadTextAndCycles(t *testing.T) {
	for name, mutate := range map[string]func(*gentypes.Settings){
		"root string":   func(v *gentypes.Settings) { v.Title = string([]byte{0xff}) },
		"nested string": func(v *gentypes.Settings) { v.Node.Label = string([]byte{0xff}) },
		"map key":       func(v *gentypes.Settings) { v.Labels = map[string]string{string([]byte{0xff}): "ok"} },
		"map value":     func(v *gentypes.Settings) { v.Labels = map[string]string{"ok": string([]byte{0xff})} },
		"object cycle":  func(v *gentypes.Settings) { v.Node.Next = v.Node },
		"longer cycle":  func(v *gentypes.Settings) { v.Node.Next = &gentypes.Node{Next: v.Node} },
		"map cycle":     func(v *gentypes.Settings) { v.Node.Children = map[string]*gentypes.Node{"self": v.Node} },
		"union cycle":   func(v *gentypes.Settings) { v.Node.Link.SetNode(v.Node) },
		"union string":  func(v *gentypes.Settings) { v.Node.Link.SetText(gentypes.LinkBranchText(string([]byte{0xff}))) },
	} {
		t.Run(name, func(t *testing.T) {
			v := value(t)
			mutate(v)
			data, err := gentypes.EncodeSettings(v)
			if err == nil || data != nil {
				t.Fatalf("usable invalid encoding: %q %v", data, err)
			}
		})
	}
	data, err := gentypes.EncodeSettings(nil)
	if err == nil || data != nil {
		t.Fatalf("nil root: %q %v", data, err)
	}
	v := value(t)
	v.Nodes = []*gentypes.Node{v.Node, v.Node}
	if _, err := gentypes.EncodeSettings(v); err != nil {
		t.Fatalf("shared acyclic node rejected: %v", err)
	}
}

func TestNullableObjectElementsRoundTrip(t *testing.T) {
	v := value(t)
	v.Node.Entries = []*gentypes.Node{nil, {Label: "nested", Count: 0}}
	v.Choice.SetNode(v.Node)
	v.Nodes = []*gentypes.Node{nil, v.Node, nil}
	first, err := gentypes.EncodeSettings(v)
	if err != nil {
		t.Fatal(err)
	}
	got, err := gentypes.DecodeSettings(first)
	if err != nil {
		t.Fatalf("cannot decode own nullable object array %s: %v", first, err)
	}
	if len(got.Nodes) != 3 || got.Nodes[0] != nil || got.Nodes[1] == nil || got.Nodes[2] != nil {
		t.Fatalf("changed nullable elements: %#v", got.Nodes)
	}
	branch, ok := got.Choice.AsNode()
	if !ok || len(branch.Entries) != 2 || branch.Entries[0] != nil || branch.Entries[1].Label != "nested" {
		t.Fatalf("changed nested array in union branch: %#v", branch)
	}
	second, err := gentypes.EncodeSettings(got)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("changed encoding: %s %s %v", first, second, err)
	}
	for name, mutate := range map[string]func(*gentypes.Settings){
		"required array":        func(v *gentypes.Settings) { v.RequiredNodes = []*gentypes.Node{nil} },
		"nested required array": func(v *gentypes.Settings) { v.Node.RequiredEntries = []*gentypes.Node{nil} },
	} {
		t.Run(name, func(t *testing.T) {
			v := value(t)
			mutate(v)
			data, err := gentypes.EncodeSettings(v)
			if err == nil || data != nil {
				t.Fatalf("usable invalid encoding: %q %v", data, err)
			}
		})
	}
}
