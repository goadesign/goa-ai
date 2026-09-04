// Package tests compiles generated collection codecs and checks the raw JSON
// errors that must retain element types and caller-written map keys.
package tests

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	"goa.design/goa-ai/codegen/testhelpers"
)

func TestGeneratedCodecCollectionBehavior(t *testing.T) {
	files := testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", testscenarios.CodecCollections())
	root := writeGeneratedModule(t, files)
	writeGeneratedPackageTest(
		t,
		root,
		"alpha/toolsets/collections/http/validate.go",
		renderedFileContent(t, files, "gen/alpha/toolsets/collections/http/validate.go"),
	)
	writeGeneratedPackageTest(t, root, "alpha/toolsets/collections/codecs_behavior_test.go", `package collections

import (
	"errors"
	"strings"
	"testing"

	"goa.design/goa-ai/runtime/agent/tools"
)

func TestCollectionCodecAcceptsValidRecursiveValuesAndAnyNull(t *testing.T) {
	payload, err := UnmarshalStorePayload([]byte(`+"`"+`{"aliases":["alpha"],"numbers":[1],"large_numbers":[2147483648],"counts":{"site":2},"node":{"name":"one","next":{"name":"two"}},"dynamic":null}`+"`"+`))
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Aliases) != 1 || payload.Aliases[0] != "alpha" {
		t.Fatalf("unexpected aliases: %#v", payload.Aliases)
	}
	if payload.Node == nil || payload.Node.Next == nil || payload.Node.Next.Name != "two" {
		t.Fatalf("unexpected recursive node: %#v", payload.Node)
	}
	if payload.Dynamic != nil {
		t.Fatalf("expected nil dynamic value, got %#v", payload.Dynamic)
	}
}

func TestCollectionCodecRejectsAliasElementsWithTheirPrimitiveType(t *testing.T) {
	for _, test := range []struct {
		name   string
		value  string
		actual string
	}{
		{name: "null", value: "null", actual: "null"},
		{name: "number", value: "7", actual: "number"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := UnmarshalStorePayload([]byte(`+"`"+`{"aliases":[`+"`"+` + test.value + `+"`"+`],"numbers":[1],"large_numbers":[2147483648]}`+"`"+`))
			assertCollectionIssue(t, err, "/aliases/0", "string", test.actual, "Required aliases.")
		})
	}
}

func TestCollectionCodecRejectsInvalidIntegerArrayElements(t *testing.T) {
	for _, test := range []struct {
		name   string
		value  string
		actual string
	}{
		{name: "null", value: "null", actual: "null"},
		{name: "string", value: `+"`"+`"one"`+"`"+`, actual: "string"},
		{name: "fraction", value: "1.5", actual: "number"},
		{name: "overflow", value: "1e100", actual: "number"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := UnmarshalStorePayload([]byte(`+"`"+`{"aliases":["alpha"],"numbers":[`+"`"+` + test.value + `+"`"+`],"large_numbers":[2147483648]}`+"`"+`))
			assertCollectionIssue(t, err, "/numbers/0", "integer", test.actual, "Required integers.")
		})
	}
}

func TestCollectionCodecKeepsIntegerWidthsSeparate(t *testing.T) {
	_, err := UnmarshalStorePayload([]byte(`+"`"+`{"aliases":["alpha"],"numbers":[2147483648],"unsigned_numbers":[2147483648],"large_numbers":[2147483648]}`+"`"+`))
	assertCollectionIssue(t, err, "/numbers/0", "integer", "number", "Required integers.")
}

func TestCollectionCodecAcceptsUnsignedIntegerAboveSignedRange(t *testing.T) {
	payload, err := UnmarshalStorePayload([]byte(`+"`"+`{"aliases":["alpha"],"numbers":[1],"unsigned_numbers":[2147483648],"large_numbers":[2147483648]}`+"`"+`))
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.UnsignedNumbers) != 1 || payload.UnsignedNumbers[0] != 2147483648 {
		t.Fatalf("unexpected unsigned numbers: %#v", payload.UnsignedNumbers)
	}
}

func TestCollectionCodecRejectsNegativeUnsignedInteger(t *testing.T) {
	_, err := UnmarshalStorePayload([]byte(`+"`"+`{"aliases":["alpha"],"numbers":[1],"unsigned_numbers":[-1],"large_numbers":[2147483648]}`+"`"+`))
	assertCollectionIssue(t, err, "/unsigned_numbers/0", "integer", "number", "Unsigned integers.")
}

func TestCollectionCodecUsesTheElementDescription(t *testing.T) {
	_, err := UnmarshalArchivePayload([]byte(`+"`"+`{"aliases":[7],"numbers":[1],"large_numbers":[2147483648]}`+"`"+`))
	assertCollectionIssue(t, err, "/aliases/0", "string", "number", "Archived aliases.")
}

func TestCollectionCodecRejectsInvalidIntegerMapValuesAtEscapedKeys(t *testing.T) {
	for _, value := range []string{"null", `+"`"+`"one"`+"`"+`, "1.5", "1e100"} {
		_, err := UnmarshalStorePayload([]byte(`+"`"+`{"aliases":["alpha"],"numbers":[1],"large_numbers":[2147483648],"counts":{"a/b~c":`+"`"+` + value + `+"`"+`}}`+"`"+`))
		actual := "number"
		if value == "null" {
			actual = "null"
		} else if value == `+"`"+`"one"`+"`"+` {
			actual = "string"
		}
		assertCollectionIssue(t, err, "/counts/a~1b~0c", "integer", actual, "Integer counts by name.")
	}
}

func TestCollectionCodecKeepsArrayIndexesWhenMapsRequireJSONPointer(t *testing.T) {
	_, err := UnmarshalStorePayload([]byte(`+"`"+`{"aliases":["alpha"],"numbers":[1],"large_numbers":[2147483648],"groups":[{"a/b":null}]}`+"`"+`))
	assertCollectionIssue(t, err, "/groups/0/a~1b", "integer", "null", "Integer counts grouped by position.")
}

func TestCollectionMetadataMatchesIndexedArrayMapValues(t *testing.T) {
	field, ok := tools.LookupFieldMetadata(storePayloadFields, "/groups/0/a~1b")
	if !ok || field.Description != "Integer counts grouped by position." {
		t.Fatalf("unexpected description: %#v, %t from %#v", field, ok, storePayloadFields)
	}
	if field.JSONType != "integer" {
		t.Fatalf("unexpected JSON type: %q from %#v", field.JSONType, storePayloadFields)
	}
}

func TestCollectionCodecRejectsUnknownFieldInsideRecursiveType(t *testing.T) {
	_, err := UnmarshalStorePayload([]byte(`+"`"+`{"aliases":["alpha"],"numbers":[1],"large_numbers":[2147483648],"node":{"name":"one","next":{"name":"two","extra":true}}}`+"`"+`))
	if err == nil {
		t.Fatal("expected error")
	}
	var validation *tools.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	issues := validation.Issues()
	if len(issues) != 1 || issues[0].Field != "node.next.extra" || issues[0].Constraint != "unknown_field" {
		t.Fatalf("unexpected issues: %#v", issues)
	}
	if len(issues[0].Allowed) != 2 || issues[0].Allowed[0] != "name" || issues[0].Allowed[1] != "next" {
		t.Fatalf("unexpected allowed fields: %#v", issues[0].Allowed)
	}
}

func TestCollectionCodecRejectsMoreThanOneJSONDocument(t *testing.T) {
	_, err := UnmarshalStorePayload([]byte(`+"`"+`{"aliases":["alpha"],"numbers":[1],"large_numbers":[2147483648]} {}`+"`"+`))
	if err == nil || !strings.Contains(err.Error(), "multiple JSON documents") {
		t.Fatalf("expected one-document error, got %v", err)
	}
}

func assertCollectionIssue(t *testing.T, err error, field, expected, actual, description string) {
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
	if validation.Descriptions()[field] != description {
		t.Fatalf("unexpected descriptions: %#v", validation.Descriptions())
	}
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(
		t,
		root,
		exec.CommandContext(ctx, "go", "test", "-mod=mod", "./alpha/toolsets/collections"),
	)
}
