// Field-derived conversion variables must not collide with codec variables.
package types_test

import (
	"reflect"
	"testing"

	gentypes "codec.local/gen/types"
)

func TestCodecLocalNames(t *testing.T) {
	before := gentypes.Record{
		Data: "data value", Err: "error value", Root: "root value",
		Decoder: "decoder value", In: "input value", Body: "body value", Out: "output value",
	}
	value := before
	data, err := gentypes.EncodeRecord(&value)
	if err != nil {
		t.Fatal(err)
	}
	result, err := gentypes.DecodeRecord(data)
	if err != nil || !reflect.DeepEqual(result, &before) {
		t.Fatalf("round trip returned %#v, %v; want %#v", result, err, before)
	}
	if value != before {
		t.Error("encoding changed the caller's value")
	}
}
