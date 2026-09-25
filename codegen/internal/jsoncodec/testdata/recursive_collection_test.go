package types_test

import (
	"reflect"
	"strings"
	"testing"

	gentypes "codec.local/gen/types"
)

func TestTree(t *testing.T) {
	leaf := gentypes.Tree{"empty": gentypes.Tree{}, "nil": nil}
	tree := gentypes.Tree{"first": leaf, "second": leaf}
	data, err := gentypes.EncodeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"first":{"empty":{},"nil":null},"second":{"empty":{},"nil":null}}`
	if string(data) != want || leaf["empty"] == nil || leaf["nil"] != nil {
		t.Fatalf("changed nil or empty map: %s, caller %#v", data, leaf)
	}
	got, err := gentypes.DecodeTree(data)
	if err != nil || !reflect.DeepEqual(got, tree) {
		t.Fatalf("shared tree round trip: got %#v, err %v", got, err)
	}
	tree["self"] = tree
	if data, err := gentypes.EncodeTree(tree); err == nil || data != nil || !strings.Contains(err.Error(), "cyclic Go value") {
		t.Fatalf("self cycle: got %q, err %v", data, err)
	}
	if reflect.ValueOf(tree["self"]).UnsafePointer() != reflect.ValueOf(tree).UnsafePointer() {
		t.Fatal("encoder changed the caller's cycle")
	}
	delete(tree, "self")
	if _, err := gentypes.EncodeTree(tree); err != nil {
		t.Fatalf("encode after cycle removed: %v", err)
	}
	a, b := gentypes.Tree{}, gentypes.Tree{}
	a["b"], b["a"] = b, a
	if data, err := gentypes.EncodeTree(a); err == nil || data != nil || !strings.Contains(err.Error(), "cyclic Go value") {
		t.Fatalf("mutual cycle: got %q, err %v", data, err)
	}
	invalid := gentypes.Tree{string([]byte{0xff}): gentypes.Tree{}}
	if data, err := gentypes.EncodeTree(invalid); err == nil || data != nil || !strings.Contains(err.Error(), "invalid UTF-8 map key") {
		t.Fatalf("invalid key: got %q, err %v", data, err)
	}
}

func TestList(t *testing.T) {
	leaf := gentypes.List{gentypes.List{}}
	list := gentypes.List{leaf, leaf}
	data, err := gentypes.EncodeList(list)
	if err != nil {
		t.Fatal(err)
	}
	got, err := gentypes.DecodeList(data)
	if err != nil || !reflect.DeepEqual(got, list) {
		t.Fatalf("shared list round trip: got %#v, err %v", got, err)
	}
	// The second element has the same data pointer as its parent but a
	// shorter length. It contains only the first, empty element.
	overlap := make(gentypes.List, 2)
	overlap[0] = gentypes.List{}
	overlap[1] = overlap[:1]
	data, err = gentypes.EncodeList(overlap)
	if err != nil {
		t.Fatalf("finite overlapping slices: %v", err)
	}
	got, err = gentypes.DecodeList(data)
	if err != nil || !reflect.DeepEqual(got, overlap) {
		t.Fatalf("overlapping slices round trip: got %#v, err %v", got, err)
	}
	self := make(gentypes.List, 1)
	self[0] = self
	if data, err := gentypes.EncodeList(self); err == nil || data != nil || !strings.Contains(err.Error(), "cyclic Go value") {
		t.Fatalf("self cycle: got %q, err %v", data, err)
	}
	if reflect.ValueOf(self[0]).UnsafePointer() != reflect.ValueOf(self).UnsafePointer() {
		t.Fatal("encoder changed the caller's cycle")
	}
	self[0] = gentypes.List{}
	if _, err := gentypes.EncodeList(self); err != nil {
		t.Fatalf("encode after cycle removed: %v", err)
	}
	a, b := make(gentypes.List, 1), make(gentypes.List, 1)
	a[0], b[0] = b, a
	if data, err := gentypes.EncodeList(a); err == nil || data != nil || !strings.Contains(err.Error(), "cyclic Go value") {
		t.Fatalf("mutual cycle: got %q, err %v", data, err)
	}
}
