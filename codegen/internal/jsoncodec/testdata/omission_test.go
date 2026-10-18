package types_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	gentypes "codec.local/gen/types"
)

func TestAuthoredOmissionRoundTrip(t *testing.T) {
	for _, nonzero := range []bool{false, true} {
		name := "zero"
		if nonzero {
			name = "nonzero"
		}
		t.Run(name, func(t *testing.T) {
			explicit := false
			policy := &gentypes.Policy{Explicit: &explicit}
			if nonzero {
				policy.Enabled = true
				policy.Count = 3
				policy.Ratio = 0.5
				policy.Title = "reader"
				policy.Flag = true
			}
			original := &gentypes.Envelope{Policy: policy, Policies: []*gentypes.Policy{policy}}
			encoded, err := gentypes.EncodeEnvelope(original)
			if err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"enabled", "count", "ratio", "title", "flag"} {
				if present := bytes.Contains(encoded, []byte(`"`+field+`":`)); present != nonzero {
					t.Fatalf("field %s presence = %t, want %t: %s", field, present, nonzero, encoded)
				}
			}
			for _, field := range []string{"required", "explicit", "default_true", "Untagged", "named"} {
				if !bytes.Contains(encoded, []byte(`"`+field+`":false`)) {
					t.Fatalf("lost explicit false for %s: %s", field, encoded)
				}
			}
			if !bytes.Contains(encoded, []byte(`"default_count":0`)) {
				t.Fatalf("lost zero overriding nonzero default: %s", encoded)
			}
			decoded, err := gentypes.DecodeEnvelope(encoded)
			if err != nil || !reflect.DeepEqual(original, decoded) {
				t.Fatalf("round trip changed values: %#v %v", decoded, err)
			}
			// Encoding the same child directly and inside an envelope must apply
			// the same omission rules, so saving either shape retains the policy.
			direct, err := gentypes.EncodePolicy(policy)
			if err != nil {
				t.Fatal(err)
			}
			var envelope struct{ Policy json.RawMessage }
			if err := json.Unmarshal(encoded, &envelope); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(direct, envelope.Policy) {
				t.Fatalf("nested encoding differs: %s / %s", direct, envelope.Policy)
			}
		})
	}
}

func TestOmittedDefaultsAndStrictPresence(t *testing.T) {
	decoded, err := gentypes.DecodePolicy([]byte(`{"required":false}`))
	if err != nil || decoded.Enabled || decoded.Count != 0 || decoded.Title != "" || !decoded.DefaultTrue || decoded.DefaultCount != 7 {
		t.Fatalf("omitted defaults changed: %#v %v", decoded, err)
	}
	for _, document := range []string{`{}`, `{"required":false,"enabled":null}`, `{"required":null}`} {
		if _, err := gentypes.DecodePolicy([]byte(document)); err == nil {
			t.Fatalf("accepted invalid presence: %s", document)
		}
	}
}
