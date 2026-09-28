// These tests use generated protobuf conversions and a force-generated JSON
// envelope. Valid empty collections must pass both contracts without repair.
package collections_test

import (
	"bytes"
	"testing"

	gen "codec.local/gen/collections"
	genclient "codec.local/gen/grpc/collections/client"
	genpb "codec.local/gen/grpc/collections/pb"
	genserver "codec.local/gen/grpc/collections/server"
	"google.golang.org/protobuf/proto"
)

// TestWireCollectionsReachStrictCodec checks both receiving conversion paths.
func TestWireCollectionsReachStrictCodec(t *testing.T) {
	for _, direction := range []string{"request", "response"} {
		for _, shape := range []string{"empty", "populated", "child", "recursive", "optional"} {
			t.Run(direction+"/"+shape, func(t *testing.T) {
				in := record(shape)
				expected, err := gen.EncodeEnvelope(&gen.Envelope{Record: in})
				if err != nil {
					t.Fatalf("input must satisfy the strict codec before protobuf: %v", err)
				}

				var out *gen.Record
				if direction == "request" {
					data, err := proto.Marshal(genclient.NewProtoExchangeRequest(in))
					if err != nil {
						t.Fatal(err)
					}
					wire := new(genpb.ExchangeRequest)
					if err := proto.Unmarshal(data, wire); err != nil {
						t.Fatal(err)
					}
					if shape != "populated" && (wire.Items != nil || wire.Labels != nil) {
						t.Fatal("request did not exercise protobuf empty collection collapse")
					}
					if err := genserver.ValidateExchangeRequest(wire); err != nil {
						t.Fatalf("valid protobuf request rejected: %v", err)
					}
					out = genserver.NewExchangePayload(wire)
				} else {
					data, err := proto.Marshal(genserver.NewProtoExchangeResponse(in))
					if err != nil {
						t.Fatal(err)
					}
					wire := new(genpb.ExchangeResponse)
					if err := proto.Unmarshal(data, wire); err != nil {
						t.Fatal(err)
					}
					if shape != "populated" && (wire.Items != nil || wire.Labels != nil) {
						t.Fatal("response did not exercise protobuf empty collection collapse")
					}
					if err := genclient.ValidateExchangeResponse(wire); err != nil {
						t.Fatalf("valid protobuf response rejected: %v", err)
					}
					out = genclient.NewExchangeResult(wire)
				}

				actual, err := gen.EncodeEnvelope(&gen.Envelope{Record: out})
				if err != nil {
					t.Fatalf("validated protobuf value cannot reach strict JSON: %v", err)
				}
				if !bytes.Equal(expected, actual) {
					t.Errorf("record changed across protobuf: want %s, got %s", expected, actual)
				}
				decoded, err := gen.DecodeEnvelope(actual)
				if err != nil {
					t.Fatal(err)
				}
				again, err := gen.EncodeEnvelope(decoded)
				if err != nil || !bytes.Equal(actual, again) {
					t.Errorf("strict JSON round trip changed the record: %v", err)
				}
				if (out.Node == nil) != (in.Node == nil) {
					t.Error("optional message presence changed")
				}
				if (out.Note == nil) != (in.Note == nil) {
					t.Error("optional scalar presence changed")
				}
				if out.Note != nil && in.Note != nil && *out.Note != *in.Note {
					t.Error("optional scalar value changed")
				}
				if in.OptionalItems == nil && out.OptionalItems != nil {
					t.Error("absent optional array was allocated")
				}
				if in.OptionalLabels == nil && out.OptionalLabels != nil {
					t.Error("absent optional map was allocated")
				}
			})
		}
	}
}

// TestRequiredJSONCollectionsStayStrict retains the incoming JSON contract.
func TestRequiredJSONCollectionsStayStrict(t *testing.T) {
	for _, data := range []string{
		`{"labels":{}}`,
		`{"items":null,"labels":{}}`,
		`{"items":[]}`,
		`{"items":[],"labels":null}`,
		`{"items":[],"labels":{},"node":{"labels":{}}}`,
		`{"items":[],"labels":{},"node":{"items":null,"labels":{}}}`,
	} {
		if value, err := gen.DecodeEnvelope([]byte(`{"record":` + data + `}`)); err == nil || value != nil {
			t.Errorf("invalid required JSON produced a usable record: %s", data)
		}
	}
}

// TestOriginalNilContractIsUnchanged keeps direct typed nil rejection separate
// from constructing a valid service value at the protobuf boundary.
func TestOriginalNilContractIsUnchanged(t *testing.T) {
	for _, field := range []string{"items", "labels"} {
		in := record("empty")
		if field == "items" {
			in.Items = nil
		} else {
			in.Labels = nil
		}
		if data, err := gen.EncodeEnvelope(&gen.Envelope{Record: in}); err == nil || data != nil {
			t.Errorf("the converter correction changed original %s admission", field)
		}
	}
}

// record supplies explicitly empty collections before any transport conversion.
func record(shape string) *gen.Record {
	value := &gen.Record{
		Items:  []*gen.Item{},
		Labels: map[string]string{},
	}
	switch shape {
	case "populated":
		value.Items = []*gen.Item{{Label: "first"}, {Label: "second"}}
		value.Labels["key"] = "value"
	case "child":
		value.Node = &gen.Node{Items: []*gen.Item{}, Labels: map[string]string{}}
	case "recursive":
		leaf := &gen.Node{Items: []*gen.Item{}, Labels: map[string]string{}}
		value.Node = &gen.Node{
			Items:    []*gen.Item{},
			Labels:   map[string]string{},
			Child:    leaf,
			Children: []*gen.Node{leaf},
			ByName:   map[string]*gen.Node{"leaf": leaf},
		}
	case "optional":
		note := ""
		value.Note = &note
		value.OptionalItems = []*gen.Item{{Label: "optional"}}
		value.OptionalLabels = map[string]string{"optional": "value"}
	}
	return value
}
