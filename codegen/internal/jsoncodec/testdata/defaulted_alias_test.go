// These tests exercise defaults and validation through the generated codecs.
package types_test

import (
	"maps"
	"reflect"
	"slices"
	"testing"

	gentypes "codec.local/gen/types"
)

func TestDefaultedAliases(t *testing.T) {
	optional := gentypes.OptionalLabel("detailed")
	value := &gentypes.Record{
		Label: "brief", Optional: &optional, Required: "detailed",
		Items: []gentypes.Label{"brief", "detailed"},
		Index: map[string]gentypes.Label{"first": "brief"},
	}
	before := snapshotRecord(value)
	data, err := gentypes.EncodeRecord(value)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := gentypes.DecodeRecord(data)
	if err != nil || !reflect.DeepEqual(decoded, before) {
		t.Fatalf("round trip = %#v, %v; want %#v", decoded, err, before)
	}
	if !reflect.DeepEqual(value, before) {
		t.Error("encoding changed the caller's value")
	}
	defaulted, err := gentypes.DecodeRecord([]byte(`{"Required":"brief"}`))
	if err != nil {
		t.Fatal(err)
	}
	if defaulted.Label != "brief" || defaulted.Required != "brief" || defaulted.Optional != nil {
		t.Fatalf("defaults changed requiredness or optional presence: %#v", defaulted)
	}

	for _, body := range []string{
		`{}`,
		`{"Required":null}`,
		`{"Required":"brief","Label":""}`,
		`{"Required":"brief","Label":"other"}`,
		`{"Required":"brief","Optional":"other"}`,
		`{"Required":"brief","Items":["other"]}`,
		`{"Required":"brief","Index":{"first":"other"}}`,
	} {
		if result, err := gentypes.DecodeRecord([]byte(body)); err == nil || result != nil {
			t.Errorf("invalid JSON %s returned %#v, %v", body, result, err)
		}
	}
	invalidOptional := gentypes.OptionalLabel("other")
	for _, invalid := range []*gentypes.Record{
		{Required: "brief"},
		{Label: "other", Required: "brief"},
		{Label: "brief", Required: "other"},
		{Label: "brief", Required: "brief", Optional: &invalidOptional},
		{Label: "brief", Required: "brief", Items: []gentypes.Label{"other"}},
		{Label: "brief", Required: "brief", Index: map[string]gentypes.Label{"first": "other"}},
	} {
		before := snapshotRecord(invalid)
		if data, err := gentypes.EncodeRecord(invalid); err == nil || data != nil {
			t.Errorf("invalid value %#v returned %s, %v", invalid, data, err)
		}
		if !reflect.DeepEqual(before, invalid) {
			t.Error("encoding changed the caller's value")
		}
	}
}

// snapshotRecord copies each mutable value so an encoder mutation cannot also
// change the expected input retained by the test.
func snapshotRecord(value *gentypes.Record) *gentypes.Record {
	snapshot := *value
	if value.Optional != nil {
		optional := *value.Optional
		snapshot.Optional = &optional
	}
	snapshot.Items = slices.Clone(value.Items)
	snapshot.Index = maps.Clone(value.Index)
	return &snapshot
}
