// These tests run emitted tool codecs against actual result-envelope bytes.
// They reuse the existing generated-module harness and supported tool designs;
// they do not introduce original codecs for Any or custom Go types.
package tests

import (
	"testing"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	"goa.design/goa-ai/codegen/testhelpers"
)

func TestGeneratedTerminalEnvelopeServerDataRoundTrip(t *testing.T) {
	root := writeGeneratedModule(t, testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", testscenarios.ServiceToolsetBindSelfServerData()))
	removeGeneratedPackageFile(t, root, "alpha/toolsets/lookup/provider.go")
	removeGeneratedPackageFile(t, root, "alpha/toolsets/lookup/transforms.go")
	writeGeneratedPackageTest(t, root, "alpha/toolsets/lookup/http/validate_stub.go", `package http

func ValidateByIDPayloadTransport(v *ByIDPayloadTransport) error {
	return nil
}

func ValidateByIDResultTransport(v *ByIDResultTransport) error {
	return nil
}

func ValidateByIDRecordsEvidenceServerDataTransport(v ByIDRecordsEvidenceServerDataTransport) error {
	return nil
}
`)
	writeGeneratedPackageTest(t, root, "alpha/toolsets/lookup/terminal_envelope_test.go", `package lookup

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/goa-ai/runtime/toolserverdata"
)

func TestGeneratedResultEnvelopeRoundTrip(t *testing.T) {
	spec := SpecByID()
	want, err := spec.Result.Codec.FromJSON([]byte("{\"ok\":true}"))
	if err != nil {
		t.Fatal(err)
	}
	resultJSON, err := spec.Result.Codec.ToJSON(want)
	if err != nil {
		t.Fatal(err)
	}
	evidence := ByIDRecordsEvidenceServerData{&Evidence{Kind: "summary"}}
	data, err := MarshalByIDRecordsEvidenceServerData(evidence)
	if err != nil {
		t.Fatal(err)
	}
	wire := toolregistry.NewToolResultMessageWithServerData(
		strings.Repeat("a", 64), "generated-result", resultJSON,
		[]*toolregistry.ServerDataItem{{Kind: "records.evidence", Audience: "timeline", Data: data}},
	)
	body, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	message, err := toolregistry.DecodeToolResultMessage(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := toolregistry.ValidateToolResultMessage(message); err != nil {
		t.Fatal(err)
	}
	got, err := spec.Result.Codec.FromJSON(message.Result)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("semantic result changed: %#v", got)
	}
	serverJSON, err := toolregistry.EncodeServerData(message.ServerData)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := toolserverdata.Apply(spec.CanonicalizeServerData, rawjson.Message(serverJSON))
	if err != nil {
		t.Fatal(err)
	}
	items, err := toolregistry.DecodeServerData(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Audience != "timeline" {
		t.Fatalf("unexpected server data: %#v", items)
	}
	restored, err := UnmarshalByIDRecordsEvidenceServerData(items[0].Data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(evidence, restored) {
		t.Fatalf("server data changed: %#v", restored)
	}
}
`)
	runGeneratedLookupGoTest(t, root)
}

func TestGeneratedTerminalEnvelopeBoundedResultRoundTrip(t *testing.T) {
	root := writeGeneratedModule(t, testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", testscenarios.ServiceToolsetBindSelfBoundedResult()))
	removeGeneratedPackageFile(t, root, "alpha/toolsets/lookup/provider.go")
	removeGeneratedPackageFile(t, root, "alpha/toolsets/lookup/transforms.go")
	writeGeneratedPackageTest(t, root, "alpha/toolsets/lookup/http/validate_stub.go", `package http

func ValidateSearchPayloadTransport(v *SearchPayloadTransport) error {
	return nil
}

func ValidateSearchResultTransport(v *SearchResultTransport) error {
	return nil
}

func ValidateSearchCopyPayloadTransport(v *SearchCopyPayloadTransport) error {
	return nil
}

func ValidateSearchCopyResultTransport(v *SearchCopyResultTransport) error {
	return nil
}

func ValidateSearchAllPayloadTransport(v *SearchAllPayloadTransport) error {
	return nil
}

func ValidateSearchAllResultTransport(v *SearchAllResultTransport) error {
	return nil
}
`)
	writeGeneratedPackageTest(t, root, "alpha/toolsets/lookup/terminal_envelope_test.go", `package lookup

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/toolregistry"
)

func TestGeneratedBoundedResultEnvelopeRoundTrip(t *testing.T) {
	spec := SpecSearch()
	want, err := spec.Result.Codec.FromJSON([]byte("{\"results\":[\"record_2\"]}"))
	if err != nil {
		t.Fatal(err)
	}
	resultJSON, err := spec.Result.Codec.ToJSON(want)
	if err != nil {
		t.Fatal(err)
	}
	wire := toolregistry.NewToolResultMessage(strings.Repeat("a", 64), "bounded-result", resultJSON)
	wire.Bounds = &agent.Bounds{Returned: 1, Truncated: false}
	body, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	message, err := toolregistry.DecodeToolResultMessage(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := toolregistry.ValidateToolResultMessage(message); err != nil {
		t.Fatal(err)
	}
	got, err := spec.Result.Codec.FromJSON(message.Result)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) || !reflect.DeepEqual(wire.Bounds, message.Bounds) {
		t.Fatalf("result or bounds changed: %#v, %#v", got, message.Bounds)
	}
	if message.Bounds.Total != nil || message.Bounds.NextCursor != nil {
		t.Fatal("absent optional bounds became populated")
	}
}
`)
	runGeneratedLookupGoTest(t, root)
}
