package tests

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	"goa.design/goa-ai/codegen/testhelpers"
	gcodegen "goa.design/goa/v3/codegen"
)

func TestGeneratedCodecInvalidFieldTypeBehavior(t *testing.T) {
	root := writeGeneratedModule(t, testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", testscenarios.ArgsInlineObject()))
	writeGeneratedPackageTest(t, root, "alpha/toolsets/math/http/validate_stub.go", `package http

func ValidateAddPayloadTransport(v *AddPayloadTransport) error {
	return nil
}

func ValidateAddResultTransport(v *AddResultTransport) error {
	return nil
}
`)
	writeGeneratedPackageTest(t, root, "alpha/toolsets/math/codecs_behavior_test.go", `package math

import (
	"errors"
	"testing"

	"goa.design/goa-ai/runtime/agent/tools"
)

func TestUnmarshalAddPayloadInvalidFieldType(t *testing.T) {
	_, err := UnmarshalAddPayload([]byte(`+"`"+`{"left":"one","right":2}`+"`"+`))
	if err == nil {
		t.Fatal("expected error")
	}
	var validation *tools.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	issues := validation.Issues()
	if len(issues) != 1 {
		t.Fatalf("expected one issue, got %d", len(issues))
	}
	issue := issues[0]
	if issue.Field != "left" || issue.Constraint != "invalid_field_type" || issue.ExpectedJSONType != "integer" || issue.ActualJSONType != "string" {
		t.Fatalf("unexpected issue: %#v", issue)
	}
}

func TestUnmarshalAddPayloadReportsFractionalIntegerAsNumber(t *testing.T) {
	_, err := UnmarshalAddPayload([]byte(`+"`"+`{"left":1.5,"right":2}`+"`"+`))
	assertAddIntegerIssue(t, err)
}

func TestUnmarshalAddPayloadReportsOverflowingIntegerAsNumber(t *testing.T) {
	_, err := UnmarshalAddPayload([]byte(`+"`"+`{"left":1e100,"right":2}`+"`"+`))
	assertAddIntegerIssue(t, err)
}

func assertAddIntegerIssue(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	var validation *tools.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	issues := validation.Issues()
	if len(issues) != 1 {
		t.Fatalf("expected one issue, got %d", len(issues))
	}
	issue := issues[0]
	if issue.Field != "left" || issue.Constraint != "invalid_field_type" || issue.ExpectedJSONType != "integer" || issue.ActualJSONType != "number" {
		t.Fatalf("unexpected issue: %#v", issue)
	}
}
`)

	runGeneratedMathGoTest(t, root)
}

func TestGeneratedCodecOpenMapAnyBehavior(t *testing.T) {
	root := writeGeneratedModule(t, testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", testscenarios.ArgsMapAny()))
	writeGeneratedPackageTest(t, root, "alpha/toolsets/records/codecs_behavior_test.go", `package records

import (
	"encoding/json"
	"testing"
)

func TestInspectSchemaClosesPayloadAndKeepsMetadataOpen(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(SpecInspect().Payload.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("root object is not closed: %#v", schema)
	}
	properties := schema["properties"].(map[string]any)
	metadata := properties["metadata"].(map[string]any)
	if metadata["additionalProperties"] == false {
		t.Fatalf("metadata map rejects caller-defined keys: %#v", metadata)
	}
}

func TestUnmarshalInspectPayloadAcceptsArbitraryMapValues(t *testing.T) {
	payload, err := UnmarshalInspectPayload([]byte("{\"metadata\":{\"nested\":{\"ready\":true},\"count\":2}}"))
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Metadata) != 2 {
		t.Fatalf("unexpected metadata: %#v", payload.Metadata)
	}
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(
		t,
		root,
		exec.CommandContext(ctx, "go", "test", "-mod=mod", "./alpha/toolsets/records"),
	)
}

func TestGeneratedCodecUnknownFieldBehavior(t *testing.T) {
	root := writeGeneratedModule(t, testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", testscenarios.DeepNestedValidations()))
	writeGeneratedPackageTest(t, root, "alpha/toolsets/deep/http/validate_stub.go", `package http

func ValidateValidatePayloadTransport(v *ValidatePayloadTransport) error {
	return nil
}

func ValidateValidateResultTransport(v *ValidateResultTransport) error {
	return nil
}
`)
	writeGeneratedPackageTest(t, root, "alpha/toolsets/deep/codecs_behavior_test.go", `package deep

import (
	"encoding/json"
	"errors"
	"testing"

	"goa.design/goa-ai/runtime/agent/tools"
)

func TestValidateSchemaClosesObjectsAndKeepsMapsOpen(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(SpecValidate().Payload.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("root object is not closed: %#v", schema)
	}
	properties := schema["properties"].(map[string]any)
	for _, name := range []string{"counts", "groups", "labels", "objects"} {
		field := properties[name].(map[string]any)
		if field["additionalProperties"] == false {
			t.Fatalf("map %q rejects caller-defined keys: %#v", name, field)
		}
	}
}

func TestUnmarshalValidatePayloadRejectsUnknownRootField(t *testing.T) {
	_, err := UnmarshalValidatePayload([]byte(`+"`"+`{"root":"r","child":{"mid":"m","child":{"leaf":"l"}},"scope_context":"record_group_2"}`+"`"+`))
	if err == nil {
		t.Fatal("expected error")
	}
	assertUnknownFieldIssue(t, err, "scope_context", []string{"child", "counts", "groups", "labels", "objects", "root"})
}

func TestUnmarshalValidatePayloadRejectsUnknownNestedField(t *testing.T) {
	_, err := UnmarshalValidatePayload([]byte(`+"`"+`{"root":"r","child":{"mid":"m","child":{"leaf":"l","extra":true}}}`+"`"+`))
	if err == nil {
		t.Fatal("expected error")
	}
	assertUnknownFieldIssue(t, err, "child.child.extra", []string{"leaf"})
}

func TestUnmarshalValidatePayloadPreservesOpenMapKeys(t *testing.T) {
	payload, err := UnmarshalValidatePayload([]byte(`+"`"+`{"root":"r","child":{"mid":"m","child":{"leaf":"l"}},"labels":{"scope_context":"record_group_2","custom":"value"}}`+"`"+`))
	if err != nil {
		t.Fatal(err)
	}
	if payload.Labels["scope_context"] != "record_group_2" || payload.Labels["custom"] != "value" {
		t.Fatalf("unexpected labels: %#v", payload.Labels)
	}
}

func TestUnmarshalValidatePayloadAcceptsGeneratedMapValueTypes(t *testing.T) {
	payload, err := UnmarshalValidatePayload([]byte(`+"`"+`{"root":"r","child":{"mid":"m","child":{"leaf":"l"}},"labels":{"site.one":"ok"},"objects":{"site/one":{"leaf":"ready"}},"groups":{"group~one":{"sensor/name":"online"}},"counts":{"site":1}}`+"`"+`))
	if err != nil {
		t.Fatal(err)
	}
	if payload.Labels["site.one"] != "ok" {
		t.Fatalf("unexpected labels: %#v", payload.Labels)
	}
	if payload.Objects["site/one"].Leaf != "ready" {
		t.Fatalf("unexpected objects: %#v", payload.Objects)
	}
	if payload.Groups["group~one"]["sensor/name"] != "online" {
		t.Fatalf("unexpected groups: %#v", payload.Groups)
	}
	if payload.Counts["site"] != 1 {
		t.Fatalf("unexpected counts: %#v", payload.Counts)
	}
}

func TestUnmarshalValidatePayloadReportsPrimitiveMapValuePath(t *testing.T) {
	_, err := UnmarshalValidatePayload([]byte(`+"`"+`{"root":"r","child":{"mid":"m","child":{"leaf":"l"}},"labels":{"site":1}}`+"`"+`))
	assertInvalidFieldTypeIssue(t, err, "/labels/site", "string", "number", "Open labels keyed by source")
}

func TestUnmarshalValidatePayloadReportsObjectMapValuePath(t *testing.T) {
	_, err := UnmarshalValidatePayload([]byte(`+"`"+`{"root":"r","child":{"mid":"m","child":{"leaf":"l"}},"objects":{"site.one":"bad"}}`+"`"+`))
	assertInvalidFieldTypeIssue(t, err, "/objects/site.one", "object", "string", "Open objects keyed by source")
}

func TestUnmarshalValidatePayloadReportsNestedMapValuePointer(t *testing.T) {
	_, err := UnmarshalValidatePayload([]byte(`+"`"+`{"root":"r","child":{"mid":"m","child":{"leaf":"l"}},"groups":{"group/one":{"sensor~name":false}}}`+"`"+`))
	assertInvalidFieldTypeIssue(t, err, "/groups/group~1one/sensor~0name", "string", "boolean", "Nested labels keyed by group and source")
}

func assertInvalidFieldTypeIssue(t *testing.T, err error, field, expected, actual, description string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	var validation *tools.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	issues := validation.Issues()
	if len(issues) != 1 {
		t.Fatalf("expected one issue, got %d", len(issues))
	}
	issue := issues[0]
	if issue.Field != field || issue.Constraint != "invalid_field_type" || issue.ExpectedJSONType != expected || issue.ActualJSONType != actual {
		t.Fatalf("unexpected issue: %#v", issue)
	}
	if validation.Descriptions()[field] != description {
		t.Fatalf("unexpected descriptions: %#v", validation.Descriptions())
	}
}

func assertUnknownFieldIssue(t *testing.T, err error, field string, allowed []string) {
	t.Helper()
	var validation *tools.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	issues := validation.Issues()
	if len(issues) != 1 {
		t.Fatalf("expected one issue, got %d", len(issues))
	}
	issue := issues[0]
	if issue.Field != field || issue.Constraint != "unknown_field" {
		t.Fatalf("unexpected issue: %#v", issue)
	}
	if !sameStrings(issue.Allowed, allowed) {
		t.Fatalf("unexpected allowed keys: got %#v want %#v", issue.Allowed, allowed)
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
`)

	runGeneratedDeepGoTest(t, root)
}

func TestGeneratedCodecRequiredUserTypePrimitiveRoundTrip(t *testing.T) {
	files := testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", testscenarios.ArgsUserType())
	root := writeGeneratedModule(t, files)
	writeGeneratedPackageTest(
		t,
		root,
		"alpha/toolsets/docs/http/validate.go",
		fileContent(t, files, "gen/alpha/toolsets/docs/http/validate.go"),
	)
	writeGeneratedPackageTest(t, root, "alpha/toolsets/docs/codecs_required_primitive_test.go", `package docs

import (
	"errors"
	"testing"

	"goa.design/goa-ai/runtime/agent/tools"
)

func TestRequiredUserTypePrimitiveRoundTrip(t *testing.T) {
	want := &StorePayload{}
	data, err := MarshalStorePayload(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnmarshalStorePayload(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || got.Title != want.Title {
		t.Fatalf("unexpected round trip: got %#v want %#v", got, want)
	}

	_, err = UnmarshalStorePayload([]byte(`+"`"+`{"title":"Runbook"}`+"`"+`))
	var validation *tools.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected validation error, got %v", err)
	}
	issues := validation.Issues()
	if len(issues) != 1 || issues[0].Field != "id" || issues[0].Constraint != "missing_field" {
		t.Fatalf("unexpected validation issues: %#v", issues)
	}

	result := &StoreResult{ID: "doc-1"}
	data, err = MarshalStoreResult(result)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := UnmarshalStoreResult(data)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ID != result.ID || decoded.Title != nil {
		t.Fatalf("unexpected optional field round trip: %#v", decoded)
	}
}
`)

	runGeneratedDocsGoTest(t, root)
}

func TestGeneratedCodecBoundedResultProjectionBehavior(t *testing.T) {
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
	writeGeneratedPackageTest(t, root, "alpha/toolsets/lookup/codecs_behavior_test.go", `package lookup

import (
	"errors"
	"testing"

	"goa.design/goa-ai/runtime/agent/tools"
)

func TestUnmarshalSearchResultAcceptsBoundedProjectionFields(t *testing.T) {
	result, err := UnmarshalSearchResult([]byte(`+"`"+`{"results":["record_2"],"returned":1,"truncated":false,"total":3,"next_cursor":"cursor_2","refinement_hint":"narrow the record category"}`+"`"+`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0] != "record_2" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestUnmarshalSearchCopyResultAcceptsBoundedProjectionFields(t *testing.T) {
	result, err := UnmarshalSearchCopyResult([]byte(`+"`"+`{"results":["compressor_2"],"returned":1,"truncated":false,"next_cursor":"cursor_2"}`+"`"+`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0] != "compressor_2" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestUnmarshalSearchAllResultRejectsBoundedProjectionFields(t *testing.T) {
	for _, field := range []string{"returned", "truncated", "next_cursor"} {
		t.Run(field, func(t *testing.T) {
			_, err := UnmarshalSearchAllResult([]byte(`+"`"+`{"results":["compressor_2"],"`+"`"+` + field + `+"`"+`":1}`+"`"+`))
			if err == nil {
				t.Fatal("expected error")
			}
			var validation *tools.ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("expected ValidationError, got %T: %v", err, err)
			}
			issues := validation.Issues()
			if len(issues) != 1 || issues[0].Field != field || issues[0].Constraint != "unknown_field" {
				t.Fatalf("unexpected issues: %#v", issues)
			}
			if !sameStrings(issues[0].Allowed, []string{"results"}) {
				t.Fatalf("unexpected allowed keys: %#v", issues[0].Allowed)
			}
		})
	}
}

func TestUnmarshalSearchResultRejectsUnknownResultField(t *testing.T) {
	_, err := UnmarshalSearchResult([]byte(`+"`"+`{"results":["record_2"],"unexpected":true}`+"`"+`))
	if err == nil {
		t.Fatal("expected error")
	}
	var validation *tools.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	issues := validation.Issues()
	if len(issues) != 1 {
		t.Fatalf("expected one issue, got %d", len(issues))
	}
	issue := issues[0]
	if issue.Field != "unexpected" || issue.Constraint != "unknown_field" {
		t.Fatalf("unexpected issue: %#v", issue)
	}
	if !sameStrings(issue.Allowed, []string{"next_cursor", "refinement_hint", "results", "returned", "total", "truncated"}) {
		t.Fatalf("unexpected allowed keys: %#v", issue.Allowed)
	}
}

func TestUnmarshalSearchResultChecksBoundedProjectionFieldTypes(t *testing.T) {
	tests := []struct {
		name     string
		field    string
		value    string
		expected string
		actual   string
	}{
		{name: "returned null", field: "returned", value: "null", expected: "integer", actual: "null"},
		{name: "returned string", field: "returned", value: `+"`"+`"one"`+"`"+`, expected: "integer", actual: "string"},
		{name: "returned fraction", field: "returned", value: "1.5", expected: "integer", actual: "number"},
		{name: "returned overflow", field: "returned", value: "1e100", expected: "integer", actual: "number"},
		{name: "total null", field: "total", value: "null", expected: "integer", actual: "null"},
		{name: "total boolean", field: "total", value: "true", expected: "integer", actual: "boolean"},
		{name: "total fraction", field: "total", value: "1.5", expected: "integer", actual: "number"},
		{name: "total overflow", field: "total", value: "1e100", expected: "integer", actual: "number"},
		{name: "truncated null", field: "truncated", value: "null", expected: "boolean", actual: "null"},
		{name: "truncated string", field: "truncated", value: `+"`"+`"false"`+"`"+`, expected: "boolean", actual: "string"},
		{name: "refinement hint null", field: "refinement_hint", value: "null", expected: "string", actual: "null"},
		{name: "refinement hint object", field: "refinement_hint", value: "{}", expected: "string", actual: "object"},
		{name: "cursor null", field: "next_cursor", value: "null", expected: "string", actual: "null"},
		{name: "cursor number", field: "next_cursor", value: "2", expected: "string", actual: "number"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := UnmarshalSearchResult([]byte(`+"`"+`{"results":["compressor_2"],"`+"`"+` + test.field + `+"`"+`":`+"`"+` + test.value + `+"`"+`}`+"`"+`))
			assertBoundedTypeIssue(t, err, test.field, test.expected, test.actual)
		})
	}
}

func assertBoundedTypeIssue(t *testing.T, err error, field, expected, actual string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	var validation *tools.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	issues := validation.Issues()
	if len(issues) != 1 {
		t.Fatalf("expected one issue, got %#v", issues)
	}
	issue := issues[0]
	if issue.Field != field || issue.Constraint != "invalid_field_type" || issue.ExpectedJSONType != expected || issue.ActualJSONType != actual {
		t.Fatalf("unexpected issue: %#v", issue)
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
`)

	runGeneratedLookupGoTest(t, root)
}

func TestGeneratedCodecArrayServerDataRoundTrip(t *testing.T) {
	files := testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", testscenarios.ServiceToolsetBindSelfServerData())
	root := writeGeneratedModule(t, files)
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
	writeGeneratedPackageTest(t, root, "alpha/toolsets/lookup/codecs_server_data_test.go", `package lookup

import (
	"strings"
	"testing"
)

func TestArrayServerDataRoundTrip(t *testing.T) {
	want := ByIDRecordsEvidenceServerData{&Evidence{Kind: "summary"}}
	data, err := MarshalByIDRecordsEvidenceServerData(want)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "\"kind\"") || strings.Contains(string(data), "\"Kind\"") {
		t.Fatalf("unexpected JSON field names: %s", data)
	}
	got, err := UnmarshalByIDRecordsEvidenceServerData(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] == nil || got[0].Kind != "summary" {
		t.Fatalf("unexpected round trip: %#v", got)
	}
}

func TestGeneratedServerDataCanonicalizer(t *testing.T) {
	canonicalize := SpecByID().CanonicalizeServerData
	if canonicalize == nil {
		t.Fatal("expected generated canonicalizer")
	}
	got, err := canonicalize([]byte(`+"`"+`[{"kind":"records.evidence","audience":"timeline","data":[ { "kind": "summary" } ]}]`+"`"+`))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `+"`"+`[{"kind":"records.evidence","audience":"timeline","data":[{"kind":"summary"}]}]`+"`"+` {
		t.Fatalf("unexpected canonical envelope: %s", got)
	}

	tests := []struct {
		name string
		data string
	}{
		{
			name: "unknown kind",
			data: `+"`"+`[{"kind":"records.unknown","audience":"timeline","data":[{"kind":"summary"}]}]`+"`"+`,
		},
		{
			name: "wrong audience",
			data: `+"`"+`[{"kind":"records.evidence","audience":"internal","data":[{"kind":"summary"}]}]`+"`"+`,
		},
		{
			name: "invalid payload",
			data: `+"`"+`[{"kind":"records.evidence","audience":"timeline","data":[{"kind":"summary","legacy":true}]}]`+"`"+`,
		},
		{
			name: "duplicate kind",
			data: `+"`"+`[{"kind":"records.evidence","audience":"timeline","data":[{"kind":"summary"}]},{"kind":"records.evidence","audience":"timeline","data":[{"kind":"summary"}]}]`+"`"+`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := canonicalize([]byte(test.data)); err == nil {
				t.Fatal("expected canonicalization error")
			}
		})
	}
}

func TestGeneratedSpecsReturnIsolatedContracts(t *testing.T) {
	first := Specs()
	second := Specs()
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("unexpected specs lengths: %d and %d", len(first), len(second))
	}
	first[0].Description = "changed"
	first[0].ExecutionPayloadSchema[0] = '['
	first[0].Payload.Schema[0] = '['
	for key := range first[0].Payload.FieldDescriptions {
		first[0].Payload.FieldDescriptions[key] = "changed"
	}
	if second[0].Description == "changed" {
		t.Fatal("description mutation escaped returned spec")
	}
	if second[0].ExecutionPayloadSchema[0] == '[' {
		t.Fatal("execution schema mutation escaped returned spec")
	}
	if second[0].Payload.Schema[0] == '[' {
		t.Fatal("schema mutation escaped returned spec")
	}
	for _, value := range second[0].Payload.FieldDescriptions {
		if value == "changed" {
			t.Fatal("field metadata mutation escaped returned spec")
		}
	}
}
`)

	runGeneratedLookupGoTest(t, root)
}

func TestGeneratedProviderMethodAritiesCompile(t *testing.T) {
	tests := []struct {
		name   string
		design func()
		stub   string
		http   string
	}{
		{
			name:   "no result",
			design: testscenarios.NoResultMethod(),
			stub: `package tasks

import "context"

type PurgePayload struct {
	SessionID string
}

type Service interface {
	Purge(context.Context, *PurgePayload) error
	Heartbeat(context.Context) error
}
`,
			http: `package http

func ValidatePurgePayloadTransport(*PurgePayloadTransport) error {
	return nil
}
`,
		},
		{
			name:   "empty payload",
			design: testscenarios.EmptyPayloadResultMethod(),
			stub: `package tasks

import "context"

type Service interface {
	Status(context.Context) (string, error)
}
`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			files := testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", test.design)
			root := writeGeneratedModule(t, files)
			writeGeneratedPackageTest(t, root, "tasks/service.go", test.stub)
			if test.http != "" {
				writeGeneratedPackageTest(t, root, "alpha/toolsets/ops/http/validate_stub.go", test.http)
			}
			if test.name == "no result" {
				writeGeneratedPackageTest(t, root, "alpha/toolsets/ops/no_result_spec_test.go", `package ops

import (
	"testing"

	"goa.design/goa-ai/runtime/agent/tools"
)

func TestNoResultToolsHaveNoResultContract(t *testing.T) {
	for _, spec := range []struct {
		name string
		spec func() tools.ToolSpec
	}{
		{name: "purge", spec: SpecPurge},
		{name: "heartbeat", spec: SpecHeartbeat},
	} {
		result := spec.spec().Result
		if result.Name != "" ||
			len(result.Schema) != 0 ||
			len(result.SchemaWithoutRootExample) != 0 ||
			len(result.ExampleJSON) != 0 ||
			len(result.FieldDescriptions) != 0 ||
			len(result.FieldJSONTypes) != 0 ||
			result.Codec.ToJSON != nil ||
			result.Codec.FromJSON != nil {
			t.Fatalf("%s generated a result contract: %#v", spec.name, result)
		}
	}
}
`)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			runGeneratedGoTestCommand(
				t,
				root,
				exec.CommandContext(ctx, "go", "test", "-mod=mod", "./alpha/toolsets/ops"),
			)
		})
	}
}

func TestGeneratedCodecUnionInvalidFieldTypeBehavior(t *testing.T) {
	files := testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", testscenarios.ArgsUnionSumTypes())
	root := writeGeneratedModule(t, files)
	writeGeneratedPackageTest(
		t,
		root,
		"alpha/toolsets/union/http/validate.go",
		renderedFileContent(t, files, "gen/alpha/toolsets/union/http/validate.go"),
	)
	writeGeneratedPackageTest(t, root, "alpha/toolsets/union/codecs_behavior_test.go", `package union

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestEchoSchemaMatchesClosedGeneratedDecoder(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(SpecEcho().Payload.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("root object is not closed: %#v", schema)
	}
	properties := schema["properties"].(map[string]any)
	value := properties["value"].(map[string]any)
	for _, raw := range value["oneOf"].([]any) {
		branch := raw.(map[string]any)
		if branch["additionalProperties"] != false {
			t.Fatalf("union branch is not closed: %#v", branch)
		}
	}
	definitions := schema["$defs"].(map[string]any)
	structured := definitions["StructuredValue"].(map[string]any)
	if structured["additionalProperties"] != false {
		t.Fatalf("nested branch object is not closed: %#v", structured)
	}
}

func TestInvalidUnionPayloadsReceiveReplacementGuidance(t *testing.T) {
	tests := []struct {
		name      string
		payload   string
		wantField string
	}{
		{name: "missing discriminator", payload: "{\"id\":\"req_1\",\"value\":{\"value\":\"bad\"}}", wantField: "value.type"},
		{name: "non-string discriminator", payload: "{\"id\":\"req_1\",\"value\":{\"type\":7,\"value\":\"bad\"}}", wantField: "value.type"},
		{name: "unknown discriminator", payload: "{\"id\":\"req_1\",\"value\":{\"type\":\"invented\",\"value\":\"bad\"}}", wantField: "value.type"},
		{name: "missing value", payload: "{\"id\":\"req_1\",\"value\":{\"type\":\"structured\"}}", wantField: "value.value"},
		{name: "wrong value type", payload: "{\"id\":\"req_1\",\"value\":{\"type\":\"number\",\"value\":\"bad\"}}", wantField: "value.value"},
		{name: "unknown envelope field", payload: "{\"id\":\"req_1\",\"value\":{\"type\":\"number\",\"value\":7,\"extra\":true}}", wantField: "value.extra"},
		{name: "unknown nested field", payload: "{\"id\":\"req_1\",\"value\":{\"type\":\"structured\",\"value\":{\"label\":\"ready\",\"extra\":true}}}", wantField: "value.value.extra"},
		{name: "invalid branch content", payload: "{\"id\":\"req_1\",\"value\":{\"type\":\"structured\",\"value\":{}}}", wantField: "value.value.label"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := model.ToolDefinitionFromSpec(SpecEcho())
			contract, err := model.NewRequestContract(&model.Request{
				Tools: []*model.ToolDefinition{definition},
			})
			if err != nil {
				t.Fatal(err)
			}
			response := &model.Response{
				Content: []model.Message{{
					Role: model.ConversationRoleAssistant,
					Parts: []model.Part{model.ToolUsePart{
						ID: "call-1",
						Name: string(Echo),
						Input: rawjson.Message(test.payload),
					}},
				}},
				StopReason: "tool_use",
			}
			_, err = contract.ValidateResponse(response)
			var validation *model.OutputValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("expected OutputValidationError, got %T: %v", err, err)
			}
			correction := validation.RecoveryCorrection()
			if correction == "" || !strings.Contains(correction, "replacement tool call") {
				t.Fatalf("expected replacement guidance, got %q", correction)
			}
			if strings.Contains(correction, test.wantField) || strings.Contains(correction, "ready") || strings.Contains(correction, "extra") {
				t.Fatalf("correction exposed rejected arguments: %q", correction)
			}
		})
	}
}

func TestUnmarshalEchoPayloadInvalidUnionEnvelopeType(t *testing.T) {
	_, err := UnmarshalEchoPayload([]byte(`+"`"+`{"id":"req_1","value":"bad"}`+"`"+`))
	if err == nil {
		t.Fatal("expected error")
	}
	var validation *tools.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	issues := validation.Issues()
	if len(issues) != 1 {
		t.Fatalf("expected one issue, got %d", len(issues))
	}
	issue := issues[0]
	if issue.Field != "value" || issue.Constraint != "invalid_field_type" || issue.ExpectedJSONType != "object" || issue.ActualJSONType != "string" {
		t.Fatalf("unexpected issue: %#v", issue)
	}
}

func TestUnmarshalEchoPayloadUsesSelectedUnionBranchType(t *testing.T) {
	_, err := UnmarshalEchoPayload([]byte(`+"`"+`{"id":"req_1","value":{"type":"number","value":"bad"}}`+"`"+`))
	if err == nil {
		t.Fatal("expected error")
	}
	var validation *tools.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	issues := validation.Issues()
	if len(issues) != 1 {
		t.Fatalf("expected one issue, got %d", len(issues))
	}
	issue := issues[0]
	if issue.Field != "value" || issue.Constraint != "invalid_field_type" || issue.ExpectedJSONType != "integer" || issue.ActualJSONType != "string" {
		t.Fatalf("unexpected issue: %#v", issue)
	}
}

func TestUnmarshalEchoPayloadRejectsMissingUnionValue(t *testing.T) {
	_, err := UnmarshalEchoPayload([]byte(`+"`"+`{"id":"req_1","value":{"type":"structured"}}`+"`"+`))
	if err == nil {
		t.Fatal("expected error")
	}
	var validation *tools.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	issues := validation.Issues()
	if len(issues) != 1 {
		t.Fatalf("expected one issue, got %d", len(issues))
	}
	issue := issues[0]
	if issue.Field != "value" || issue.Constraint != "missing_field" {
		t.Fatalf("unexpected issue: %#v", issue)
	}
}

func TestUnmarshalEchoPayloadRejectsMissingRequiredUnion(t *testing.T) {
	_, err := UnmarshalEchoPayload([]byte(`+"`"+`{"id":"req_1"}`+"`"+`))
	if err == nil {
		t.Fatal("expected error")
	}
	var validation *tools.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	issues := validation.Issues()
	if len(issues) != 1 || issues[0].Field != "value" || issues[0].Constraint != "missing_field" {
		t.Fatalf("unexpected issues: %#v", issues)
	}
}

func TestUnmarshalEchoPayloadRejectsNullUnionValue(t *testing.T) {
	_, err := UnmarshalEchoPayload([]byte(`+"`"+`{"id":"req_1","value":{"type":"structured","value":null}}`+"`"+`))
	if err == nil {
		t.Fatal("expected error")
	}
	var validation *tools.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	issues := validation.Issues()
	if len(issues) != 1 {
		t.Fatalf("expected one issue, got %d", len(issues))
	}
	issue := issues[0]
	if issue.Field != "value" || issue.Constraint != "invalid_field_type" || issue.ExpectedJSONType != "object" || issue.ActualJSONType != "null" {
		t.Fatalf("unexpected issue: %#v", issue)
	}
}

func TestValueValidateRejectsNilSelectedBranch(t *testing.T) {
	value := NewValueStructured(nil)
	err := value.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	var validation *tools.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	issues := validation.Issues()
	if len(issues) != 1 || issues[0].Field != "value" || issues[0].Constraint != "missing_field" {
		t.Fatalf("unexpected issues: %#v", issues)
	}
}

func TestMarshalEchoPayloadRejectsMissingRequiredUnion(t *testing.T) {
	_, err := MarshalEchoPayload(&EchoPayload{ID: "req_1"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestOptionalUnionOmissionAndRoundTrip(t *testing.T) {
	payload := &EchoPayload{
		ID:    "req_1",
		Value: NewValueText("required"),
	}
	data, err := MarshalEchoPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "optional_value") {
		t.Fatalf("omitted optional union was serialized: %s", data)
	}
	decoded, err := UnmarshalEchoPayload(data)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.OptionalValue.Kind() != "" {
		t.Fatalf("omitted optional union became present: %#v", decoded.OptionalValue)
	}

	optional := NewOptionalValueNumber(7)
	payload.OptionalValue = optional
	data, err = MarshalEchoPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err = UnmarshalEchoPayload(data)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.OptionalValue.Kind() == "" {
		t.Fatal("present optional union was omitted")
	}
	got, ok := decoded.OptionalValue.AsNumber()
	if !ok || got != 7 {
		t.Fatalf("unexpected optional union: %#v", decoded.OptionalValue)
	}
}

func TestOptionalUnionZeroValueIsOmitted(t *testing.T) {
	payload := &EchoPayload{
		ID:            "req_1",
		Value:         NewValueText("required"),
		OptionalValue: OptionalValue{},
	}
	data, err := MarshalEchoPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "optional_value") {
		t.Fatalf("zero optional union was serialized: %s", data)
	}

	// A present JSON object is not omission and must still select one branch.
	if _, err := UnmarshalEchoPayload([]byte("{\"id\":\"req_1\",\"value\":{\"type\":\"text\",\"value\":\"required\"},\"optional_value\":{}}")); err == nil {
		t.Fatal("expected malformed JSON optional union to fail")
	}
}

func TestUnmarshalEchoPayloadRejectsUnknownUnionBranchFields(t *testing.T) {
	_, err := UnmarshalEchoPayload([]byte(`+"`"+`{"id":"req_1","value":{"type":"structured","value":{"label":"ready","extra":true}}}`+"`"+`))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unknown field") || !strings.Contains(err.Error(), "extra") {
		t.Fatalf("expected unknown field error for extra, got %v", err)
	}
}
`)

	runGeneratedUnionGoTest(t, root)
}

func TestGeneratedCodecModelJSONNamesBehavior(t *testing.T) {
	root := writeGeneratedModule(t, testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", testscenarios.ModelJSONNames()))
	writeGeneratedPackageTest(t, root, "alpha/toolsets/review/http/validate_stub.go", `package http

func ValidateReviewRecordPayloadTransport(v *ReviewRecordPayloadTransport) error {
	return nil
}

func ValidateReviewRecordResultTransport(v *ReviewRecordResultTransport) error {
	return nil
}
`)
	writeGeneratedPackageTest(t, root, "alpha/toolsets/review/codecs_behavior_test.go", `package review

import (
	"strings"
	"testing"
)

func TestUnmarshalReviewRecordPayloadAcceptsSnakeCase(t *testing.T) {
	payload, err := UnmarshalReviewRecordPayload([]byte(`+"`"+`{"record_key":"record_1","include_details":true,"source_ids":["source_1"],"time_context":{"start_time":"2026-01-01T00:00:00Z","end_time":"2026-01-01T01:00:00Z"}}`+"`"+`))
	if err != nil {
		t.Fatal(err)
	}
	if payload.RecordKey != "record_1" || !payload.IncludeDetails || len(payload.SourceIds) != 1 || payload.SourceIds[0] != "source_1" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if payload.TimeContext.StartTime != "2026-01-01T00:00:00Z" || payload.TimeContext.EndTime != "2026-01-01T01:00:00Z" {
		t.Fatalf("unexpected time context: %#v", payload.TimeContext)
	}
}

func TestUnmarshalReviewRecordPayloadRejectsLowerCamel(t *testing.T) {
	_, err := UnmarshalReviewRecordPayload([]byte(`+"`"+`{"recordKey":"record_1"}`+"`"+`))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unknown field") || !strings.Contains(err.Error(), "recordKey") {
		t.Fatalf("expected unknown field error for recordKey, got %v", err)
	}
}

func TestMarshalReviewRecordPayloadEmitsSnakeCase(t *testing.T) {
	payload := &ReviewRecordPayload{
		RecordKey:      "record_1",
		IncludeDetails: true,
		SourceIds:      []string{"source_1"},
		TimeContext: &TimeContext{
			StartTime: "2026-01-01T00:00:00Z",
			EndTime:   "2026-01-01T01:00:00Z",
		},
	}
	data, err := MarshalReviewRecordPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{"record_key", "include_details", "source_ids", "time_context", "start_time", "end_time"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in %s", want, got)
		}
	}
	for _, forbidden := range []string{"recordKey", "includeDetails", "sourceIds", "timeContext", "startTime", "endTime"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("did not expect %q in %s", forbidden, got)
		}
	}
}
`)

	runGeneratedReviewGoTest(t, root)
}

func writeGeneratedModule(t *testing.T, files []*gcodegen.File) string {
	t.Helper()
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../../..")
	require.NoError(t, err)
	goMod := "module generated.local/gen\n\ngo 1.24\n\nrequire goa.design/goa-ai v0.0.0\n\nreplace goa.design/goa-ai => " + filepath.ToSlash(repoRoot) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte(goMod), 0o600))
	for _, file := range files {
		rel := strings.TrimPrefix(filepath.ToSlash(file.Path), "gen/")
		if strings.HasSuffix(rel, "/http/validate.go") {
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(fileContent(t, files, file.Path)), 0o600))
	}
	return root
}

func writeGeneratedPackageTest(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func removeGeneratedPackageFile(t *testing.T, root, rel string) {
	t.Helper()
	require.NoError(t, os.Remove(filepath.Join(root, filepath.FromSlash(rel))))
}

func runGeneratedMathGoTest(t *testing.T, root string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "./alpha/toolsets/math"))
}

func runGeneratedDeepGoTest(t *testing.T, root string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "./alpha/toolsets/deep"))
}

func runGeneratedDocsGoTest(t *testing.T, root string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "./alpha/toolsets/docs"))
}

func runGeneratedLookupGoTest(t *testing.T, root string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "./alpha/toolsets/lookup"))
}

func runGeneratedUnionGoTest(t *testing.T, root string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "./alpha/toolsets/union"))
}

func runGeneratedReviewGoTest(t *testing.T, root string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "./alpha/toolsets/review"))
}

func runGeneratedGoTestCommand(t *testing.T, root string, cmd *exec.Cmd) {
	t.Helper()
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s failed: %v\n%s", strings.Join(cmd.Args, " "), err, out.String())
	}
}
