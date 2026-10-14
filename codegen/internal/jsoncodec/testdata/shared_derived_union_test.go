package exchange_test

import (
	"reflect"
	"strings"
	"testing"

	genexchange "codec.local/gen/exchange"
	genshared "codec.local/gen/shared"
)

func TestSharedDerivedRoundTrip(t *testing.T) {
	detail := &genshared.Detail{Text: "shared child"}
	for _, input := range []*genexchange.Combined{
		{Original: &genshared.Envelope{Choice: genshared.NewSelectionText("original")}, Derived: &genexchange.DerivedEnvelope{Choice: genexchange.NewSelectionText("derived")}},
		{Original: &genshared.Envelope{Choice: genshared.NewSelectionDetail(detail)}, Derived: &genexchange.DerivedEnvelope{Choice: genexchange.NewSelectionDetail(detail)}},
	} {
		data, err := genexchange.EncodeCombined(input)
		if err != nil {
			t.Fatal(err)
		}
		got, err := genexchange.DecodeCombined(data)
		if err != nil || !reflect.DeepEqual(input, got) {
			t.Fatalf("round trip: got %#v, error %v", got, err)
		}
	}
}

func TestSharedDerivedInvalidText(t *testing.T) {
	bad := string([]byte{0xff})
	cases := []struct {
		name     string
		original genshared.Selection
		derived  genexchange.Selection
	}{
		{"original primitive", genshared.NewSelectionText(genshared.SelectionBranchText(bad)), genexchange.NewSelectionText("valid")},
		{"derived primitive", genshared.NewSelectionText("valid"), genexchange.NewSelectionText(genexchange.SelectionBranchText(bad))},
		{"original object", genshared.NewSelectionDetail(&genshared.Detail{Text: bad}), genexchange.NewSelectionText("valid")},
		{"derived object", genshared.NewSelectionText("valid"), genexchange.NewSelectionDetail(&genshared.Detail{Text: bad})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := &genexchange.Combined{Original: &genshared.Envelope{Choice: tc.original}, Derived: &genexchange.DerivedEnvelope{Choice: tc.derived}}
			if data, err := genexchange.EncodeCombined(input); data != nil || err == nil || !strings.Contains(err.Error(), "invalid UTF-8") {
				t.Fatalf("got %q, error %v", data, err)
			}
		})
	}
}

func TestSharedDerivedCycle(t *testing.T) {
	for _, derived := range []bool{false, true} {
		detail := &genshared.Detail{Text: "node"}
		detail.Next = detail
		input := &genexchange.Combined{Original: &genshared.Envelope{Choice: genshared.NewSelectionText("valid")}, Derived: &genexchange.DerivedEnvelope{Choice: genexchange.NewSelectionText("valid")}}
		if derived {
			input.Derived.Choice = genexchange.NewSelectionDetail(detail)
		} else {
			input.Original.Choice = genshared.NewSelectionDetail(detail)
		}
		if data, err := genexchange.EncodeCombined(input); data != nil || err == nil || !strings.Contains(err.Error(), "cyclic Go value") {
			t.Fatalf("derived=%v: got %q, error %v", derived, data, err)
		}
		if detail.Next != detail {
			t.Fatal("encoder changed caller's cycle")
		}
		detail.Next = nil
		if _, err := genexchange.EncodeCombined(input); err != nil {
			t.Fatal(err)
		}
	}
}
